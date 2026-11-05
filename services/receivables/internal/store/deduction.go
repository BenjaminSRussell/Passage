package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
)

// CodeDeductionNotFound answers a deduction id the caller cannot see.
const CodeDeductionNotFound = "AR-DEDUCTION-NOT-FOUND"

// Deduction is the unexplained part of a short payment, moved off the short-paid
// open item into the deductions workbench (FR-30.17, BR-30.12). Children is
// filled on a single read of a SPLIT deduction.
type Deduction struct {
	ID             string           `json:"id"`
	LegalEntityID  string           `json:"legalEntityId"`
	CustomerID     string           `json:"customerId"`
	OpenItemID     string           `json:"openItemId"`
	DocumentNumber string           `json:"documentNumber"`
	PaymentID      *string          `json:"paymentId,omitempty"`
	ParentID       *string          `json:"parentId,omitempty"`
	Type           string           `json:"type"`
	ReasonCode     string           `json:"reasonCode"`
	Currency       string           `json:"currency"`
	Amount         decimal.Decimal  `json:"amount"`
	Status         string           `json:"status"`
	ClaimType      *string          `json:"claimType,omitempty"`
	ClaimRef       *string          `json:"claimRef,omitempty"`
	ClaimRemaining *decimal.Decimal `json:"claimRemaining,omitempty"`
	DenialReason   *string          `json:"denialReason,omitempty"`
	DecisionNote   *string          `json:"decisionNote,omitempty"`
	RebillItemID   *string          `json:"rebillItemId,omitempty"`
	CreatedAt      time.Time        `json:"createdAt"`
	ResolvedAt     *time.Time       `json:"resolvedAt,omitempty"`
	Version        int              `json:"version"`
	Children       []Deduction      `json:"children,omitempty"`
}

// DeductionFilter narrows a deduction list.
type DeductionFilter struct {
	Status     string
	Type       string
	CustomerID string
}

// ClaimMatch is the wave 26 claim a deduction is matched to. Until the pricing
// claims API exists the clerk states the claim's remaining amount.
type ClaimMatch struct {
	Type      string
	Ref       string
	Remaining decimal.Decimal
}

const deductionColumns = `d.id, d.legal_entity_id, d.customer_id, d.open_item_id, oi.document_number, d.payment_id, d.parent_id,
	d.deduction_type, d.reason_code, d.currency, d.amount::text, d.status, d.claim_type, d.claim_ref, d.claim_remaining::text,
	d.denial_reason, d.decision_note, d.rebill_item_id, d.created_at, d.resolved_at, d.version`

const deductionFrom = ` from deduction d join open_item oi on oi.tenant_id = d.tenant_id and oi.id = d.open_item_id`

func scanDeduction(r rowScanner) (Deduction, error) {
	var d Deduction
	var amount string
	var remaining *string
	if err := r.Scan(&d.ID, &d.LegalEntityID, &d.CustomerID, &d.OpenItemID, &d.DocumentNumber, &d.PaymentID, &d.ParentID,
		&d.Type, &d.ReasonCode, &d.Currency, &amount, &d.Status, &d.ClaimType, &d.ClaimRef, &remaining,
		&d.DenialReason, &d.DecisionNote, &d.RebillItemID, &d.CreatedAt, &d.ResolvedAt, &d.Version); err != nil {
		return Deduction{}, err
	}
	var err error
	if d.Amount, err = decimal.NewFromString(amount); err != nil {
		return Deduction{}, err
	}
	if remaining != nil {
		v, err := decimal.NewFromString(*remaining)
		if err != nil {
			return Deduction{}, err
		}
		d.ClaimRemaining = &v
	}
	return d, nil
}

func readDeduction(ctx context.Context, tx pgx.Tx, where string, args ...any) (Deduction, error) {
	return scanDeduction(tx.QueryRow(ctx, `select `+deductionColumns+deductionFrom+` where `+where, args...))
}

// sameDeduction compares the fields a client controls, for idempotent replay.
func sameDeduction(stored, in Deduction) bool {
	return stored.LegalEntityID == in.LegalEntityID && stored.OpenItemID == in.OpenItemID && eqStrPtr(stored.PaymentID, in.PaymentID) &&
		stored.Type == in.Type && stored.ReasonCode == in.ReasonCode && stored.Amount.Equal(in.Amount)
}

