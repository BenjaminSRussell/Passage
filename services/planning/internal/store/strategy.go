package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
)

const (
	// CodeStrategyExists is returned when a strategy already has stored setup.
	CodeStrategyExists = "PLAN-STRATEGY-EXISTS"
	// CodeStrategyNotFound is returned for a strategy code with no stored setup.
	CodeStrategyNotFound = "PLAN-STRATEGY-NOT-FOUND"

	strategyCodeConstraint        = "planning_strategy_code_key"
	strategyIdempotencyConstraint = "planning_strategy_idempotency_key"
)

// Strategy is stored planning strategy setup (FR-22.19). Version is its ETag.
type Strategy struct {
	ID      string
	Version int
	Spec    domain.StrategySpec
}

// StrategyCreate is one create command; IdempotencyKey and InputHash are empty
// when the caller sent no Idempotency-Key.
type StrategyCreate struct {
	IdempotencyKey string
	InputHash      string
	Spec           domain.StrategySpec
}

const strategySelect = `select id::text, version, code, consumption_mode, backward_days, forward_days, uses_marking, segregated_stock from planning_strategy`

func scanStrategy(row pgx.Row) (Strategy, error) {
	var st Strategy
	s := &st.Spec
	err := row.Scan(&st.ID, &st.Version, &s.Code, &s.ConsumptionMode, &s.BackwardDays, &s.ForwardDays, &s.UsesMarking, &s.SegregatedStock)
	return st, err
}

func strategyNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeStrategyNotFound, "planning strategy has no stored setup")
}

// ListStrategies returns the stored strategy setup of the tenant ordered by code.
func (s *Postgres) ListStrategies(ctx context.Context) ([]Strategy, error) {
	p, _ := tenant.FromContext(ctx)
	out := []Strategy{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, strategySelect+` where tenant_id = $1 order by code`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			st, err := scanStrategy(rows)
			if err != nil {
				return err
			}
			out = append(out, st)
		}
		return rows.Err()
	})
	return out, err
}

// GetStrategy returns the stored setup of one strategy code.
func (s *Postgres) GetStrategy(ctx context.Context, code string) (Strategy, error) {
	p, _ := tenant.FromContext(ctx)
	var st Strategy
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		st, err = scanStrategy(tx.QueryRow(ctx, strategySelect+` where tenant_id = $1 and code = $2`, p.TenantID, code))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Strategy{}, strategyNotFound()
	}
	return st, err
}

// CreateStrategy stores the setup of a strategy at version 1. A strategy that
// already has setup is 409 PLAN-STRATEGY-EXISTS. With an Idempotency-Key, a
// retry of the same body returns the first result (replayed true) and another
// body under the same key is 422.
func (s *Postgres) CreateStrategy(ctx context.Context, req StrategyCreate) (Strategy, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out Strategy
	replayed := false
	var key, hash *string
	if req.IdempotencyKey != "" {
		key, hash = &req.IdempotencyKey, &req.InputHash
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			var stored string
			err := tx.QueryRow(ctx, `select input_hash from planning_strategy where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key).Scan(&stored)
			if err == nil {
				if stored != *hash {
					return idempotencyReuse()
				}
				st, err := scanStrategy(tx.QueryRow(ctx, strategySelect+` where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
				out, replayed = st, true
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		g := req.Spec
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into planning_strategy (tenant_id, id, code, consumption_mode, backward_days, forward_days,
			uses_marking, segregated_stock, idempotency_key, input_hash) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			p.TenantID, id, g.Code, g.ConsumptionMode, g.BackwardDays, g.ForwardDays, g.UsesMarking, g.SegregatedStock, key, hash); err != nil {
			return err
		}
		out = Strategy{ID: id, Version: 1, Spec: g}
		return nil
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		switch pgErr.ConstraintName {
		case strategyCodeConstraint:
			return Strategy{}, false, httpx.NewProblem(http.StatusConflict, CodeStrategyExists, "strategy "+req.Spec.Code+" already has setup")
		case strategyIdempotencyConstraint:
			return Strategy{}, false, idempotencyReuse()
		}
	}
	return out, replayed, err
}

// UpdateStrategy replaces the setup of spec.Code under If-Match: expectedVersion
// must be its current version (412 otherwise), and the version is bumped.
func (s *Postgres) UpdateStrategy(ctx context.Context, expectedVersion int, spec domain.StrategySpec) (Strategy, error) {
	p, _ := tenant.FromContext(ctx)
	var out Strategy
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		var version int
		err := tx.QueryRow(ctx, `select id::text, version from planning_strategy where tenant_id = $1 and code = $2 for update`, p.TenantID, spec.Code).
			Scan(&id, &version)
		if errors.Is(err, pgx.ErrNoRows) {
			return strategyNotFound()
		}
		if err != nil {
			return err
		}
		if version != expectedVersion {
			return versionConflict("planning strategy", version)
		}
		if _, err := tx.Exec(ctx, `update planning_strategy set consumption_mode = $3, backward_days = $4, forward_days = $5,
			uses_marking = $6, segregated_stock = $7, version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`,
			p.TenantID, id, spec.ConsumptionMode, spec.BackwardDays, spec.ForwardDays, spec.UsesMarking, spec.SegregatedStock); err != nil {
			return err
		}
		out = Strategy{ID: id, Version: version + 1, Spec: spec}
		return nil
	})
	return out, err
}

// RunStrategy is what a plan run reads for one item: the strategy on the item's
// site-scope coverage with its setup, or the PRD defaults when none is stored.
type RunStrategy struct {
	ItemID string
	Spec   domain.StrategySpec
}

// RunStrategies returns the planning strategy of each of the given items at
// siteID, read from the site-scope item coverage (FR-22.19), with the stored
// setup of that strategy or the PRD defaults when none is stored. Items
// without stored coverage are absent: a run treats them as mts-net, as before
// strategies existed. With planEntity set,
// only coverage of that legal entity is read. itemIDs must be UUIDs.
func (s *Postgres) RunStrategies(ctx context.Context, siteID, planEntity string, itemIDs []string) (map[string]domain.StrategySpec, error) {
	out := map[string]domain.StrategySpec{}
	if len(itemIDs) == 0 {
		return out, nil
	}
	p, _ := tenant.FromContext(ctx)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select c.item_id::text, c.planning_strategy_code, s.id is not null, coalesce(s.consumption_mode, 'backward'),
				coalesce(s.backward_days, 30), coalesce(s.forward_days, 0), coalesce(s.uses_marking, false), coalesce(s.segregated_stock, false)
			from item_coverage c
			left join planning_strategy s on s.tenant_id = c.tenant_id and s.code = c.planning_strategy_code
			where c.tenant_id = $1 and c.site_id = $2::uuid and c.planning_scope = 'site' and c.item_id = any($3::uuid[])
			  and ($4::text = '' or c.legal_entity_id::text = $4::text)`, p.TenantID, siteID, itemIDs, planEntity)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var stored bool
			var st domain.StrategySpec
			if err := rows.Scan(&id, &st.Code, &stored, &st.ConsumptionMode, &st.BackwardDays, &st.ForwardDays, &st.UsesMarking, &st.SegregatedStock); err != nil {
				return err
			}
			if !stored {
				st = domain.DefaultStrategy(st.Code)
			}
			out[id] = st
		}
		return rows.Err()
	})
	return out, err
}
