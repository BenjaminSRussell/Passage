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
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	orgevents "github.com/BenjaminSRussell/passage/services/org/internal/events"
)

const (
	CodeRoundingNotFound = "ORG-CUR-ROUNDING-NOT-FOUND"

	RoundingSourceLegalEntity = "legal_entity"
	RoundingSourceTenant      = "tenant"
	RoundingSourceDefault     = "default"

	roundingColumns = "id, currency_code, purpose, legal_entity_id, unit, method, updated_at"
)

type CurrencyRounding struct {
	ID            string          `json:"id"`
	CurrencyCode  string          `json:"currencyCode"`
	Purpose       string          `json:"purpose"`
	LegalEntityID *string         `json:"legalEntityId"`
	Unit          decimal.Decimal `json:"unit"`
	Method        string          `json:"method"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

type RoundingRule struct {
	Unit   decimal.Decimal
	Method domain.Method
	Source string
}

func scanCurrencyRounding(row pgx.Row) (CurrencyRounding, error) {
	var c CurrencyRounding
	err := row.Scan(&c.ID, &c.CurrencyCode, &c.Purpose, &c.LegalEntityID, &c.Unit, &c.Method, &c.UpdatedAt)
	return c, err
}

func requireCurrency(ctx context.Context, tx pgx.Tx, tenantID, code string) (ReferenceItem, error) {
	items, err := queryReference(ctx, tx, KindCurrency, tenantID, code, ReferenceFilter{})
	if err != nil {
		return ReferenceItem{}, err
	}
	if len(items) == 0 || items[0].MinorUnits == nil {
		return ReferenceItem{}, referenceNotFound(KindCurrency, code)
	}
	return items[0], nil
}

func requireLegalEntity(ctx context.Context, tx pgx.Tx, tenantID, id string) error {
	var exists bool
	if !ids.Valid(id) {
		return legalEntityNotFound()
	}
	if err := tx.QueryRow(ctx, `select exists (select 1 from legal_entity where tenant_id = $1 and id = $2)`, tenantID, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return legalEntityNotFound()
	}
	return nil
}

const DutyReference = "org.reference.write"

func (s *Postgres) tenantWriteTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	p, _ := tenant.FromContext(ctx)
	if p.LegalEntityID != "" || !p.HasDuty(DutyReference) {
		return dbx.WithTenantTx(ctx, s.pool, fn)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', '*', true)`, p.TenantID); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (s *Postgres) PutCurrencyRounding(ctx context.Context, in CurrencyRounding) (CurrencyRounding, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out CurrencyRounding
	changed := false
	err := s.tenantWriteTx(ctx, func(tx pgx.Tx) error {
		currency, err := requireCurrency(ctx, tx, p.TenantID, in.CurrencyCode)
		if err != nil {
			return err
		}
		if currency.Status != "active" {
			problem := httpx.NewProblem(http.StatusUnprocessableEntity, CodeReferenceInactive, fmt.Sprintf("currency %s is inactive", in.CurrencyCode))
			problem.Errors = map[string]string{"currency": "must be an active currency"}
			return problem
		}
		if in.LegalEntityID != nil {
			if err := requireLegalEntity(ctx, tx, p.TenantID, *in.LegalEntityID); err != nil {
				return err
			}
		}
		if err := domain.ValidateUnit(in.Unit, int32(*currency.MinorUnits)); err != nil {
			problem := problemFromDomain(err)
			problem.Errors = map[string]string{"unit": "must be a power of ten no finer than the currency minor unit, or 5 or 25 times one"}
			return problem
		}
		out, err = scanCurrencyRounding(tx.QueryRow(ctx, `insert into currency_rounding (tenant_id, id, currency_code, purpose, legal_entity_id, unit, method)
			values ($1, $2, $3, $4, $5, $6, $7)
			on conflict (tenant_id, currency_code, purpose, (coalesce(legal_entity_id, '00000000-0000-0000-0000-000000000000'::uuid)))
			do update set unit = excluded.unit, method = excluded.method, updated_at = now()
			where currency_rounding.unit <> excluded.unit or currency_rounding.method <> excluded.method
			returning `+roundingColumns, p.TenantID, ids.New(), in.CurrencyCode, in.Purpose, in.LegalEntityID, in.Unit.String(), in.Method))
		if errors.Is(err, pgx.ErrNoRows) {
			out, err = scanCurrencyRounding(tx.QueryRow(ctx, `select `+roundingColumns+` from currency_rounding
				where tenant_id = $1 and currency_code = $2 and purpose = $3 and legal_entity_id is not distinct from $4::uuid`, p.TenantID, in.CurrencyCode, in.Purpose, in.LegalEntityID))
			return err
		}
		if err != nil {
			return err
		}
		changed = true
		return enqueue(ctx, tx, "currencyrounding", out.ID, orgevents.CurrencyRoundingChanged, p.TenantID, orgevents.CurrencyRoundingChangedData{
			CurrencyCode: out.CurrencyCode, Purpose: out.Purpose, LegalEntityID: out.LegalEntityID, Unit: out.Unit.String(), Method: out.Method})
	})
	if err != nil {
		return CurrencyRounding{}, false, err
	}
	return out, changed, nil
}

