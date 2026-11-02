package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

// Problem codes raised by the charge store (FR-17.9).
const (
	CodeChargeCodeNotFound = "PRC-CHG-NOT-FOUND"
	CodeAutoChargeNotFound = "PRC-CHG-AUTO-NOT-FOUND"
	// CodeChargeCodeExists: the legal entity already has a charge code with this code.
	CodeChargeCodeExists = "PRC-CHG-001"
	// CodeChargeCodeStale: If-Match is not the charge code's version.
	CodeChargeCodeStale = "PRC-CHG-002"
	// CodeAutoChargeStale: If-Match is not the auto charge's version.
	CodeAutoChargeStale = "PRC-CHG-003"
	// CodeChargeCalcLocked: the calculation of a charge code that auto charges use does not change,
	// because their values and currencies were checked against it.
	CodeChargeCalcLocked = "PRC-CHG-006"
)

// ChargeCodeFilter narrows ListChargeCodes; empty fields do not filter.
type ChargeCodeFilter struct {
	LegalEntityID string
	Status        string
	Text          string
}

// AutoChargeFilter narrows ListAutoCharges; empty fields do not filter.
type AutoChargeFilter struct {
	LegalEntityID string
	ChargeCodeID  string
	Status        string
}

const chargeCodeColumns = `id, legal_entity_id, code, name, calc, default_value, debit_type, credit_type,
	coalesce(debit_account_id::text, ''), coalesce(credit_account_id::text, ''), coalesce(vendor_id::text, ''), allocation_rule,
	status, row_version, coalesce(created_by::text, ''), created_at, coalesce(updated_by::text, ''), updated_at`

func scanChargeCode(row pgx.Row, c *domain.ChargeCode) error {
	return row.Scan(&c.ID, &c.LegalEntityID, &c.Code, &c.Name, &c.Calc, &c.DefaultValue, &c.DebitType, &c.CreditType,
		&c.DebitAccountID, &c.CreditAccountID, &c.VendorID, &c.AllocationRule, &c.Status, &c.Version,
		&c.CreatedBy, &c.CreatedAt, &c.UpdatedBy, &c.UpdatedAt)
}

func loadChargeCode(ctx context.Context, tx pgx.Tx, id string) (domain.ChargeCode, error) {
	p, _ := tenant.FromContext(ctx)
	var c domain.ChargeCode
	err := scanChargeCode(tx.QueryRow(ctx, `select `+chargeCodeColumns+` from charge_code where tenant_id = $1 and id = $2`,
		p.TenantID, id), &c)
	return c, notFound(err, CodeChargeCodeNotFound, "charge code not found")
}

func chargeCodeData(c domain.ChargeCode, by string) purchasingevents.ChargeCodeData {
	return purchasingevents.ChargeCodeData{ChargeCodeID: c.ID, LegalEntityID: c.LegalEntityID, Code: c.Code, Name: c.Name,
		Calc: c.Calc, DefaultValue: c.DefaultValue.String(), DebitType: c.DebitType, CreditType: c.CreditType,
		DebitAccountID: c.DebitAccountID, CreditAccountID: c.CreditAccountID, VendorID: c.VendorID,
		AllocationRule: c.AllocationRule, Status: c.Status, Version: c.Version, ChangedBy: by}
}

