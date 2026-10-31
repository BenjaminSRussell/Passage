package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
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

// CodeAgreementNotFound is raised when a rebate agreement id does not exist.
const CodeAgreementNotFound = "PRC-0035"

// CodePartyMismatch is raised when an invoice of one party role is presented to an
// agreement of the other: sales invoices accrue CUSTOMER agreements, vendor invoices
// accrue VENDOR agreements (FR-26.15).
const CodePartyMismatch = "PRC-0045"

// Accrual is one invoice line's accrual row (FR-26.11).
type Accrual struct {
	ID            string
	AgreementID   string
	PeriodKey     string
	InvoiceLineID string
	InvoiceDate   time.Time
	BasisValue    decimal.Decimal
	BasisQty      decimal.Decimal
	Accrued       decimal.Decimal
	Currency      string
	Status        string
	// Retroactive marks rows written by a retroactive recompute (FR-26.23).
	Retroactive bool
}

// AccrualInput is one posted invoice line presented for accrual. Party, when set, must
// match the agreement's party role (PRC-0045): the sales invoice feed sends CUSTOMER and
// the vendor invoice feed sends VENDOR.
type AccrualInput struct {
	Party         string
	AgreementID   string
	InvoiceLineID string
	InvoiceDate   time.Time
	Value         decimal.Decimal
	Quantity      decimal.Decimal
	Currency      string
}

// AccrualFilter selects accrual rows by agreement and period.
type AccrualFilter struct {
	AgreementID string
	PeriodKey   string
	Limit       int
}

const accrualColumns = `select id::text, agreement_id::text, period_key, invoice_line_id, invoice_date,
	basis_value::text, basis_qty::text, accrued_amount::text, currency::text, status, retroactive from rebate_accrual`

func scanAccrual(row pgx.Row) (Accrual, error) {
	var a Accrual
	var bv, bq, acc string
	err := row.Scan(&a.ID, &a.AgreementID, &a.PeriodKey, &a.InvoiceLineID, &a.InvoiceDate, &bv, &bq, &acc, &a.Currency, &a.Status, &a.Retroactive)
	if err != nil {
		return Accrual{}, err
	}
	a.BasisValue, a.BasisQty, a.Accrued = decimal.RequireFromString(bv), decimal.RequireFromString(bq), decimal.RequireFromString(acc)
	return a, nil
}

