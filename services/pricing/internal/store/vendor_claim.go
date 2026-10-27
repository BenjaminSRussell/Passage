package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	pevents "github.com/BenjaminSRussell/passage/services/pricing/internal/events"
)

// Problem codes of vendor rebate claims (FR-26.15).
const (
	// CodeClaimAlreadyProcessed: the period's accruals are fully claimed (message catalog PRC-0015).
	CodeClaimAlreadyProcessed = "PRC-0015"
	// CodeNothingToClaim: the period has no accrual to claim.
	CodeNothingToClaim = "PRC-0043"
	// CodeClaimNotFound: no vendor rebate claim with the id.
	CodeClaimNotFound = "PRC-0044"
	// CodeClaimFourEyes: the user who created a claim cannot approve it.
	CodeClaimFourEyes = "PRC-0046"
)

// VendorClaim is a period claim on a VENDOR rebate agreement (FR-26.15, BR-26.11).
type VendorClaim struct {
	ID             string          `json:"id"`
	Number         string          `json:"number"`
	LegalEntityID  string          `json:"legalEntityId"`
	AgreementID    string          `json:"agreementId"`
	VendorID       string          `json:"vendorId"`
	PeriodKey      string          `json:"periodKey"`
	AccruedTotal   decimal.Decimal `json:"accruedTotal"`
	PriorClaimed   decimal.Decimal `json:"priorClaimed"`
	Amount         decimal.Decimal `json:"amount"`
	Currency       string          `json:"currency"`
	SettlementMode string          `json:"settlementMode"`
	Status         string          `json:"status"`
	Version        int             `json:"version"`
	CreatedBy      string          `json:"createdBy"`
	ApprovedBy     string          `json:"approvedBy"`
	CreatedAt      time.Time       `json:"createdAt"`
	ApprovedAt     *time.Time      `json:"approvedAt"`
}

// VendorClaimPreview is what a claim for a period would be, before it is created
// (AC-26.11: the period claim preview).
type VendorClaimPreview struct {
	AgreementID    string
	VendorID       string
	PeriodKey      string
	Accrued        decimal.Decimal
	PriorClaimed   decimal.Decimal
	Amount         decimal.Decimal
	Currency       string
	SettlementMode string
	Claims         int
}

// VendorClaimInput asks for the claim of one agreement period. SettlementMode is
// CREDIT_NOTE (default) or PAYMENT_DEDUCTION; a negative claim settles by VENDOR_INVOICE.
type VendorClaimInput struct {
	AgreementID    string
	PeriodKey      string
	SettlementMode string
}

// ClaimFilter selects vendor claims (the Vendor rebate claims inquiry, §5.26.14).
type ClaimFilter struct {
	AgreementID string
	VendorID    string
	PeriodKey   string
	Status      string
	Limit       int
}

const (
	claimFields = `id::text, number, legal_entity_id::text, agreement_id::text, vendor_id::text, period_key,
	accrued_total::text, prior_claimed::text, amount::text, currency::text, settlement_mode, status, version, created_by,
	coalesce(approved_by, ''), created_at, approved_at`
	claimColumns = `select ` + claimFields + ` from vendor_rebate_claim `
)

func scanClaim(row pgx.Row) (VendorClaim, error) {
	var c VendorClaim
	var accrued, prior, amount string
	if err := row.Scan(&c.ID, &c.Number, &c.LegalEntityID, &c.AgreementID, &c.VendorID, &c.PeriodKey, &accrued, &prior, &amount,
		&c.Currency, &c.SettlementMode, &c.Status, &c.Version, &c.CreatedBy, &c.ApprovedBy, &c.CreatedAt, &c.ApprovedAt); err != nil {
		return c, err
	}
	c.AccruedTotal, c.PriorClaimed, c.Amount = decimal.RequireFromString(accrued), decimal.RequireFromString(prior), decimal.RequireFromString(amount)
	return c, nil
}

func claimNotFound(id string) error {
	return httpx.NewProblem(http.StatusNotFound, CodeClaimNotFound, "no vendor rebate claim "+id)
}

// vendorAgreement loads an agreement and requires it to be a VENDOR agreement (PRC-0045).
func vendorAgreement(ctx context.Context, tx pgx.Tx, id string) (domain.RebateAgreement, error) {
	a, err := loadAgreement(ctx, tx, `where id = $1`, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, agreementNotFound(id)
	}
	if err != nil {
		return a, err
	}
	if a.PartyRole != domain.PartyVendor {
		return a, httpx.NewProblem(http.StatusUnprocessableEntity, CodePartyMismatch,
			"agreement "+id+" is a "+a.PartyRole+" rebate agreement; claims apply to VENDOR agreements only")
	}
	return a, nil
}