// CreateChargeCode stores a charge code (version 1) and writes chargecode.created.v1 in the same
// transaction. A replayed Idempotency-Key returns the code the first request created; a second code
// with the same code string in the legal entity is 409 PRC-CHG-001.
func (s *Postgres) CreateChargeCode(ctx context.Context, in domain.ChargeCode, idem Idem) (domain.ChargeCode, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	var out domain.ChargeCode
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		existing, replay, err := claimKey(ctx, tx, idem, in.ID)
		if err != nil {
			return err
		}
		if replay {
			out, err = loadChargeCode(ctx, tx, existing)
			return err
		}
		if _, err := tx.Exec(ctx, `insert into charge_code (tenant_id, id, legal_entity_id, code, name, calc, default_value, debit_type,
			credit_type, debit_account_id, credit_account_id, vendor_id, allocation_rule, status, created_by, updated_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $15)`,
			p.TenantID, in.ID, in.LegalEntityID, in.Code, in.Name, in.Calc, in.DefaultValue, in.DebitType, in.CreditType,
			nullable(in.DebitAccountID), nullable(in.CreditAccountID), nullable(in.VendorID), in.AllocationRule, in.Status,
			userOrNil(p.UserID)); err != nil {
			var pe *pgconn.PgError
			if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "charge_code_unique" {
				return httpx.NewProblem(http.StatusConflict, CodeChargeCodeExists,
					"the legal entity already has a charge code "+in.Code+"; change that one instead")
			}
			return err
		}
		if out, err = loadChargeCode(ctx, tx, in.ID); err != nil {
			return err
		}
		return enqueue(ctx, tx, "chargecode", out.ID, purchasingevents.ChargeCodeCreated, chargeCodeData(out, p.UserID))
	})
	return out, err
}

// GetChargeCode returns one charge code.
func (s *Postgres) GetChargeCode(ctx context.Context, id string) (domain.ChargeCode, error) {
	var out domain.ChargeCode
	err := s.withTx(ctx, func(tx pgx.Tx) (err error) {
		out, err = loadChargeCode(ctx, tx, id)
		return err
	})
	return out, err
}

// ListChargeCodes returns charge codes newest first, after the cursor id.
func (s *Postgres) ListChargeCodes(ctx context.Context, f ChargeCodeFilter, after string, limit int) (Page[domain.ChargeCode], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	items := []domain.ChargeCode{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+chargeCodeColumns+` from charge_code
			where tenant_id = $1 and ($2::uuid is null or legal_entity_id = $2::uuid) and ($3 = '' or status = $3)
			and ($4 = '' or code ilike '%' || $4 || '%' or name ilike '%' || $4 || '%') and ($5::uuid is null or id < $5::uuid)
			order by id desc limit $6`, p.TenantID, nullable(f.LegalEntityID), f.Status, f.Text, nullable(after), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c domain.ChargeCode
			if err := scanChargeCode(rows, &c); err != nil {
				return err
			}
			items = append(items, c)
		}
		return rows.Err()
	})
	if err != nil {
		return Page[domain.ChargeCode]{}, err
	}
	return pageOf(items, limit, func(c domain.ChargeCode) string { return c.ID }), nil
}

