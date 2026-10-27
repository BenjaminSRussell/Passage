package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

const promoColumns = `id::text, legal_entity_id::text, name, event_category, event_type, customer_scope,
	coalesce(fund_id::text, ''), product_id, currency::text,
	start_date, end_date, baseline_weekly_units::text[], lift_pct::text, planned_spend::text,
	actual_units::text, actual_weeks, actual_spend::text`

func scanPromo(row pgx.Row) (domain.PromotionPlan, error) {
	var p domain.PromotionPlan
	var baseline []string
	var lift, spend string
	var actUnits, actSpend *string
	var actWeeks *int32
	if err := row.Scan(&p.ID, &p.LegalEntityID, &p.Name, &p.EventCategory, &p.EventType, &p.CustomerScope, &p.FundID, &p.ProductID, &p.Currency, &p.Start, &p.End,
		&baseline, &lift, &spend, &actUnits, &actWeeks, &actSpend); err != nil {
		return domain.PromotionPlan{}, err
	}
	p.LiftPct = decimal.RequireFromString(lift)
	p.PlannedSpend = decimal.RequireFromString(spend)
	for _, b := range baseline {
		p.BaselineWeekly = append(p.BaselineWeekly, decimal.RequireFromString(b))
	}
	if actUnits != nil && actWeeks != nil {
		a := domain.PromotionActuals{Units: decimal.RequireFromString(*actUnits), Weeks: int(*actWeeks)}
		if actSpend != nil {
			a.Spend = decimal.RequireFromString(*actSpend)
		}
		p.Actuals = &a
	}
	return p, nil
}

func promoNotFound(id string) error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodePromoNotFound, "no promotion plan "+id)
}

// samePromo reports whether a replayed create carries the same economic content.
func samePromo(a, b domain.PromotionPlan) bool {
	if a.LegalEntityID != b.LegalEntityID || a.Name != b.Name || a.EventCategory != b.EventCategory || a.EventType != b.EventType ||
		a.CustomerScope != b.CustomerScope || a.FundID != b.FundID || a.ProductID != b.ProductID ||
		a.Currency != b.Currency || dayStr(a.Start) != dayStr(b.Start) || dayStr(a.End) != dayStr(b.End) ||
		!a.LiftPct.Equal(b.LiftPct) || !a.PlannedSpend.Equal(b.PlannedSpend) || len(a.BaselineWeekly) != len(b.BaselineWeekly) {
		return false
	}
	for i := range a.BaselineWeekly {
		if !a.BaselineWeekly[i].Equal(b.BaselineWeekly[i]) {
			return false
		}
	}
	return true
}

// CreatePromotion stores a promotion plan. The fund, when given, must exist in the same
// legal entity and currency and be open (PRC-0011); it is share-locked so a concurrent
// close cannot slip between the check and the insert.
func (s *Postgres) CreatePromotion(ctx context.Context, p domain.PromotionPlan, idempotencyKey string) (domain.PromotionPlan, error) {
	tp, _ := tenant.FromContext(ctx)
	var key, fund *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	if p.FundID != "" {
		fund = &p.FundID
	}
	baseline := make([]string, len(p.BaselineWeekly))
	for i, w := range p.BaselineWeekly {
		baseline[i] = w.String()
	}
	var out domain.PromotionPlan
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanPromo(tx.QueryRow(ctx, `select `+promoColumns+` from promotion_plan where idempotency_key = $1`, idempotencyKey))
			if err == nil {
				if !samePromo(replay, p) {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyUse, "Idempotency-Key was already used with a different request body")
				}
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if fund != nil {
			f, err := scanFund(tx.QueryRow(ctx, `select `+fundColumns+` from trade_fund where id = $1 for share`, *fund))
			if errors.Is(err, pgx.ErrNoRows) {
				return fundNotFound(*fund)
			}
			if err != nil {
				return err
			}
			fields, err := domain.CheckPromotionFund(f, p)
			if len(fields) > 0 {
				prob := httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, "promotion cannot draw on this fund")
				prob.Errors = fields
				return prob
			}
			if err != nil {
				return RuleProblem(err)
			}
		}
		var err error
		out, err = scanPromo(tx.QueryRow(ctx, `insert into promotion_plan (tenant_id, id, legal_entity_id, name, event_category, event_type,
			customer_scope, fund_id, product_id, currency, start_date, end_date, baseline_weekly_units, lift_pct, planned_spend, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::date, $12::date, $13::text[]::numeric[], $14::numeric, $15::numeric, $16)
			returning `+promoColumns,
			tp.TenantID, ids.New(), p.LegalEntityID, p.Name, p.EventCategory, p.EventType, p.CustomerScope, fund, p.ProductID, p.Currency, dayStr(p.Start), dayStr(p.End),
			baseline, p.LiftPct.String(), p.PlannedSpend.String(), key))
		return err
	})
	return out, err
}

// GetPromotion reads one promotion plan.
func (s *Postgres) GetPromotion(ctx context.Context, id string) (domain.PromotionPlan, error) {
	var p domain.PromotionPlan
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		p, err = scanPromo(tx.QueryRow(ctx, `select `+promoColumns+` from promotion_plan where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return promoNotFound(id)
		}
		return err
	})
	return p, err
}

// ListPromotions returns promotion plans ordered by id, optionally for one legal entity.
func (s *Postgres) ListPromotions(ctx context.Context, legalEntityID string, limit int) ([]domain.PromotionPlan, error) {
	if limit <= 0 || limit > MaxListLimit {
		limit = MaxListLimit
	}
	out := []domain.PromotionPlan{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+promoColumns+` from promotion_plan
			where ($1::text = '' or legal_entity_id::text = $1) order by id limit $2`, legalEntityID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanPromo(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

// RecordPromotionActuals stores the units and spend posted inside the promotion window.
// Actuals are overwritten on each call; the nightly actuals job (FR-26.20) feeds the same write.
func (s *Postgres) RecordPromotionActuals(ctx context.Context, id string, a domain.PromotionActuals) (domain.PromotionPlan, error) {
	var out domain.PromotionPlan
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := scanPromo(tx.QueryRow(ctx, `update promotion_plan set actual_units = $2::numeric, actual_weeks = $3, actual_spend = $4::numeric
			where id = $1 returning `+promoColumns, id, a.Units.String(), a.Weeks, a.Spend.String()))
		if errors.Is(err, pgx.ErrNoRows) {
			return promoNotFound(id)
		}
		out = p
		return err
	})
	return out, err
}
