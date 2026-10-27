package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

// Supplementary rule problem codes (README lists the full catalogue).
const (
	CodeSupplementaryDuplicate = "SLS-0033"
	CodeSupplementaryNotFound  = "SLS-0106"
)

// SupplementaryRuleFilter narrows the rule list. Empty fields match
// everything; an empty LegalEntityID means every legal entity in the caller's
// scope.
type SupplementaryRuleFilter struct {
	LegalEntityID   string
	ParentProductID string
	Limit           int
}

const supplementaryColumns = `id::text, legal_entity_id::text, parent_product_id::text, customer_id::text, supplementary_product_id::text,
	unit, requirement, base_quantity::text, quantity_per_base::text, pricing, unit_price::text`

func scanSupplementaryRule(row pgx.Row) (domain.SupplementaryRule, error) {
	var r domain.SupplementaryRule
	var base, per, price string
	if err := row.Scan(&r.ID, &r.LegalEntityID, &r.ParentProductID, &r.CustomerID, &r.SupplementaryProduct, &r.Unit,
		&r.Requirement, &base, &per, &r.Pricing, &price); err != nil {
		return domain.SupplementaryRule{}, err
	}
	r.BaseQuantity = decimal.RequireFromString(base)
	r.QuantityPerBase = decimal.RequireFromString(per)
	r.UnitPrice = decimal.RequireFromString(price)
	return r, nil
}

// CreateSupplementaryRule stores a validated rule. A second rule for the same
// parent, customer (or all customers) and supplementary product is refused
// with 409, so evaluation never adds an item twice.
func (s *Postgres) CreateSupplementaryRule(ctx context.Context, in domain.SupplementaryRule, actor string) (domain.SupplementaryRule, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	var out domain.SupplementaryRule
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanSupplementaryRule(tx.QueryRow(ctx, `insert into supplementary_item_rule (tenant_id, id, legal_entity_id, parent_product_id,
				customer_id, supplementary_product_id, unit, requirement, base_quantity, quantity_per_base, pricing, unit_price, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10::numeric, $11, $12::numeric, $13)
			returning `+supplementaryColumns,
			p.TenantID, in.ID, in.LegalEntityID, in.ParentProductID, in.CustomerID, in.SupplementaryProduct, in.Unit, in.Requirement,
			in.BaseQuantity.String(), in.QuantityPerBase.String(), in.Pricing, in.UnitPrice.String(), actor))
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return domain.SupplementaryRule{}, problem(http.StatusConflict, CodeSupplementaryDuplicate,
			"a rule for this parent product, customer and supplementary product already exists")
	}
	return out, err
}

// ListSupplementaryRules returns rules in the caller's scope, all-customer
// rules and customer rules in creation order.
func (s *Postgres) ListSupplementaryRules(ctx context.Context, f SupplementaryRuleFilter) ([]domain.SupplementaryRule, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.SupplementaryRule{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+supplementaryColumns+` from supplementary_item_rule
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) and ($3 = '' or parent_product_id::text = $3)
			order by created_at, id limit $4`, p.TenantID, f.LegalEntityID, f.ParentProductID, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanSupplementaryRule(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// DeleteSupplementaryRule removes a rule. Stored orders are not changed.
func (s *Postgres) DeleteSupplementaryRule(ctx context.Context, id string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from supplementary_item_rule where tenant_id = $1 and id = $2`, p.TenantID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return problem(http.StatusNotFound, CodeSupplementaryNotFound, "supplementary item rule not found")
		}
		return nil
	})
}