// UpdateChargeCode replaces the name, calculation, default value, sides, allocation rule and status of a
// charge code under If-Match and writes chargecode.changed.v1. The legal entity and the code string do
// not change. A stale version is 412 PRC-CHG-002. POs already carry a copy of the sides, so a change
// does not move them.
func (s *Postgres) UpdateChargeCode(ctx context.Context, id string, ifMatch int, in domain.ChargeCode) (domain.ChargeCode, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.ChargeCode
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := loadChargeCode(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Version != ifMatch {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeChargeCodeStale,
				fmt.Sprintf("charge code is at version %d, not %d; reload and retry", cur.Version, ifMatch))
		}
		if in.Calc != cur.Calc {
			var used bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from auto_charge where tenant_id = $1 and charge_code_id = $2)`,
				p.TenantID, id).Scan(&used); err != nil {
				return err
			}
			if used {
				return httpx.NewProblem(http.StatusConflict, CodeChargeCalcLocked,
					"charge code "+cur.Code+" is used by auto charges, whose values were set for "+cur.Calc+"; keep the calculation or create another code")
			}
		}
		tag, err := tx.Exec(ctx, `update charge_code set name = $3, calc = $4, default_value = $5, debit_type = $6, credit_type = $7,
			debit_account_id = $8, credit_account_id = $9, vendor_id = $10, allocation_rule = $11, status = $12,
			updated_by = $13, updated_at = now(), row_version = row_version + 1
			where tenant_id = $1 and id = $2 and row_version = $14`,
			p.TenantID, id, in.Name, in.Calc, in.DefaultValue, in.DebitType, in.CreditType, nullable(in.DebitAccountID),
			nullable(in.CreditAccountID), nullable(in.VendorID), in.AllocationRule, in.Status, userOrNil(p.UserID), ifMatch)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeChargeCodeStale,
				fmt.Sprintf("charge code changed after version %d; reload and retry", ifMatch))
		}
		if out, err = loadChargeCode(ctx, tx, id); err != nil {
			return err
		}
		return enqueue(ctx, tx, "chargecode", id, purchasingevents.ChargeCodeChanged, chargeCodeData(out, p.UserID))
	})
	return out, err
}

const autoChargeColumns = `id, legal_entity_id, charge_code_id, account_code, coalesce(account_id::text, ''), value, min_order_amount,
	coalesce(currency, ''), valid_from, valid_to, status, row_version, coalesce(created_by::text, ''), created_at,
	coalesce(updated_by::text, ''), updated_at`

func scanAutoCharge(row pgx.Row, a *domain.AutoCharge) error {
	var from time.Time
	var to *time.Time
	var value, min *decimal.Decimal
	if err := row.Scan(&a.ID, &a.LegalEntityID, &a.ChargeCodeID, &a.AccountCode, &a.AccountID, &value, &min, &a.Currency,
		&from, &to, &a.Status, &a.Version, &a.CreatedBy, &a.CreatedAt, &a.UpdatedBy, &a.UpdatedAt); err != nil {
		return err
	}
	a.Value, a.MinOrderAmount = value, min
	a.ValidFrom, a.ValidTo = day(from), dayOrNil(to)
	return nil
}

func loadAutoCharge(ctx context.Context, tx pgx.Tx, id string) (domain.AutoCharge, error) {
	p, _ := tenant.FromContext(ctx)
	var a domain.AutoCharge
	err := scanAutoCharge(tx.QueryRow(ctx, `select `+autoChargeColumns+` from auto_charge where tenant_id = $1 and id = $2`,
		p.TenantID, id), &a)
	return a, notFound(err, CodeAutoChargeNotFound, "auto charge not found")
}

func autoChargeData(a domain.AutoCharge, by string) purchasingevents.AutoChargeData {
	d := purchasingevents.AutoChargeData{AutoChargeID: a.ID, LegalEntityID: a.LegalEntityID, ChargeCodeID: a.ChargeCodeID,
		AccountCode: a.AccountCode, AccountID: a.AccountID, Currency: a.Currency, ValidFrom: a.ValidFrom.String(),
		Status: a.Status, Version: a.Version, ChangedBy: by}
	if a.Value != nil {
		d.Value = a.Value.String()
	}
	if a.MinOrderAmount != nil {
		d.MinOrderAmount = a.MinOrderAmount.String()
	}
	if a.ValidTo != nil {
		d.ValidTo = a.ValidTo.String()
	}
	return d
}

// CreateAutoCharge stores an auto charge (version 1) and writes autocharge.created.v1 in the same
// transaction. A replayed Idempotency-Key returns the row the first request created.
func (s *Postgres) CreateAutoCharge(ctx context.Context, in domain.AutoCharge, idem Idem) (domain.AutoCharge, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	var out domain.AutoCharge
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		existing, replay, err := claimKey(ctx, tx, idem, in.ID)
		if err != nil {
			return err
		}
		if replay {
			out, err = loadAutoCharge(ctx, tx, existing)
			return err
		}
		if _, err := tx.Exec(ctx, `insert into auto_charge (tenant_id, id, legal_entity_id, charge_code_id, account_code, account_id, value,
			min_order_amount, currency, valid_from, valid_to, status, created_by, updated_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $13)`,
			p.TenantID, in.ID, in.LegalEntityID, in.ChargeCodeID, in.AccountCode, nullable(in.AccountID), in.Value, in.MinOrderAmount,
			nullable(in.Currency), in.ValidFrom.Time, dayPtr(in.ValidTo), in.Status, userOrNil(p.UserID)); err != nil {
			return err
		}
		if out, err = loadAutoCharge(ctx, tx, in.ID); err != nil {
			return err
		}
		return enqueue(ctx, tx, "autocharge", out.ID, purchasingevents.AutoChargeCreated, autoChargeData(out, p.UserID))
	})
	return out, err
}

// GetAutoCharge returns one auto charge.
func (s *Postgres) GetAutoCharge(ctx context.Context, id string) (domain.AutoCharge, error) {
	var out domain.AutoCharge
	err := s.withTx(ctx, func(tx pgx.Tx) (err error) {
		out, err = loadAutoCharge(ctx, tx, id)
		return err
	})
	return out, err
}

// ListAutoCharges returns auto charges newest first, after the cursor id.
func (s *Postgres) ListAutoCharges(ctx context.Context, f AutoChargeFilter, after string, limit int) (Page[domain.AutoCharge], error) {
	p, _ := tenant.FromContext(ctx)
	limit = clampLimit(limit)
	items := []domain.AutoCharge{}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+autoChargeColumns+` from auto_charge
			where tenant_id = $1 and ($2::uuid is null or legal_entity_id = $2::uuid) and ($3::uuid is null or charge_code_id = $3::uuid)
			and ($4 = '' or status = $4) and ($5::uuid is null or id < $5::uuid) order by id desc limit $6`,
			p.TenantID, nullable(f.LegalEntityID), nullable(f.ChargeCodeID), f.Status, nullable(after), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a domain.AutoCharge
			if err := scanAutoCharge(rows, &a); err != nil {
				return err
			}
			items = append(items, a)
		}
		return rows.Err()
	})
	if err != nil {
		return Page[domain.AutoCharge]{}, err
	}
	return pageOf(items, limit, func(a domain.AutoCharge) string { return a.ID }), nil
}

// UpdateAutoCharge replaces the value, minimum order amount, currency, window and status of an auto
// charge under If-Match and writes autocharge.changed.v1. Its charge code and key do not change.
// A stale version is 412 PRC-CHG-003.
func (s *Postgres) UpdateAutoCharge(ctx context.Context, id string, ifMatch int, in domain.AutoCharge) (domain.AutoCharge, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.AutoCharge
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := loadAutoCharge(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Version != ifMatch {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeAutoChargeStale,
				fmt.Sprintf("auto charge is at version %d, not %d; reload and retry", cur.Version, ifMatch))
		}
		tag, err := tx.Exec(ctx, `update auto_charge set value = $3, min_order_amount = $4, currency = $5, valid_from = $6, valid_to = $7,
			status = $8, updated_by = $9, updated_at = now(), row_version = row_version + 1
			where tenant_id = $1 and id = $2 and row_version = $10`,
			p.TenantID, id, in.Value, in.MinOrderAmount, nullable(in.Currency), in.ValidFrom.Time, dayPtr(in.ValidTo), in.Status,
			userOrNil(p.UserID), ifMatch)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeAutoChargeStale,
				fmt.Sprintf("auto charge changed after version %d; reload and retry", ifMatch))
		}
		if out, err = loadAutoCharge(ctx, tx, id); err != nil {
			return err
		}
		return enqueue(ctx, tx, "autocharge", id, purchasingevents.AutoChargeChanged, autoChargeData(out, p.UserID))
	})
	return out, err
}

// ActiveAutoCharges returns the ACTIVE auto charges of a legal entity keyed on vendorID, on one of
// groups or on every vendor and valid on day, oldest first; the caller applies the currency and
// minimum order amount with domain.AutoCharge.Applies.
func (s *Postgres) ActiveAutoCharges(ctx context.Context, legalEntityID, vendorID string, groups []string, on domain.Day) ([]domain.AutoCharge, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.AutoCharge{}
	if groups == nil {
		groups = []string{}
	}
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+autoChargeColumns+` from auto_charge
			where tenant_id = $1 and legal_entity_id = $2 and status = 'ACTIVE'
			and (account_code = 'ALL' or (account_code = 'VENDOR' and account_id = $3::uuid)
				or (account_code = 'GROUP' and account_id = any($4::uuid[])))
			and valid_from <= $5 and (valid_to is null or valid_to >= $5)
			order by id`, p.TenantID, legalEntityID, vendorID, groups, on.Time)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a domain.AutoCharge
			if err := scanAutoCharge(rows, &a); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}
