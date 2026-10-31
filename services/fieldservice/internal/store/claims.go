package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	svcevents "github.com/BenjaminSRussell/passage/services/fieldservice/internal/events"
)

// Problem codes of supplier warranty claims beside the BR-45.12 rule codes in the
// domain package. Each is listed in the service README.
const (
	CodeClaimUnknown = "SVC-SWC-007"
	CodeClaimSource  = "SVC-SWC-008"
	CodeClaimClaimed = "SVC-SWC-009"
)

// SupplierClaim is a claim against a vendor for failed parts and labor (FR-45.15).
type SupplierClaim struct {
	ID              string             `json:"id"`
	Number          string             `json:"number"`
	VendorPartyID   string             `json:"vendorPartyId"`
	LegalEntityID   string             `json:"legalEntityId"`
	SourceKind      string             `json:"sourceKind"`
	SourceIDs       []string           `json:"sourceIds"`
	Currency        string             `json:"currency"`
	LaborRate       decimal.Decimal    `json:"laborRate"`
	Lines           []domain.ClaimLine `json:"lines"`
	ClaimedTotal    decimal.Decimal    `json:"claimedTotal"`
	AcceptedTotal   decimal.Decimal    `json:"acceptedTotal"`
	RecoveredAmount decimal.Decimal    `json:"recoveredAmount"`
	Status          string             `json:"status"`
	CompletedOn     string             `json:"completedOn"`
	WindowDays      int                `json:"windowDays"`
	// WindowEndsOn is the last day the claim can be submitted (BR-45.12).
	WindowEndsOn    string     `json:"windowEndsOn"`
	ReturnRequired  bool       `json:"returnRequired"`
	ReturnOrderID   string     `json:"returnOrderId"`
	SubmittedOn     *string    `json:"submittedOn,omitempty"`
	VendorReference string     `json:"vendorReference"`
	DecisionReason  string     `json:"decisionReason"`
	DecidedAt       *time.Time `json:"decidedAt,omitempty"`
	CreditNoteID    string     `json:"creditNoteId"`
	SettledAt       *time.Time `json:"settledAt,omitempty"`
	ClosedAt        *time.Time `json:"closedAt,omitempty"`
	Version         int        `json:"version"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

// ClaimInput is a claim to create. A SERVICE_WO claim names posted work orders and
// takes its legal entity, currency, completion date and lines from them; an EAM_WO
// claim (a stand-in for the wave-44 eam.warranty.claimable.v1 consumer) carries them.
type ClaimInput struct {
	VendorPartyID  string
	SourceKind     string
	SourceIDs      []string
	LaborRate      decimal.Decimal
	FailureCode    string
	WindowDays     int
	ReturnRequired bool
	// EAM_WO only.
	LegalEntityID string
	Currency      string
	CompletedOn   string
	Lines         []domain.ClaimLine
	At            time.Time
}

// ClaimMove is a submit, decision, settle or close request on one claim at a version.
type ClaimMove struct {
	ID              string
	Version         int
	VendorReference string
	ReturnOrderID   string
	Decision        string
	Reason          string
	Accepted        []decimal.Decimal
	CreditNoteID    string
	CreditAmount    decimal.Decimal
	CreditCurrency  string
	At              time.Time
}

// ClaimFilter narrows the claim list.
type ClaimFilter struct {
	VendorPartyID string
	Status        string
	Cursor        string
	Limit         int
}

const claimColumns = `c.id::text, c.number, c.vendor_party_id::text, c.legal_entity_id::text, c.source_kind, c.source_ids, c.currency,
	c.labor_rate::text, c.lines, c.claimed_total::text, c.accepted_total::text, c.recovered_amount::text, c.status, c.completed_on::text,
	c.window_days, c.return_required, c.return_order_id, c.submitted_on::text, c.vendor_reference, c.decision_reason, c.decided_at,
	c.credit_note_id, c.settled_at, c.closed_at, c.version, c.created_at, c.updated_at`

func scanClaim(r pgx.Row) (SupplierClaim, error) {
	var c SupplierClaim
	var sources, lines []byte
	var rate, claimed, accepted, recovered string
	if err := r.Scan(&c.ID, &c.Number, &c.VendorPartyID, &c.LegalEntityID, &c.SourceKind, &sources, &c.Currency, &rate, &lines,
		&claimed, &accepted, &recovered, &c.Status, &c.CompletedOn, &c.WindowDays, &c.ReturnRequired, &c.ReturnOrderID,
		&c.SubmittedOn, &c.VendorReference, &c.DecisionReason, &c.DecidedAt, &c.CreditNoteID, &c.SettledAt, &c.ClosedAt,
		&c.Version, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return c, err
	}
	if err := json.Unmarshal(sources, &c.SourceIDs); err != nil {
		return c, err
	}
	if err := json.Unmarshal(lines, &c.Lines); err != nil {
		return c, err
	}
	var err error
	for _, f := range []struct {
		dst *decimal.Decimal
		raw string
	}{{&c.LaborRate, rate}, {&c.ClaimedTotal, claimed}, {&c.AcceptedTotal, accepted}, {&c.RecoveredAmount, recovered}} {
		if *f.dst, err = decimal.NewFromString(f.raw); err != nil {
			return c, err
		}
	}
	if day, err := time.Parse(dateLayout, c.CompletedOn); err == nil {
		c.WindowEndsOn = domain.WindowEnds(day, c.WindowDays).Format(dateLayout)
	}
	return c, nil
}

func getClaim(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (SupplierClaim, error) {
	q := `select ` + claimColumns + ` from service_supplier_claim c where c.tenant_id = $1 and c.id = $2`
	if lock {
		q += ` for update`
	}
	c, err := scanClaim(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, httpx.NewProblem(http.StatusNotFound, CodeClaimUnknown, "supplier claim not found")
	}
	return c, err
}

// claimSource is a posted work order's share of a claim.
type claimSource struct {
	wo          WorkOrder
	itemID      string
	serial      string
	partCost    decimal.Decimal
	laborHours  decimal.Decimal
	completedOn time.Time
}

// serviceSource loads a POSTED work order with WARRANTY lines and the item and
// serial of its case's service object (FR-45.15 "from WARRANTY-covered service work
// order lines").
func serviceSource(ctx context.Context, tx pgx.Tx, tenantID, id string) (claimSource, error) {
	wo, err := getWorkOrder(ctx, tx, tenantID, id)
	var p *httpx.Problem
	if errors.As(err, &p) && p.Status == http.StatusNotFound {
		return claimSource{}, unprocessable(CodeClaimSource, "work order "+id+" was not found")
	}
	if err != nil {
		return claimSource{}, err
	}
	if wo.Status != string(domain.WOPosted) || wo.Posting == nil || wo.CompletedAt == nil {
		return claimSource{}, unprocessable(CodeClaimSource, "work order "+wo.Number+" is not POSTED; claims are made from posted warranty lines")
	}
	part, hours, ok := domain.WarrantyShare(*wo.Posting)
	if !ok {
		return claimSource{}, unprocessable(CodeClaimSource, "work order "+wo.Number+" has no WARRANTY part or labor line")
	}
	src := claimSource{wo: wo, partCost: part, laborHours: hours, completedOn: wo.CompletedAt.UTC().Truncate(24 * time.Hour)}
	if wo.CaseID != nil {
		err := tx.QueryRow(ctx, `select so.item_id::text, so.serial from service_case c
			join service_object so on so.tenant_id = c.tenant_id and so.id = c.service_object_id
			where c.tenant_id = $1 and c.id = $2`, tenantID, *wo.CaseID).Scan(&src.itemID, &src.serial)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return claimSource{}, err
		}
	}
	return src, nil
}

// CreateClaim drafts a supplier warranty claim (FR-45.15, BR-45.12). Each line
// claims part + labor hours × the vendor's labor rate. A source work order is
// claimed once (SVC-SWC-009).
func (s *Postgres) CreateClaim(ctx context.Context, in ClaimInput, key, requestHash string) (SupplierClaim, bool, error) {
	tid := tenantOf(ctx)
	var out SupplierClaim
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		id, found, err := replayOf(ctx, tx, "service_supplier_claim", tid, key, requestHash)
		if err != nil {
			return err
		}
		if found {
			replayed = true
			out, err = getClaim(ctx, tx, tid, id, false)
			return err
		}
		le, currency, completed, lines := in.LegalEntityID, in.Currency, in.CompletedOn, in.Lines
		if in.SourceKind == domain.ClaimFromServiceWO {
			lines = make([]domain.ClaimLine, 0, len(in.SourceIDs))
			var earliest time.Time
			for i, sid := range in.SourceIDs {
				src, err := serviceSource(ctx, tx, tid, sid)
				if err != nil {
					return err
				}
				if i == 0 {
					le, currency, earliest = src.wo.LegalEntityID, src.wo.Currency, src.completedOn
				} else if src.wo.LegalEntityID != le || src.wo.Currency != currency {
					return unprocessable(CodeClaimSource, "the work orders of one claim share a legal entity and currency")
				}
				if src.completedOn.Before(earliest) {
					earliest = src.completedOn
				}
				lines = append(lines, domain.ClaimLine{SourceID: sid, ItemID: src.itemID, Serial: src.serial, FailureCode: in.FailureCode,
					PartAmount: src.partCost, LaborHours: src.laborHours})
			}
			completed = earliest.Format(dateLayout)
		}
		for i := range lines {
			lines[i].LaborRate = in.LaborRate
			lines[i].ClaimedAmount = domain.ClaimableAmount(lines[i].PartAmount, lines[i].LaborHours, in.LaborRate, currency)
			lines[i].AcceptedAmount = decimal.Zero
		}
		claimed, _ := domain.ClaimTotals(lines)
		if !claimed.IsPositive() {
			return unprocessable(CodeClaimSource, "the claim has nothing to claim: no part cost and no covered labor")
		}
		sources, err := json.Marshal(in.SourceIDs)
		if err != nil {
			return err
		}
		rawLines, err := json.Marshal(lines)
		if err != nil {
			return err
		}
		number, err := nextNumber(ctx, tx, tid, "supplierclaim", "SWC")
		if err != nil {
			return err
		}
		id = ids.New()
		if _, err := tx.Exec(ctx, `insert into service_supplier_claim (tenant_id, id, number, vendor_party_id, legal_entity_id, source_kind,
			source_ids, currency, labor_rate, lines, claimed_total, completed_on, window_days, return_required, idempotency_key, request_hash,
			created_at, updated_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10, $11::numeric, $12::date, $13, $14, $15, $16, $17, $17)`,
			tid, id, number, in.VendorPartyID, le, in.SourceKind, sources, currency, in.LaborRate.String(), rawLines, claimed.String(),
			completed, in.WindowDays, in.ReturnRequired, nullable(key), nullable(requestHash), in.At); err != nil {
			return err
		}
		for _, sid := range in.SourceIDs {
			tag, err := tx.Exec(ctx, `insert into service_supplier_claim_source (tenant_id, source_kind, source_id, claim_id)
				values ($1, $2, $3, $4) on conflict do nothing`, tid, in.SourceKind, sid, id)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return httpx.NewProblem(http.StatusConflict, CodeClaimClaimed, "work order "+sid+" is already on a supplier claim")
			}
		}
		out, err = getClaim(ctx, tx, tid, id, false)
		return err
	})
	return out, replayed, err
}

// GetClaim loads one claim inside the caller's legal-entity scope.
func (s *Postgres) GetClaim(ctx context.Context, id string) (SupplierClaim, error) {
	tid := tenantOf(ctx)
	var c SupplierClaim
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		c, err = getClaim(ctx, tx, tid, id, false)
		return err
	})
	return c, err
}

// ListClaims returns claims newest first, by vendor and status.
func (s *Postgres) ListClaims(ctx context.Context, f ClaimFilter) (Page[SupplierClaim], error) {
	return listPage(ctx, s, f.Limit, func(c SupplierClaim) string { return c.ID }, scanClaim,
		`select `+claimColumns+` from service_supplier_claim c
		where c.tenant_id = $1 and ($2 = '' or c.vendor_party_id::text = $2) and ($3 = '' or c.status = $3)
			and `+keyset("c", "service_supplier_claim", 4)+`
		order by c.created_at desc, c.id desc limit $5`,
		tenantOf(ctx), f.VendorPartyID, f.Status, f.Cursor)
}

// moveClaim locks the claim, answers a replay of the same Idempotency-Key with the
// stored answer, checks the version and runs apply, storing the answer under the key.
func (s *Postgres) moveClaim(ctx context.Context, m ClaimMove, key, requestHash string,
	apply func(tx pgx.Tx, c SupplierClaim) error) (SupplierClaim, bool, error) {
	tid := tenantOf(ctx)
	var out SupplierClaim
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := getClaim(ctx, tx, tid, m.ID, true)
		if err != nil {
			return err
		}
		if replayed, err = keyedAnswer(ctx, tx, tid, key, requestHash, &out); err != nil || replayed {
			return err
		}
		if c.Version != m.Version {
			return versionConflict(c.Version)
		}
		if err := apply(tx, c); err != nil {
			return err
		}
		if out, err = getClaim(ctx, tx, tid, m.ID, false); err != nil {
			return err
		}
		return storeAnswer(ctx, tx, tid, key, requestHash, out)
	})
	return out, replayed, err
}

// SubmitClaim sends a DRAFT claim to the vendor on the day of m.At, inside the
// vendor's window (SVC-SWC-001) and with the failed-part return recorded when the
// vendor requires one (SVC-SWC-002).
func (s *Postgres) SubmitClaim(ctx context.Context, m ClaimMove, key, requestHash string) (SupplierClaim, bool, error) {
	tid := tenantOf(ctx)
	return s.moveClaim(ctx, m, key, requestHash, func(tx pgx.Tx, c SupplierClaim) error {
		completed, err := time.Parse(dateLayout, c.CompletedOn)
		if err != nil {
			return err
		}
		ret := c.ReturnOrderID
		if m.ReturnOrderID != "" {
			ret = m.ReturnOrderID
		}
		ref := c.VendorReference
		if m.VendorReference != "" {
			ref = m.VendorReference
		}
		if err := domain.CheckSubmit(c.Status, completed, m.At.UTC(), c.WindowDays, c.ReturnRequired, ret); err != nil {
			return ruleProblem(err)
		}
		_, err = tx.Exec(ctx, `update service_supplier_claim set status = 'SUBMITTED', submitted_on = $3::date, return_order_id = $4,
			vendor_reference = $5, version = version + 1, updated_at = $6 where tenant_id = $1 and id = $2`,
			tid, c.ID, m.At.UTC().Format(dateLayout), ret, ref, m.At)
		return err
	})
}

// DecideClaim records the vendor decision with the accepted amount per line
// (BR-45.12); the vendor reference may arrive with it.
func (s *Postgres) DecideClaim(ctx context.Context, m ClaimMove, key, requestHash string) (SupplierClaim, bool, error) {
	tid := tenantOf(ctx)
	return s.moveClaim(ctx, m, key, requestHash, func(tx pgx.Tx, c SupplierClaim) error {
		lines, err := domain.Decide(c.Status, m.Decision, m.Reason, c.Currency, c.Lines, m.Accepted)
		if err != nil {
			return ruleProblem(err)
		}
		_, accepted := domain.ClaimTotals(lines)
		raw, err := json.Marshal(lines)
		if err != nil {
			return err
		}
		ref := c.VendorReference
		if m.VendorReference != "" {
			ref = m.VendorReference
		}
		_, err = tx.Exec(ctx, `update service_supplier_claim set status = $3, lines = $4, accepted_total = $5::numeric, decision_reason = $6,
			vendor_reference = $7, decided_at = $8, version = version + 1, updated_at = $8 where tenant_id = $1 and id = $2`,
			tid, c.ID, m.Decision, raw, accepted.String(), m.Reason, ref, m.At)
		return err
	})
}

// SettleClaim matches the vendor's credit note (wave 18) to an accepted claim by
// its vendor reference; the claim becomes SETTLED with the credit as the recovered
// amount, and dev.passage.service.supplierclaim.settled.v1 is written to the outbox
// in the same transaction for the GL warranty recovery (BR-45.12, AC-45.13).
func (s *Postgres) SettleClaim(ctx context.Context, m ClaimMove, key, requestHash string) (SupplierClaim, bool, error) {
	tid := tenantOf(ctx)
	return s.moveClaim(ctx, m, key, requestHash, func(tx pgx.Tx, c SupplierClaim) error {
		recovered, err := domain.CheckSettle(c.Status, c.VendorReference, c.Currency, c.AcceptedTotal, m.VendorReference, m.CreditCurrency, m.CreditAmount)
		if err != nil {
			return ruleProblem(err)
		}
		if _, err := tx.Exec(ctx, `update service_supplier_claim set status = 'SETTLED', recovered_amount = $3::numeric, credit_note_id = $4,
			settled_at = $5, version = version + 1, updated_at = $5 where tenant_id = $1 and id = $2`,
			tid, c.ID, recovered.String(), m.CreditNoteID, m.At); err != nil {
			return err
		}
		return enqueue(ctx, tx, "supplier_claim", c.ID, svcevents.SupplierClaimSettled, tid, svcevents.SupplierClaimSettledData{
			ClaimID: c.ID, Number: c.Number, VendorID: c.VendorPartyID, LegalEntityID: c.LegalEntityID, Currency: c.Currency,
			ClaimedAmount: c.ClaimedTotal.String(), RecoveredAmount: recovered.String(), CreditNoteID: m.CreditNoteID,
			VendorReference: c.VendorReference, SettledAt: m.At.UTC().Format(time.RFC3339),
		})
	})
}

// CloseClaim closes a REJECTED (with its reason, no posting) or SETTLED claim.
func (s *Postgres) CloseClaim(ctx context.Context, m ClaimMove, key, requestHash string) (SupplierClaim, bool, error) {
	tid := tenantOf(ctx)
	return s.moveClaim(ctx, m, key, requestHash, func(tx pgx.Tx, c SupplierClaim) error {
		if err := domain.CheckCloseClaim(c.Status); err != nil {
			return ruleProblem(err)
		}
		_, err := tx.Exec(ctx, `update service_supplier_claim set status = 'CLOSED', closed_at = $3, version = version + 1, updated_at = $3
			where tenant_id = $1 and id = $2`, tid, c.ID, m.At)
		return err
	})
}
