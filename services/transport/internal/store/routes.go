package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
)

const routePlanSummaryColumns = `id, request_hash, stop_count, vehicle_count, route_count, dropped_count,
	total_distance_km::float8, time_limit_s, created_by, created_at`

const routePlanColumns = routePlanSummaryColumns + `, request, solution`

func scanRoutePlanSummary(row pgx.Row, extra ...any) (domain.RoutePlanSummary, error) {
	var p domain.RoutePlanSummary
	dest := append([]any{&p.ID, &p.RequestHash, &p.StopCount, &p.VehicleCount, &p.RouteCount, &p.DroppedCount,
		&p.TotalDistanceKm, &p.TimeLimitS, &p.CreatedBy, &p.CreatedAt}, extra...)
	err := row.Scan(dest...)
	return p, err
}

func scanRoutePlan(row pgx.Row) (domain.RoutePlan, error) {
	var request, solution []byte
	sum, err := scanRoutePlanSummary(row, &request, &solution)
	if err != nil {
		return domain.RoutePlan{}, err
	}
	plan := domain.RoutePlan{RoutePlanSummary: sum}
	if err := json.Unmarshal(request, &plan.Request); err != nil {
		return domain.RoutePlan{}, err
	}
	if err := json.Unmarshal(solution, &plan.Solution); err != nil {
		return domain.RoutePlan{}, err
	}
	return plan, nil
}

// FindRoutePlan returns the plan stored for a request hash, if any. The hash is
// the idempotency key of route optimization (§6.34.5).
func (s *Postgres) FindRoutePlan(ctx context.Context, hash string) (domain.RoutePlan, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var plan domain.RoutePlan
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		plan, err = scanRoutePlan(tx.QueryRow(ctx, `select `+routePlanColumns+` from route_plan
			where tenant_id = $1 and request_hash = $2`, p.TenantID, hash))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RoutePlan{}, false, nil
	}
	return plan, err == nil, err
}

// SaveRoutePlan stores a solved plan. When a concurrent request with the same
// hash committed first, that plan is returned and created is false.
func (s *Postgres) SaveRoutePlan(ctx context.Context, plan domain.RoutePlan) (domain.RoutePlan, bool, error) {
	p, _ := tenant.FromContext(ctx)
	request, err := json.Marshal(plan.Request)
	if err != nil {
		return domain.RoutePlan{}, false, err
	}
	solution, err := json.Marshal(plan.Solution)
	if err != nil {
		return domain.RoutePlan{}, false, err
	}
	var out domain.RoutePlan
	created := true
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanRoutePlan(tx.QueryRow(ctx, `insert into route_plan (tenant_id, id, request_hash, request, solution,
			stop_count, vehicle_count, route_count, dropped_count, total_distance_km, time_limit_s, created_by)
			values ($1, $2, $3, $4::jsonb, $5::jsonb, $6, $7, $8, $9, $10, $11, $12)
			on conflict (tenant_id, request_hash) do nothing
			returning `+routePlanColumns,
			p.TenantID, ids.New(), plan.RequestHash, string(request), string(solution), plan.StopCount, plan.VehicleCount,
			plan.RouteCount, plan.DroppedCount, plan.TotalDistanceKm, plan.TimeLimitS, p.UserID))
		if errors.Is(err, pgx.ErrNoRows) {
			created = false
			out, err = scanRoutePlan(tx.QueryRow(ctx, `select `+routePlanColumns+` from route_plan
				where tenant_id = $1 and request_hash = $2`, p.TenantID, plan.RequestHash))
		}
		return err
	})
	return out, created, err
}

// GetRoutePlan returns one plan with its request and routes.
func (s *Postgres) GetRoutePlan(ctx context.Context, id string) (domain.RoutePlan, error) {
	p, _ := tenant.FromContext(ctx)
	var plan domain.RoutePlan
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		plan, err = scanRoutePlan(tx.QueryRow(ctx, `select `+routePlanColumns+` from route_plan
			where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RoutePlan{}, NotFound("route plan")
	}
	return plan, err
}

// ListRoutePlans returns the newest plans first, at most ListLimit, without
// their request and routes.
func (s *Postgres) ListRoutePlans(ctx context.Context) ([]domain.RoutePlanSummary, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.RoutePlanSummary{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+routePlanSummaryColumns+` from route_plan
			where tenant_id = $1 order by created_at desc, id desc limit $2`, p.TenantID, ListLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			sum, err := scanRoutePlanSummary(rows)
			if err != nil {
				return err
			}
			out = append(out, sum)
		}
		return rows.Err()
	})
	return out, err
}
