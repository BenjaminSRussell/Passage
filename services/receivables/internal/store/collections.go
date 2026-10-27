package store

import (
	"context"
	"errors"
	"net/http"
	"strings"
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

const (
	CodeDisputeNotFound = "AR-DISPUTE-NOT-FOUND"
	CodeRunNotFound     = "AR-DUNNING-RUN-NOT-FOUND"
)

// Dispute is a customer dispute on one open item (FR-30.9, §5.30.4 dispute).
type Dispute struct {
	ID             string          `json:"id"`
	LegalEntityID  string          `json:"legalEntityId"`
	CustomerID     string          `json:"customerId"`
	OpenItemID     string          `json:"openItemId"`
	DocumentNumber string          `json:"documentNumber"`
	ReasonCode     string          `json:"reasonCode"`
	DisputedAmount decimal.Decimal `json:"disputedAmount"`
	Currency       string          `json:"currency"`
	Owner          string          `json:"owner"`
	Status         string          `json:"status"`
	ResolutionNote *string         `json:"resolutionNote,omitempty"`
	SLADue         time.Time       `json:"slaDue"`
	CreatedAt      time.Time       `json:"createdAt"`
	ResolvedAt     *time.Time      `json:"resolvedAt,omitempty"`
	Version        int             `json:"version"`
}

// DisputeFilter narrows a dispute list. Active keeps OPEN and UNDER_REVIEW only.
type DisputeFilter struct {
	Status     string
	Active     bool
	CustomerID string
	OpenItemID string
}

// Promise is a promise to pay on one open item (FR-30.9).
type Promise struct {
	ID             string          `json:"id"`
	LegalEntityID  string          `json:"legalEntityId"`
	CustomerID     string          `json:"customerId"`
	OpenItemID     string          `json:"openItemId"`
	DocumentNumber string          `json:"documentNumber"`
	Currency       string          `json:"currency"`
	Amount         decimal.Decimal `json:"amount"`
	PromiseDate    string          `json:"promiseDate"`
	CreatedAt      time.Time       `json:"createdAt"`
}

// PromiseFilter narrows a promise list.
type PromiseFilter struct {
	CustomerID string
	OpenItemID string
}

// The dispute and promise reads join the item for its document number.
const disputeColumns = `d.id, d.legal_entity_id, d.customer_id, d.open_item_id, oi.document_number, d.reason_code, d.disputed_amount::text,
	d.currency, d.owner, d.status, d.resolution_note, d.sla_due, d.created_at, d.resolved_at, d.version`

const disputeFrom = ` from dispute d join open_item oi on oi.tenant_id = d.tenant_id and oi.id = d.open_item_id`

const promiseColumns = `p.id, p.legal_entity_id, p.customer_id, p.open_item_id, oi.document_number, oi.currency, p.amount::text,
	p.promise_date::text, p.created_at`

const promiseFrom = ` from promise_to_pay p join open_item oi on oi.tenant_id = p.tenant_id and oi.id = p.open_item_id`

func scanDispute(row rowScanner) (Dispute, error) {
	var d Dispute
	var amount string
	if err := row.Scan(&d.ID, &d.LegalEntityID, &d.CustomerID, &d.OpenItemID, &d.DocumentNumber, &d.ReasonCode, &amount, &d.Currency,
		&d.Owner, &d.Status, &d.ResolutionNote, &d.SLADue, &d.CreatedAt, &d.ResolvedAt, &d.Version); err != nil {
		return Dispute{}, err
	}
	var err error
	d.DisputedAmount, err = decimal.NewFromString(amount)
	return d, err
}

func scanPromise(row rowScanner) (Promise, error) {
	var p Promise
	var amount string
	if err := row.Scan(&p.ID, &p.LegalEntityID, &p.CustomerID, &p.OpenItemID, &p.DocumentNumber, &p.Currency, &amount,
		&p.PromiseDate, &p.CreatedAt); err != nil {
		return Promise{}, err
	}
	var err error
	p.Amount, err = decimal.NewFromString(amount)
	return p, err
}

func readDispute(ctx context.Context, tx pgx.Tx, where string, args ...any) (Dispute, error) {
	return scanDispute(tx.QueryRow(ctx, `select `+disputeColumns+disputeFrom+` where `+where, args...))
}

// sameDispute compares the fields a client controls, for idempotent replay.
func sameDispute(stored, in Dispute) bool {
	return stored.LegalEntityID == in.LegalEntityID && stored.OpenItemID == in.OpenItemID && stored.DisputedAmount.Equal(in.DisputedAmount) &&
		stored.ReasonCode == in.ReasonCode && stored.Owner == in.Owner
}

// OpenDispute records a dispute on an open item and sets the item's dispute flag,
// which excludes it from dunning and interest (FR-30.9). The item is locked
// (NOWAIT, BR-30.7) so a concurrent settlement or second dispute waits its turn.
// A repeated key returns the stored dispute when the request matches.
func (s *Postgres) OpenDispute(ctx context.Context, in Dispute, key string) (Dispute, error) {
	p, _ := tenant.FromContext(ctx)
	var out Dispute
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay, err := readDispute(ctx, tx, `d.idempotency_key = $1`, key)
		if err == nil {
			if !sameDispute(replay, in) {
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
			return httpx.NewProblem(http.StatusBadRequest, domain.CodeApplyMismatch, "open item "+in.OpenItemID+" belongs to another legal entity")
		}
		if item.Status != "OPEN" && item.Status != "PARTIALLY_SETTLED" {
			return domainProblem(domain.CodeDisputeState, "open item "+item.DocumentNumber+" is "+item.Status+" and cannot be disputed")
		}
		var active bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from dispute where open_item_id = $1 and status in ('OPEN', 'UNDER_REVIEW'))`,
			in.OpenItemID).Scan(&active); err != nil {
			return err
		}
		if active {
			return domainProblem(domain.CodeDisputeExists, "open item "+item.DocumentNumber+" already has an active dispute")
		}
		if e := domain.ValidateAmountWithin(in.DisputedAmount, item.OpenAmount); e != nil {
			return domainProblemFrom(e)
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into dispute (tenant_id, id, legal_entity_id, customer_id, open_item_id, reason_code,
			disputed_amount, currency, owner, status, sla_due, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'OPEN', now() + make_interval(hours => $10), $11)`,
			p.TenantID, id, in.LegalEntityID, item.CustomerID, in.OpenItemID, in.ReasonCode, in.DisputedAmount.String(),
			item.Currency, in.Owner, domain.DisputeSLAHours, key); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update open_item set dispute_id = $3, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, in.OpenItemID, id); err != nil {
			return err
		}
		if out, err = readDispute(ctx, tx, `d.id = $1`, id); err != nil {
			return err
		}
		return enqueue(ctx, tx, "dispute", out.ID, rcvevents.DisputeOpened, p.TenantID, rcvevents.DisputeOpenedData{
			DisputeID: out.ID, LegalEntityID: out.LegalEntityID, CustomerID: out.CustomerID, OpenItemID: out.OpenItemID,
			Amount: out.DisputedAmount, Currency: out.Currency, ReasonCode: out.ReasonCode,
		})
	})
	return out, mapWriteErr(err)
}