// periodPosition reads a period's accrued total (lock=true takes the period row lock that
// accruals take, so a claim and an accrual of one period serialize) and what the period's
// claims already claimed. found is false when nothing was ever accrued in the period.
func periodPosition(ctx context.Context, tx pgx.Tx, agreementID, periodKey string, lock bool) (accrued, claimed decimal.Decimal, claims int, found bool, err error) {
	q := `select accrued_total::text from rebate_period where agreement_id = $1 and period_key = $2`
	if lock {
		q += ` for update`
	}
	var total string
	err = tx.QueryRow(ctx, q, agreementID, periodKey).Scan(&total)
	if errors.Is(err, pgx.ErrNoRows) {
		return decimal.Zero, decimal.Zero, 0, false, nil
	}
	if err != nil {
		return
	}
	var sum string
	if err = tx.QueryRow(ctx, `select coalesce(sum(amount), 0)::text, count(*)::int from vendor_rebate_claim
		where agreement_id = $1 and period_key = $2`, agreementID, periodKey).Scan(&sum, &claims); err != nil {
		return
	}
	return decimal.RequireFromString(total), decimal.RequireFromString(sum), claims, true, nil
}

// PreviewVendorClaim computes the claim a period would produce without writing anything.
func (s *Postgres) PreviewVendorClaim(ctx context.Context, agreementID, periodKey string) (VendorClaimPreview, error) {
	var out VendorClaimPreview
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		a, err := vendorAgreement(ctx, tx, agreementID)
		if err != nil {
			return err
		}
		accrued, claimed, claims, _, err := periodPosition(ctx, tx, agreementID, periodKey, false)
		if err != nil {
			return err
		}
		amount := domain.ClaimAmount(accrued, claimed)
		out = VendorClaimPreview{AgreementID: a.ID, VendorID: a.VendorID, PeriodKey: periodKey, Accrued: accrued, PriorClaimed: claimed,
			Amount: amount, Currency: a.Currency, SettlementMode: domain.ClaimSettlement(amount, ""), Claims: claims}
		return nil
	})
	return out, err
}

// CreateVendorClaim creates the claim for one agreement period (FR-26.15, BR-26.11): the
// period's accruals less its earlier claims, numbered RC-n per legal entity, OPEN, with
// vendorrebateclaim.created in the same transaction. A period with nothing accrued is
// PRC-0043; a period whose accruals are fully claimed is 409 PRC-0015. A repeated
// Idempotency-Key replays the first response.
func (s *Postgres) CreateVendorClaim(ctx context.Context, in VendorClaimInput, idempotencyKey string) (VendorClaim, error) {
	p, _ := tenant.FromContext(ctx)
	var out VendorClaim
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		a, err := vendorAgreement(ctx, tx, in.AgreementID)
		if err != nil {
			return err
		}
		accrued, claimed, claims, found, err := periodPosition(ctx, tx, a.ID, in.PeriodKey, true)
		if err != nil {
			return err
		}
		// Checked after the period lock, so a retry waits for the first call and replays it.
		hash := commandHash("vendor-claim", a.ID, in.PeriodKey, in.SettlementMode)
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		amount := domain.ClaimAmount(accrued, claimed)
		if !found || (amount.IsZero() && claims == 0) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeNothingToClaim, "agreement "+a.ID+" has nothing accrued in period "+in.PeriodKey)
		}
		if amount.IsZero() {
			return httpx.NewProblem(http.StatusConflict, CodeClaimAlreadyProcessed,
				"vendor rebate claim for "+a.ID+" "+in.PeriodKey+" already processed: its accruals are fully claimed")
		}
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "rc|"+p.TenantID+"|"+a.LegalEntityID); err != nil {
			return err
		}
		var next int64
		if err := tx.QueryRow(ctx, `select coalesce(max(substr(number, 4)::bigint), 0) + 1 from vendor_rebate_claim where legal_entity_id = $1`,
			a.LegalEntityID).Scan(&next); err != nil {
			return err
		}
		out, err = scanClaim(tx.QueryRow(ctx, `insert into vendor_rebate_claim (tenant_id, id, legal_entity_id, agreement_id, vendor_id,
			period_key, number, accrued_total, prior_claimed, amount, currency, settlement_mode, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9::numeric, $10::numeric, $11, $12, $13)
			returning `+claimFields,
			p.TenantID, ids.New(), a.LegalEntityID, a.ID, a.VendorID, in.PeriodKey, fmt.Sprintf("RC-%06d", next), accrued.String(),
			claimed.String(), amount.String(), a.Currency, domain.ClaimSettlement(amount, in.SettlementMode), p.UserID))
		if err != nil {
			return err
		}
		if err := enqueueClaim(ctx, tx, out, pevents.VendorRebateClaimCreated); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// ApproveVendorClaim moves an OPEN claim to APPROVED at the body version (412 otherwise)