// CreateDeduction moves amount off a short-paid debit item into a new OPEN
// deduction and enqueues deduction.opened.v1. The item is locked NOWAIT
// (BR-30.7); it must be open, undisputed, of the deduction's legal entity and
// hold at least amount. The deduction takes the item's customer and currency.
// A given payment must be of the same legal entity, currency and (when it is
// identified) customer. A repeated key returns the stored deduction when the
// request matches.
func (s *Postgres) CreateDeduction(ctx context.Context, in Deduction, key string) (Deduction, error) {
	p, _ := tenant.FromContext(ctx)
	var out Deduction
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay, err := readDeduction(ctx, tx, `d.idempotency_key = $1`, key)
		if err == nil {
			if !sameDeduction(replay, in) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
			}
			out = replay
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		item, err := lockOpenItem(ctx, tx, in.OpenItemID)
		if err != nil {
			return err
		}
		if item.LegalEntityID != in.LegalEntityID {
			return httpx.NewProblem(http.StatusBadRequest, domain.CodeDeductionMismatch, "open item "+item.DocumentNumber+" belongs to another legal entity")
		}
		if item.Status != "OPEN" && item.Status != "PARTIALLY_SETTLED" {
			return domainProblem(domain.CodeDeductionState, "open item "+item.DocumentNumber+" is "+item.Status)
		}
		var disputed bool
		if err := tx.QueryRow(ctx, `select dispute_id is not null from open_item where id = $1`, item.ID).Scan(&disputed); err != nil {
			return err
		}
		if disputed {
			return domainProblem(domain.CodeDisputeExists, "open item "+item.DocumentNumber+" has an active dispute; resolve it before taking a deduction")
		}
		if in.Amount.GreaterThan(item.OpenAmount) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeAmountRange,
				fmt.Sprintf("deduction %s exceeds the open amount %s of %s", in.Amount, item.OpenAmount, item.DocumentNumber))
		}
		if in.PaymentID != nil {
			pay, err := scanPayment(tx.QueryRow(ctx, `select `+paymentColumns+` from payment where id = $1`, *in.PaymentID))
			if err != nil {
				return notFound(err, CodePaymentNotFound, "payment not found")
			}
			if pay.LegalEntityID != item.LegalEntityID || pay.Currency != item.Currency || (pay.CustomerID != nil && *pay.CustomerID != item.CustomerID) {
				return httpx.NewProblem(http.StatusBadRequest, domain.CodeDeductionMismatch,
					"the payment is not of the open item's legal entity, currency or customer")
			}
		}
		newOpen := item.OpenAmount.Sub(in.Amount)
		if _, err := tx.Exec(ctx, `update open_item set open_amount = $3::numeric, status = $4, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, item.ID, newOpen.String(), domain.ApplyStatus(newOpen)); err != nil {
			return err
		}
		in.CustomerID, in.Currency = item.CustomerID, item.Currency
		out, err = insertDeduction(ctx, tx, p.TenantID, in, key)
		return err
	})
	return out, mapWriteErr(err)
}

// insertDeduction writes an OPEN deduction and enqueues deduction.opened.v1.
func insertDeduction(ctx context.Context, tx pgx.Tx, tenantID string, in Deduction, key string) (Deduction, error) {
	id := ids.New()
	if _, err := tx.Exec(ctx, `insert into deduction (tenant_id, id, legal_entity_id, customer_id, open_item_id, payment_id, parent_id,
		deduction_type, reason_code, currency, amount, status, idempotency_key)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::numeric, 'OPEN', $12)`,
		tenantID, id, in.LegalEntityID, in.CustomerID, in.OpenItemID, in.PaymentID, in.ParentID, in.Type, in.ReasonCode,
		in.Currency, in.Amount.String(), key); err != nil {
		return Deduction{}, err
	}
	out, err := readDeduction(ctx, tx, `d.id = $1`, id)
	if err != nil {
		return Deduction{}, err
	}
	return out, enqueue(ctx, tx, "deduction", out.ID, rcvevents.DeductionOpened, tenantID, rcvevents.DeductionOpenedData{
		DeductionID: out.ID, LegalEntityID: out.LegalEntityID, CustomerID: out.CustomerID, OpenItemID: out.OpenItemID,
		PaymentID: out.PaymentID, ParentID: out.ParentID, Type: out.Type, ReasonCode: out.ReasonCode, Amount: out.Amount,
		Currency: out.Currency,
	})
}

// ListDeductions reads deductions, open and matched ones first and oldest first
// within them (deduction aging), bounded by limit.
func (s *Postgres) ListDeductions(ctx context.Context, f DeductionFilter, limit int) ([]Deduction, error) {
	out := []Deduction{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+deductionColumns+deductionFrom+`
			where ($1::text = '' or d.status = $1::text) and ($2::text = '' or d.deduction_type = $2::text)
			and ($3::text = '' or d.customer_id::text = $3::text)
			order by d.status not in ('OPEN', 'MATCHED'), d.created_at, d.id limit $4`, f.Status, f.Type, f.CustomerID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDeduction(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// GetDeduction reads one deduction with the children of a split.
func (s *Postgres) GetDeduction(ctx context.Context, id string) (Deduction, error) {
	var out Deduction
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadDeduction(ctx, tx, id)
		return err
	})
	return out, err
}

func loadDeduction(ctx context.Context, tx pgx.Tx, id string) (Deduction, error) {
	d, err := readDeduction(ctx, tx, `d.id = $1`, id)
	if err != nil {
		return Deduction{}, notFound(err, CodeDeductionNotFound, "deduction not found")
	}
	if d.Status != domain.DeductionSplit {
		return d, nil
	}
	rows, err := tx.Query(ctx, `select `+deductionColumns+deductionFrom+` where d.parent_id = $1 order by d.created_at, d.id`, id)
	if err != nil {
		return Deduction{}, err
	}
	defer rows.Close()
	d.Children = []Deduction{}
	for rows.Next() {
		c, err := scanDeduction(rows)
		if err != nil {
			return Deduction{}, err
		}
		d.Children = append(d.Children, c)
	}
	return d, rows.Err()
}

// lockDeduction reads a deduction under a row lock, checks the If-Match version
// and that action is allowed from its state.
func lockDeduction(ctx context.Context, tx pgx.Tx, id string, version int, action string) (Deduction, error) {
	cur, err := readDeduction(ctx, tx, `d.id = $1 for update of d`, id)
	if err != nil {
		return Deduction{}, notFound(err, CodeDeductionNotFound, "deduction not found")
	}
	if cur.Version != version {
		return Deduction{}, httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "the deduction changed since it was read")
	}
	if err := domain.CheckDeductionMove(cur.Status, action); err != nil {
		return Deduction{}, domainProblemFrom(err)
	}
	return cur, nil
}

// resolvedEvent is the deduction.resolved.v1 payload for d after resolution.
func resolvedEvent(d Deduction, resolution string) rcvevents.DeductionResolvedData {
	return rcvevents.DeductionResolvedData{DeductionID: d.ID, LegalEntityID: d.LegalEntityID, CustomerID: d.CustomerID,
		Resolution: resolution, Amount: d.Amount, Currency: d.Currency, ClaimType: d.ClaimType, ClaimRef: d.ClaimRef,
		DenialReason: d.DenialReason, RebillItemID: d.RebillItemID, ChildIDs: []string{}, Postings: []domain.Posting{}}
}

// MatchDeduction attaches an OPEN deduction to a wave 26 claim. The whole
// deduction must fit the claim's remaining amount, or AR-0014 asks the clerk to
// split it first (BR-30.12). No event: nothing is consumed until approval.
func (s *Postgres) MatchDeduction(ctx context.Context, id string, version int, claim ClaimMatch) (Deduction, error) {
	p, _ := tenant.FromContext(ctx)
	var out Deduction
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockDeduction(ctx, tx, id, version, "match")
		if err != nil {
			return err
		}
		if err := domain.CheckClaimCovers(cur.Amount, claim.Remaining); err != nil {
			return domainProblemFrom(err)
		}
		if _, err := tx.Exec(ctx, `update deduction set status = 'MATCHED', claim_type = $3, claim_ref = $4, claim_remaining = $5::numeric,
			version = version + 1 where tenant_id = $1 and id = $2`, p.TenantID, id, claim.Type, claim.Ref, claim.Remaining.String()); err != nil {
			return err
		}
		out, err = loadDeduction(ctx, tx, id)
		return err
	})
	return out, mapWriteErr(err)
}

// ApproveDeduction accepts a MATCHED deduction: billing issues the credit note
// against the claim, which consumes the fund or accrual (deduction.resolved.v1, APPROVED).
func (s *Postgres) ApproveDeduction(ctx context.Context, id string, version int, note *string) (Deduction, error) {
	return s.resolveDeduction(ctx, id, version, "approve", func(tx pgx.Tx, cur Deduction, tenantID string) (rcvevents.DeductionResolvedData, error) {
		if _, err := tx.Exec(ctx, `update deduction set status = 'APPROVED', decision_note = $3, resolved_at = now(), version = version + 1
			where tenant_id = $1 and id = $2`, tenantID, id, note); err != nil {
			return rcvevents.DeductionResolvedData{}, err
		}
		cur.Status = domain.DeductionApproved
		return resolvedEvent(cur, domain.DeductionApproved), nil
	})
}

// DenyDeduction rejects a deduction with a denial reason and re-bills its
// amount to the customer as a DEDUCTION debit open item, due today.
func (s *Postgres) DenyDeduction(ctx context.Context, id string, version int, reason string, note *string) (Deduction, error) {
	return s.resolveDeduction(ctx, id, version, "deny", func(tx pgx.Tx, cur Deduction, tenantID string) (rcvevents.DeductionResolvedData, error) {
		rebill := ids.New()
		if _, err := tx.Exec(ctx, `insert into open_item (tenant_id, id, legal_entity_id, customer_id, source_type, document_number,
			document_date, due_date, currency, amount, open_amount, status)
			values ($1, $2, $3, $4, 'DEDUCTION', $5, current_date, current_date, $6, $7::numeric, $7::numeric, 'OPEN')`,
			tenantID, rebill, cur.LegalEntityID, cur.CustomerID, "DED-"+cur.ID, cur.Currency, cur.Amount.String()); err != nil {
			return rcvevents.DeductionResolvedData{}, err
		}
		if _, err := tx.Exec(ctx, `update deduction set status = 'DENIED', denial_reason = $3, decision_note = $4, rebill_item_id = $5,
			resolved_at = now(), version = version + 1 where tenant_id = $1 and id = $2`, tenantID, id, reason, note, rebill); err != nil {
			return rcvevents.DeductionResolvedData{}, err
		}
		cur.Status, cur.DenialReason, cur.RebillItemID = domain.DeductionDenied, &reason, &rebill
		return resolvedEvent(cur, domain.DeductionDenied), nil
	})
}

// WriteOffDeduction writes a deduction off to 6110 within deduction_write_off_limit,
// or above it when the caller holds receivables.write-off.approve (mayApprove). The event
// carries Dr 6110 / Cr 1200 until the ledger client posts it.
func (s *Postgres) WriteOffDeduction(ctx context.Context, id string, version int, mayApprove bool, note *string) (Deduction, error) {
	return s.resolveDeduction(ctx, id, version, "write-off", func(tx pgx.Tx, cur Deduction, tenantID string) (rcvevents.DeductionResolvedData, error) {
		if err := domain.CheckDeductionWriteOff(cur.Amount, cur.Currency, mayApprove); err != nil {
			return rcvevents.DeductionResolvedData{}, domainProblemFrom(err)
		}
		if _, err := tx.Exec(ctx, `update deduction set status = 'WRITTEN_OFF', decision_note = $3, resolved_at = now(), version = version + 1
			where tenant_id = $1 and id = $2`, tenantID, id, note); err != nil {
			return rcvevents.DeductionResolvedData{}, err
		}
		cur.Status = domain.DeductionWrittenOff
		ev := resolvedEvent(cur, domain.DeductionWrittenOff)
		ev.Postings = domain.DeductionWriteOffPostings(cur.Amount)
		return ev, nil
	})
}

// SplitDeduction divides an OPEN deduction into OPEN children that sum to it;
// the parent becomes SPLIT. Each child is a new deduction on the same item,
// with its own deduction.opened.v1.
func (s *Postgres) SplitDeduction(ctx context.Context, id string, version int, parts []decimal.Decimal) (Deduction, error) {
	return s.resolveDeduction(ctx, id, version, "split", func(tx pgx.Tx, cur Deduction, tenantID string) (rcvevents.DeductionResolvedData, error) {
		if err := domain.CheckSplit(cur.Amount, parts); err != nil {
			return rcvevents.DeductionResolvedData{}, domainProblemFrom(err)
		}
		if _, err := tx.Exec(ctx, `update deduction set status = 'SPLIT', resolved_at = now(), version = version + 1
			where tenant_id = $1 and id = $2`, tenantID, id); err != nil {
			return rcvevents.DeductionResolvedData{}, err
		}
		ev := resolvedEvent(cur, domain.DeductionSplit)
		for i, amount := range parts {
			child := cur
			child.ParentID, child.Amount = &cur.ID, amount
			saved, err := insertDeduction(ctx, tx, tenantID, child, fmt.Sprintf("split:%s:%d", cur.ID, i))
			if err != nil {
				return rcvevents.DeductionResolvedData{}, err
			}
			ev.ChildIDs = append(ev.ChildIDs, saved.ID)
		}
		return ev, nil
	})
}

// resolveDeduction locks the deduction, applies one move with its event in the
// same transaction and returns the deduction as stored.
func (s *Postgres) resolveDeduction(ctx context.Context, id string, version int, action string,
	move func(tx pgx.Tx, cur Deduction, tenantID string) (rcvevents.DeductionResolvedData, error)) (Deduction, error) {
	p, _ := tenant.FromContext(ctx)
	var out Deduction
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := lockDeduction(ctx, tx, id, version, action)
		if err != nil {
			return err
		}
		ev, err := move(tx, cur, p.TenantID)
		if err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "deduction", id, rcvevents.DeductionResolved, p.TenantID, ev); err != nil {
			return err
		}
		out, err = loadDeduction(ctx, tx, id)
		return err
	})
	return out, mapWriteErr(err)
}