// TransitionDispute moves a dispute along BR-30.6. version is the If-Match value.
// A resolution clears the item's dispute flag and enqueues dispute.resolved.v1.
func (s *Postgres) TransitionDispute(ctx context.Context, id string, version int, to string, note *string) (Dispute, error) {
	p, _ := tenant.FromContext(ctx)
	var out Dispute
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := readDispute(ctx, tx, `d.id = $1 for update of d`, id)
		if err != nil {
			return notFound(err, CodeDisputeNotFound, "dispute not found")
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "dispute changed since it was read")
		}
		if !domain.CanMoveDispute(cur.Status, to) {
			return domainProblem(domain.CodeDisputeState, "dispute in status "+cur.Status+" cannot move to "+to)
		}
		if _, err := tx.Exec(ctx, `update dispute set status = $3, resolution_note = coalesce($4, resolution_note),
			resolved_at = case when $3 in ('OPEN', 'UNDER_REVIEW') then null else now() end, version = version + 1
			where tenant_id = $1 and id = $2`, p.TenantID, id, to, note); err != nil {
			return err
		}
		if out, err = readDispute(ctx, tx, `d.id = $1`, id); err != nil {
			return err
		}
		if domain.DisputeActive(to) {
			return nil
		}
		if _, err := tx.Exec(ctx, `update open_item set dispute_id = null, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 and dispute_id = $3`, p.TenantID, out.OpenItemID, out.ID); err != nil {
			return err
		}
		return enqueue(ctx, tx, "dispute", out.ID, rcvevents.DisputeResolved, p.TenantID, rcvevents.DisputeResolvedData{
			DisputeID: out.ID, LegalEntityID: out.LegalEntityID, CustomerID: out.CustomerID, OpenItemID: out.OpenItemID,
			Amount: out.DisputedAmount, Currency: out.Currency, ReasonCode: out.ReasonCode,
			Resolution: strings.TrimPrefix(to, "RESOLVED_"),
		})
	})
	return out, mapWriteErr(err)
}

