package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

// Template is how the revenue of a product is recognized when its contract is opened from a posted invoice (29-4):
// POINT_IN_TIME at the posting date, RATABLE over TermMonths from the posting date, MILESTONE (recognized when the milestone is
// satisfied) or USAGE (recognized as usage posts).
type Template struct {
	ProductID  string
	Method     string
	TermMonths *int
	UpdatedAt  time.Time
}

// PutTemplate stores the template of a product, replacing the one it had.
func (s *Postgres) PutTemplate(ctx context.Context, t Template) (Template, error) {
	p, err := principal(ctx)
	if err != nil {
		return Template{}, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `insert into recognition_template (tenant_id, product_id, method, term_months) values ($1, $2, $3, $4)
			on conflict (tenant_id, product_id) do update set method = excluded.method, term_months = excluded.term_months, updated_at = now()
			returning updated_at`, p.TenantID, t.ProductID, t.Method, t.TermMonths).Scan(&t.UpdatedAt)
	})
	return t, err
}

// ListTemplates lists the tenant's templates by product.
func (s *Postgres) ListTemplates(ctx context.Context) ([]Template, error) {
	out := []Template{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select product_id::text, method, term_months, updated_at from recognition_template order by product_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t Template
			if err := rows.Scan(&t.ProductID, &t.Method, &t.TermMonths, &t.UpdatedAt); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// TemplatesFor returns the templates of the named products; a product with none is absent from the map.
func (s *Postgres) TemplatesFor(ctx context.Context, productIDs []string) (map[string]Template, error) {
	out := map[string]Template{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select product_id::text, method, term_months, updated_at from recognition_template where product_id::text = any($1::text[])`, productIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t Template
			if err := rows.Scan(&t.ProductID, &t.Method, &t.TermMonths, &t.UpdatedAt); err != nil {
				return err
			}
			out[t.ProductID] = t
		}
		return rows.Err()
	})
	return out, err
}
