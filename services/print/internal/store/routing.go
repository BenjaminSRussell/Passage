package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

const routingColumns = "id::text, document_type, coalesce(user_id, ''), coalesce(zone, ''), coalesce(warehouse_id, ''), queue_code, sequence"

func scanRule(row pgx.Row) (domain.RoutingRule, error) {
	var r domain.RoutingRule
	err := row.Scan(&r.ID, &r.DocumentType, &r.UserID, &r.Zone, &r.WarehouseID, &r.QueueCode, &r.Sequence)
	return r, err
}

// CreateRoutingRule stores a rule. A repeated Idempotency-Key returns the stored rule
// (inserted=false); a different body under that key is refused.
func (s *Postgres) CreateRoutingRule(ctx context.Context, in domain.RoutingRule, idempotencyKey string) (domain.RoutingRule, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.RoutingRule
	var inserted bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanRule(tx.QueryRow(ctx, `insert into print_routing_rule (tenant_id, id, document_type, user_id, zone, warehouse_id, queue_code, sequence, idempotency_key)
			values ($1, $2, $3, nullif($4, ''), nullif($5, ''), nullif($6, ''), $7, $8, $9)
			on conflict (tenant_id, idempotency_key) do nothing
			returning `+routingColumns, p.TenantID, ids.New(), in.DocumentType, in.UserID, in.Zone, in.WarehouseID, in.QueueCode, in.Sequence, idempotencyKey))
		if err == nil {
			inserted = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		out, err = scanRule(tx.QueryRow(ctx, `select `+routingColumns+` from print_routing_rule where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idempotencyKey))
		return err
	})
	if err != nil {
		return domain.RoutingRule{}, false, err
	}
	if !inserted && (out.DocumentType != in.DocumentType || out.UserID != in.UserID || out.Zone != in.Zone ||
		out.WarehouseID != in.WarehouseID || out.QueueCode != in.QueueCode || out.Sequence != in.Sequence) {
		return domain.RoutingRule{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return out, inserted, nil
}

// ListRoutingRules returns every rule of the tenant in resolution order (sequence).
func (s *Postgres) ListRoutingRules(ctx context.Context) ([]domain.RoutingRule, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.RoutingRule{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+routingColumns+` from print_routing_rule where tenant_id = $1 order by sequence, id`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRule(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