// CreateAgreement stores a rebate agreement. A repeated Idempotency-Key returns the
// original agreement when the body matches and is rejected otherwise; the insert uses
// ON CONFLICT so two concurrent requests with one key cannot both create an agreement.
func (s *Postgres) CreateAgreement(ctx context.Context, a domain.RebateAgreement, idempotencyKey string) (domain.RebateAgreement, error) {
	p, _ := tenant.FromContext(ctx)
	tiers, err := marshalTiers(a.Tiers)
	if err != nil {
		return a, err
	}
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	a.PartyRole = domain.PartyRoleOf(a)
	var out domain.RebateAgreement
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		a.ID = ids.New()
		var inserted string
		err := tx.QueryRow(ctx, `insert into rebate_agreement (tenant_id, id, legal_entity_id, name, party_role, customer_id, customer_group_id,
			vendor_id, basis, period, tiers, currency, valid_from, valid_to, accrual_account, expense_account, idempotency_key)
			values ($1, $2, $3, $4, $5, nullif($6, '')::uuid, nullif($7, '')::uuid, nullif($8, '')::uuid, $9, $10, $11, $12, $13, $14, $15, $16, $17)
			on conflict (tenant_id, idempotency_key) do nothing returning id::text`,
			p.TenantID, a.ID, a.LegalEntityID, a.Name, a.PartyRole, a.CustomerID, a.CustomerGroupID, a.VendorID, a.Basis, a.Period, tiers,
			a.Currency, a.ValidFrom, a.ValidTo, a.AccrualAccount, a.ExpenseAccount, key).Scan(&inserted)
		if errors.Is(err, pgx.ErrNoRows) && key != nil {
			replay, err := loadAgreement(ctx, tx, `where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key)
			if err != nil {
				return err
			}
			if !sameAgreement(replay, a) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse, "Idempotency-Key was already used with a different request body")
			}
			out = replay
			return nil
		}
		if err != nil {
			return err
		}
		a.Status, a.Version = domain.AgreementActive, 1
		out = a
		return nil
	})
	return out, err
}

// GetAgreement reads one rebate agreement.
func (s *Postgres) GetAgreement(ctx context.Context, id string) (domain.RebateAgreement, error) {
	var out domain.RebateAgreement
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = loadAgreement(ctx, tx, `where id = $1`, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return agreementNotFound(id)
		}
		return err
	})
	return out, err
}

func agreementNotFound(id string) error {
	return httpx.NewProblem(http.StatusNotFound, CodeAgreementNotFound, "no rebate agreement "+id)
}

const agreementColumns = `select id::text, legal_entity_id::text, name, party_role, coalesce(customer_id::text, ''),
	coalesce(customer_group_id::text, ''), coalesce(vendor_id::text, ''), basis, period, tiers, currency::text, valid_from, valid_to,
	accrual_account, expense_account, status, version from rebate_agreement `

func scanAgreement(row pgx.Row) (domain.RebateAgreement, error) {
	var a domain.RebateAgreement
	var tiers []byte
	if err := row.Scan(&a.ID, &a.LegalEntityID, &a.Name, &a.PartyRole, &a.CustomerID, &a.CustomerGroupID, &a.VendorID, &a.Basis,
		&a.Period, &tiers, &a.Currency, &a.ValidFrom, &a.ValidTo, &a.AccrualAccount, &a.ExpenseAccount, &a.Status, &a.Version); err != nil {
		return a, err
	}
	if err := json.Unmarshal(tiers, &a.Tiers); err != nil {
		return a, fmt.Errorf("decode agreement tiers: %w", err)
	}
	return a, nil
}

// loadAgreement reads one agreement matched by a trailing WHERE clause.
func loadAgreement(ctx context.Context, tx pgx.Tx, where string, args ...any) (domain.RebateAgreement, error) {
	return scanAgreement(tx.QueryRow(ctx, agreementColumns+where, args...))
}

// sameAgreement reports whether a replayed create carries the same economic content.
func sameAgreement(stored, in domain.RebateAgreement) bool {
	a, _ := json.Marshal(stored.Tiers)
	b, _ := json.Marshal(in.Tiers)
	return stored.LegalEntityID == in.LegalEntityID && stored.Name == in.Name && stored.PartyRole == domain.PartyRoleOf(in) &&
		stored.CustomerID == in.CustomerID && stored.CustomerGroupID == in.CustomerGroupID && stored.VendorID == in.VendorID &&
		stored.Basis == in.Basis && stored.Period == in.Period && stored.AccrualAccount == in.AccrualAccount &&
		stored.ExpenseAccount == in.ExpenseAccount &&
		stored.Currency == in.Currency && string(a) == string(b) &&
		dayStr(stored.ValidFrom) == dayStr(in.ValidFrom) && dayStr(stored.ValidTo) == dayStr(in.ValidTo)
}

// AccrueInvoiceLine accrues one posted invoice line (FR-26.11, BR-26.8). It locks the
// period row, moves the period to its target at the new cumulative basis and books the
// difference on this invoice, so a tier crossing posts a delta on the next invoice.
// A repeated invoice line returns its existing accrual with created=false.
func (s *Postgres) AccrueInvoiceLine(ctx context.Context, in AccrualInput) (Accrual, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out Accrual
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// FOR SHARE: accruals of one agreement run side by side, but a revise or close (FOR UPDATE)
		// waits for them and they wait for it, so no invoice accrues past a close date or at
		// tiers that a concurrent revise is replacing.
		a, err := loadAgreement(ctx, tx, `where id = $1 for share`, in.AgreementID)
		if errors.Is(err, pgx.ErrNoRows) {
			return agreementNotFound(in.AgreementID)
		}
		if err != nil {
			return err
		}
		if in.Party != "" && in.Party != a.PartyRole {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodePartyMismatch,
				"agreement "+a.ID+" is a "+a.PartyRole+" rebate agreement; it does not accrue "+strings.ToLower(in.Party)+" invoices")
		}
		if in.Currency != a.Currency {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "invoice currency does not match the agreement currency")
		}
		if day := dayStr(in.InvoiceDate); day < dayStr(a.ValidFrom) || day > dayStr(a.ValidTo) {
			return httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "invoice date is outside the agreement validity")
		}
		key := domain.PeriodKey(a.Period, in.InvoiceDate)
		if _, err := tx.Exec(ctx, `insert into rebate_period (tenant_id, agreement_id, period_key) values ($1, $2, $3) on conflict do nothing`,
			p.TenantID, a.ID, key); err != nil {
			return err
		}
		// Lock the period before the duplicate check: two requests for one invoice line serialize here.
		var bv, bq, accrued string
		if err := tx.QueryRow(ctx, `select basis_value::text, basis_qty::text, accrued_total::text from rebate_period
			where agreement_id = $1 and period_key = $2 for update`, a.ID, key).Scan(&bv, &bq, &accrued); err != nil {
			return err
		}
		existing, err := scanAccrual(tx.QueryRow(ctx, accrualColumns+` where agreement_id = $1 and invoice_line_id = $2`, a.ID, in.InvoiceLineID))
		if err == nil {
			if !sameInvoiceLine(existing, in) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse,
					"invoice line "+in.InvoiceLineID+" was already accrued with a different date, value or quantity")
			}
			out = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		prior := domain.PeriodState{Value: decimal.RequireFromString(bv), Qty: decimal.RequireFromString(bq), Accrued: decimal.RequireFromString(accrued)}
		next, inc := domain.Accrue(a, prior, in.Value, in.Quantity)
		if _, err := tx.Exec(ctx, `update rebate_period set basis_value = $3::numeric, basis_qty = $4::numeric, accrued_total = $5::numeric
			where agreement_id = $1 and period_key = $2`, a.ID, key, next.Value.String(), next.Qty.String(), next.Accrued.String()); err != nil {
			return err
		}
		out, err = scanAccrual(tx.QueryRow(ctx, `insert into rebate_accrual (tenant_id, id, legal_entity_id, agreement_id, period_key,
			invoice_line_id, invoice_date, basis_value, basis_qty, accrued_amount, currency)
			values ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9::numeric, $10::numeric, $11)
			returning id::text, agreement_id::text, period_key, invoice_line_id, invoice_date,
			basis_value::text, basis_qty::text, accrued_amount::text, currency::text, status, retroactive`,
			p.TenantID, ids.New(), a.LegalEntityID, a.ID, key, in.InvoiceLineID, in.InvoiceDate, in.Value.String(), in.Quantity.String(),
			inc.String(), a.Currency))
		if err != nil {
			return err
		}
		created = true
		if inc.IsZero() {
			return nil
		}
		debit, credit := domain.AccrualPosting(a)
		evt, err := events.New(pevents.RebateAccrualPosted, eventSource, out.ID, p.TenantID, pevents.RebateAccrualPostedData{
			AccrualID: out.ID, LegalEntityID: a.LegalEntityID, AgreementID: a.ID, PartyRole: a.PartyRole, PeriodID: key,
			InvoiceLineID: in.InvoiceLineID, Amount: inc.String(), Currency: a.Currency, DebitAccount: debit, CreditAccount: credit})
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, "rebateaccrual", out.ID, evt)
	})
	return out, created, err
}

// ListAccruals returns accrual rows, optionally filtered by agreement and period.
func (s *Postgres) ListAccruals(ctx context.Context, f AccrualFilter) ([]Accrual, error) {
	if f.Limit <= 0 || f.Limit > MaxListLimit {
		f.Limit = MaxListLimit
	}
	out := []Accrual{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, accrualColumns+` where ($1::text = '' or agreement_id::text = $1) and ($2::text = '' or period_key = $2)
			order by invoice_date, retroactive, created_at, id limit $3`, f.AgreementID, f.PeriodKey, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAccrual(rows)
			if err != nil {
				return fmt.Errorf("scan accrual: %w", err)
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// AgreementFilter selects rebate agreements by legal entity and party role.
type AgreementFilter struct {
	LegalEntityID string
	PartyRole     string
	Limit         int
}

// ListAgreements returns rebate agreements, optionally filtered by legal entity and party
// role (FR-26.11, FR-26.15).
func (s *Postgres) ListAgreements(ctx context.Context, f AgreementFilter) ([]domain.RebateAgreement, error) {
	if f.Limit <= 0 || f.Limit > MaxListLimit {
		f.Limit = MaxListLimit
	}
	out := []domain.RebateAgreement{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, agreementColumns+`where ($1::text = '' or legal_entity_id::text = $1)
			and ($2::text = '' or party_role = $2) order by valid_from, id limit $3`, f.LegalEntityID, f.PartyRole, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAgreement(rows)
			if err != nil {
				return fmt.Errorf("scan agreement: %w", err)
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// sameInvoiceLine reports whether a redelivered invoice line matches the one already accrued.
func sameInvoiceLine(a Accrual, in AccrualInput) bool {
	return dayStr(a.InvoiceDate) == dayStr(in.InvoiceDate) && a.BasisValue.Equal(in.Value) && a.BasisQty.Equal(in.Quantity)
}

func marshalTiers(t []domain.Tier) ([]byte, error) { return json.Marshal(t) }

func tiersHash(t *[]domain.Tier) string {
	if t == nil {
		return "-"
	}
	b, _ := json.Marshal(*t)
	return string(b)
}

func ptrStr(s *string) string {
	if s == nil {
		return "-"
	}
	return "=" + *s
}

func ptrDay(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return dayStr(*t)
}