// GetDispute reads one dispute.
func (s *Postgres) GetDispute(ctx context.Context, id string) (Dispute, error) {
	var out Dispute
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = readDispute(ctx, tx, `d.id = $1`, id)
		return notFound(err, CodeDisputeNotFound, "dispute not found")
	})
	return out, err
}

// ListDisputes reads disputes, nearest SLA first, bounded by limit.
func (s *Postgres) ListDisputes(ctx context.Context, f DisputeFilter, limit int) ([]Dispute, error) {
	out := []Dispute{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+disputeColumns+disputeFrom+`
			where ($1::text = '' or d.status = $1::text) and (not $2::bool or d.status in ('OPEN', 'UNDER_REVIEW'))
			and ($3::text = '' or d.customer_id::text = $3::text) and ($4::text = '' or d.open_item_id::text = $4::text)
			order by d.sla_due, d.id limit $5`, f.Status, f.Active, f.CustomerID, f.OpenItemID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDispute(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// CreatePromise records a promise to pay on an open item. A promise that still
// holds blocks a second one on the same item; a broken promise may be replaced.
// A repeated key returns the stored promise when the request matches.
func (s *Postgres) CreatePromise(ctx context.Context, in Promise, key string) (Promise, error) {
	p, _ := tenant.FromContext(ctx)
	var out Promise
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay, err := scanPromise(tx.QueryRow(ctx, `select `+promiseColumns+promiseFrom+` where p.idempotency_key = $1`, key))
		if err == nil {
			if replay.LegalEntityID != in.LegalEntityID || replay.OpenItemID != in.OpenItemID || !replay.Amount.Equal(in.Amount) ||
				replay.PromiseDate != in.PromiseDate {
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
			return httpx.NewProblem(http.StatusBadRequest, domain.CodeApplyMismatch, "open item "+in.OpenItemID+" belongs to another legal entity")
		}
		if item.Status != "OPEN" && item.Status != "PARTIALLY_SETTLED" {
			return domainProblem(domain.CodePromiseState, "open item "+item.DocumentNumber+" is "+item.Status+" and needs no promise")
		}
		if e := domain.ValidateAmountWithin(in.Amount, item.OpenAmount); e != nil {
			return domainProblemFrom(e)
		}
		var holding bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from promise_to_pay where open_item_id = $1
			and promise_date + $2::int >= current_date)`, in.OpenItemID, domain.PromiseGraceDays).Scan(&holding); err != nil {
			return err
		}
		if holding {
			return domainProblem(domain.CodePromiseExists, "open item "+item.DocumentNumber+" already has a promise that has not yet broken")
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into promise_to_pay (tenant_id, id, legal_entity_id, customer_id, open_item_id,
			amount, promise_date, idempotency_key) values ($1, $2, $3, $4, $5, $6, $7, $8)`,
			p.TenantID, id, in.LegalEntityID, item.CustomerID, in.OpenItemID, in.Amount.String(), in.PromiseDate, key); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update open_item set promise_id = $3, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, in.OpenItemID, id); err != nil {
			return err
		}
		out, err = scanPromise(tx.QueryRow(ctx, `select `+promiseColumns+promiseFrom+` where p.id = $1`, id))
		return err
	})
	return out, mapWriteErr(err)
}

// ListPromises reads promises to pay, latest promise date first, bounded by limit.
func (s *Postgres) ListPromises(ctx context.Context, f PromiseFilter, limit int) ([]Promise, error) {
	out := []Promise{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+promiseColumns+promiseFrom+`
			where ($1::text = '' or p.customer_id::text = $1::text) and ($2::text = '' or p.open_item_id::text = $2::text)
			order by p.promise_date desc, p.created_at desc, p.id limit $3`, f.CustomerID, f.OpenItemID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			pr, err := scanPromise(rows)
			if err != nil {
				return err
			}
			out = append(out, pr)
		}
		return rows.Err()
	})
	return out, err
}
