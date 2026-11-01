package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

// Alternative item problem codes (README lists the full catalogue).
const (
	CodeAlternativeDuplicate = "SLS-0055"
	CodeAlternativeNotFound  = "SLS-0113"
)

// AlternativeRuleFilter narrows the rule list. An empty LegalEntityID means
// every legal entity in the caller's scope; OrderedProductIDs empty means every
// ordered product.
type AlternativeRuleFilter struct {
	LegalEntityID     string
	OrderedProductIDs []string
	Limit             int
}

const alternativeColumns = `id::text, legal_entity_id::text, ordered_product_id::text, alternative_product_id::text,
	customer_id::text, priority::int, valid_from::text, valid_to::text`

func scanAlternativeRule(row pgx.Row) (domain.AlternativeRule, error) {
	var r domain.AlternativeRule
	err := row.Scan(&r.ID, &r.LegalEntityID, &r.OrderedProductID, &r.AlternativeProductID, &r.CustomerID, &r.Priority,
		&r.ValidFrom, &r.ValidTo)
	return r, err
}

// CreateAlternativeRule stores a validated rule. A second rule for the same
// ordered product, customer (or all customers) and alternative is refused with
// 409.
func (s *Postgres) CreateAlternativeRule(ctx context.Context, in domain.AlternativeRule, actor string) (domain.AlternativeRule, error) {
	p, _ := tenant.FromContext(ctx)
	in.ID = ids.New()
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into alternative_item_rule (tenant_id, id, legal_entity_id, ordered_product_id,
				alternative_product_id, customer_id, priority, valid_from, valid_to, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8::text::date, $9::text::date, $10)`,
			p.TenantID, in.ID, in.LegalEntityID, in.OrderedProductID, in.AlternativeProductID, in.CustomerID, in.Priority,
			in.ValidFrom, in.ValidTo, actor)
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return domain.AlternativeRule{}, problem(http.StatusConflict, CodeAlternativeDuplicate,
			"an alternative item rule for this ordered product, customer and alternative already exists")
	}
	if err != nil {
		return domain.AlternativeRule{}, err
	}
	return in, nil
}

// ListAlternativeRules returns rules in the caller's scope by ordered product,
// priority and creation.
func (s *Postgres) ListAlternativeRules(ctx context.Context, f AlternativeRuleFilter) ([]domain.AlternativeRule, error) {
	p, _ := tenant.FromContext(ctx)
	products := f.OrderedProductIDs
	if products == nil {
		products = []string{}
	}
	out := []domain.AlternativeRule{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+alternativeColumns+` from alternative_item_rule
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2)
				and (cardinality($3::uuid[]) = 0 or ordered_product_id = any($3::uuid[]))
			order by ordered_product_id, priority, created_at, id limit $4`, p.TenantID, f.LegalEntityID, products, f.Limit)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.AlternativeRule, error) { return scanAlternativeRule(row) })
		return err
	})
	return out, err
}

// DeleteAlternativeRule removes a rule.
func (s *Postgres) DeleteAlternativeRule(ctx context.Context, id string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from alternative_item_rule where tenant_id = $1 and id = $2`, p.TenantID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return problem(http.StatusNotFound, CodeAlternativeNotFound, "alternative item rule not found")
		}
		return nil
	})
}