func problemFromDomain(err error) *httpx.Problem {
	var de *domain.Error
	if errors.As(err, &de) {
		return httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail)
	}
	return httpx.NewProblem(http.StatusInternalServerError, "COM-0500", "internal error")
}

func (s *Postgres) ListCurrencyRoundings(ctx context.Context, currency string) ([]CurrencyRounding, error) {
	p, _ := tenant.FromContext(ctx)
	rules := []CurrencyRounding{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := requireCurrency(ctx, tx, p.TenantID, currency); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+roundingColumns+` from currency_rounding where tenant_id = $1 and currency_code = $2
			order by purpose, legal_entity_id is not null, legal_entity_id`, p.TenantID, currency)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			rule, err := scanCurrencyRounding(rows)
			if err != nil {
				return err
			}
			rules = append(rules, rule)
		}
		return rows.Err()
	})
	return rules, err
}

func (s *Postgres) ResolveCurrencyRounding(ctx context.Context, currency, purpose, legalEntityID string) (RoundingRule, error) {
	p, _ := tenant.FromContext(ctx)
	var rule RoundingRule
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		item, err := requireCurrency(ctx, tx, p.TenantID, currency)
		if err != nil {
			return err
		}
		var scope *string
		if legalEntityID != "" {
			if err := requireLegalEntity(ctx, tx, p.TenantID, legalEntityID); err != nil {
				return err
			}
			scope = &legalEntityID
		}
		var method string
		var scoped bool
		err = tx.QueryRow(ctx, `select unit, method, legal_entity_id is not null from currency_rounding
			where tenant_id = $1 and currency_code = $2 and purpose = $3 and (legal_entity_id = $4::uuid or legal_entity_id is null)
			order by legal_entity_id is null limit 1`, p.TenantID, currency, purpose, scope).Scan(&rule.Unit, &method, &scoped)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			rule = RoundingRule{Unit: decimal.New(1, -int32(*item.MinorUnits)), Method: domain.HalfEven, Source: RoundingSourceDefault}
			return nil
		case err != nil:
			return err
		}
		rule.Method = domain.Method(method)
		rule.Source = RoundingSourceTenant
		if scoped {
			rule.Source = RoundingSourceLegalEntity
		}
		return nil
	})
	return rule, err
}

func (s *Postgres) DeleteCurrencyRounding(ctx context.Context, currency, purpose string, legalEntityID *string) error {
	p, _ := tenant.FromContext(ctx)
	return s.tenantWriteTx(ctx, func(tx pgx.Tx) error {
		item, err := requireCurrency(ctx, tx, p.TenantID, currency)
		if err != nil {
			return err
		}
		if legalEntityID != nil {
			if err := requireLegalEntity(ctx, tx, p.TenantID, *legalEntityID); err != nil {
				return err
			}
		}
		var deletedID string
		err = tx.QueryRow(ctx, `delete from currency_rounding where tenant_id = $1 and currency_code = $2 and purpose = $3
			and legal_entity_id is not distinct from $4::uuid returning id::text`, p.TenantID, currency, purpose, legalEntityID).Scan(&deletedID)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeRoundingNotFound, fmt.Sprintf("no %s rounding rule for %s", purpose, currency))
		}
		if err != nil {
			return err
		}
		inForce := RoundingRule{Unit: decimal.New(1, -int32(*item.MinorUnits)), Method: domain.HalfEven}
		if legalEntityID != nil {
			var method string
			var unit decimal.Decimal
			err := tx.QueryRow(ctx, `select unit, method from currency_rounding where tenant_id = $1 and currency_code = $2 and purpose = $3 and legal_entity_id is null`,
				p.TenantID, currency, purpose).Scan(&unit, &method)
			if err == nil {
				inForce = RoundingRule{Unit: unit, Method: domain.Method(method)}
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		return enqueue(ctx, tx, "currencyrounding", deletedID, orgevents.CurrencyRoundingChanged, p.TenantID, orgevents.CurrencyRoundingChangedData{
			CurrencyCode: currency, Purpose: purpose, LegalEntityID: legalEntityID, Unit: inForce.Unit.String(), Method: string(inForce.Method)})
	})
}