// and writes vendorrebateclaim.approved, which w19 AP consumes. The user who created the
// claim cannot approve it (PRC-0046, VendorRebateClaimApprove vs RebateMaintain, §5.26.17).
func (s *Postgres) ApproveVendorClaim(ctx context.Context, id string, version int, idempotencyKey string) (VendorClaim, error) {
	p, _ := tenant.FromContext(ctx)
	var out VendorClaim
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanClaim(tx.QueryRow(ctx, claimColumns+`where id = $1 for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return claimNotFound(id)
		}
		if err != nil {
			return err
		}
		hash := commandHash("approve-claim", id, strconv.Itoa(version))
		if ok, err := replayJSON(ctx, tx, p.TenantID, idempotencyKey, hash, &out); err != nil || ok {
			return err
		}
		if cur.Version != version {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, "claim version does not match")
		}
		if cur.Status != domain.ClaimOpen {
			return httpx.NewProblem(http.StatusConflict, CodeStateInvalid, "only OPEN claims can be approved")
		}
		if p.UserID != "" && p.UserID == cur.CreatedBy {
			return httpx.NewProblem(http.StatusForbidden, CodeClaimFourEyes, "the user who created claim "+cur.Number+" cannot approve it")
		}
		out, err = scanClaim(tx.QueryRow(ctx, `update vendor_rebate_claim set status = 'APPROVED', version = version + 1,
			approved_by = $3, approved_at = now() where id = $1 and version = $2
			returning `+claimFields, id, version, p.UserID))
		if err != nil {
			return err
		}
		if err := enqueueClaim(ctx, tx, out, pevents.VendorRebateClaimApproved); err != nil {
			return err
		}
		return rememberJSON(ctx, tx, p.TenantID, idempotencyKey, hash, out)
	})
	return out, err
}

// GetVendorClaim reads one claim.
func (s *Postgres) GetVendorClaim(ctx context.Context, id string) (VendorClaim, error) {
	var out VendorClaim
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = scanClaim(tx.QueryRow(ctx, claimColumns+`where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return claimNotFound(id)
		}
		return err
	})
	return out, err
}

// ListVendorClaims returns claims, newest first, filtered by agreement, vendor, period and status.
func (s *Postgres) ListVendorClaims(ctx context.Context, f ClaimFilter) ([]VendorClaim, error) {
	if f.Limit <= 0 || f.Limit > MaxListLimit {
		f.Limit = MaxListLimit
	}
	out := []VendorClaim{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, claimColumns+`where ($1::text = '' or agreement_id::text = $1) and ($2::text = '' or vendor_id::text = $2)
			and ($3::text = '' or period_key = $3) and ($4::text = '' or status = $4)
			order by created_at desc, number desc limit $5`, f.AgreementID, f.VendorID, f.PeriodKey, f.Status, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanClaim(rows)
			if err != nil {
				return fmt.Errorf("scan vendor claim: %w", err)
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

func enqueueClaim(ctx context.Context, tx pgx.Tx, c VendorClaim, typ string) error {
	p, _ := tenant.FromContext(ctx)
	evt, err := events.New(typ, eventSource, c.ID, p.TenantID, pevents.VendorRebateClaimData{ClaimID: c.ID, Number: c.Number,
		LegalEntityID: c.LegalEntityID, AgreementID: c.AgreementID, VendorID: c.VendorID, PeriodID: c.PeriodKey,
		AccruedTotal: c.AccruedTotal.String(), PriorClaimed: c.PriorClaimed.String(), Amount: c.Amount.String(), Currency: c.Currency,
		SettlementMode: c.SettlementMode, Status: c.Status, Version: c.Version})
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "vendorrebateclaim", c.ID, evt)
}
