package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
)

const (
	// CodeScenarioExists is 409 for a scenario code already in use.
	CodeScenarioExists = "PLAN-SCENARIO-EXISTS"
	// CodeScenarioNotFound is 404 for an unknown scenario code.
	CodeScenarioNotFound = "PLAN-SCENARIO-NOT-FOUND"
	// CodeScenarioLimit is 409 when a plan would have more than 10 active scenarios.
	CodeScenarioLimit = "PLAN-SCENARIO-LIMIT"
	// CodeScenarioInactive is 409 for a run of an inactive scenario.
	CodeScenarioInactive = "PLAN-SCENARIO-INACTIVE"
	// CodePlanNotFound is 404 for a scenario of a plan that has no operational
	// run yet or that the caller's legal-entity scope cannot see.
	CodePlanNotFound = "PLAN-NOT-FOUND"

	scenarioCodeConstraint        = "planning_scenario_code_key"
	scenarioIdempotencyConstraint = "planning_scenario_idempotency_key"
)

// Scenario is a stored planning scenario (FR-22.15). Version is its ETag.
type Scenario struct {
	ID      string
	Version int
	Spec    domain.ScenarioSpec
}

// ScenarioCreate is one create command; IdempotencyKey and InputHash are
// empty when the caller sent no Idempotency-Key.
type ScenarioCreate struct {
	IdempotencyKey string
	InputHash      string
	Spec           domain.ScenarioSpec
}

const scenarioSelect = `select id::text, version, code, plan_code, name, demand_factor::text, item_factors, added_demand, lead_time_overrides, active
	from planning_scenario`

func scanScenario(row pgx.Row) (Scenario, error) {
	var sc Scenario
	s := &sc.Spec
	var factor string
	var itemFactors, added, lead []byte
	if err := row.Scan(&sc.ID, &sc.Version, &s.Code, &s.PlanCode, &s.Name, &factor, &itemFactors, &added, &lead, &s.Active); err != nil {
		return sc, err
	}
	var err error
	if s.DemandFactor, err = decimal.NewFromString(factor); err != nil {
		return sc, err
	}
	for _, f := range []struct {
		raw []byte
		to  any
	}{{itemFactors, &s.ItemFactors}, {added, &s.AddedDemand}, {lead, &s.LeadTimeOverrides}} {
		if err := json.Unmarshal(f.raw, f.to); err != nil {
			return sc, err
		}
	}
	return sc, nil
}

func scenarioNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeScenarioNotFound, "planning scenario not found")
}

// scenarioJSON encodes the three line lists of a spec for the jsonb columns;
// a nil list is stored as [].
func scenarioJSON(s domain.ScenarioSpec) (itemFactors, added, lead []byte, err error) {
	if s.ItemFactors == nil {
		s.ItemFactors = []domain.ItemFactor{}
	}
	if s.AddedDemand == nil {
		s.AddedDemand = []domain.AddedDemand{}
	}
	if s.LeadTimeOverrides == nil {
		s.LeadTimeOverrides = []domain.LeadTimeOverride{}
	}
	if itemFactors, err = json.Marshal(s.ItemFactors); err != nil {
		return
	}
	if added, err = json.Marshal(s.AddedDemand); err != nil {
		return
	}
	lead, err = json.Marshal(s.LeadTimeOverrides)
	return
}

// ScenarioQuery narrows the scenario list.
type ScenarioQuery struct {
	PlanCode string
	Limit    int
}

// ListScenarios returns the tenant's scenarios ordered by plan and code.
func (s *Postgres) ListScenarios(ctx context.Context, q ScenarioQuery) ([]Scenario, error) {
	p, _ := tenant.FromContext(ctx)
	out := []Scenario{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, scenarioSelect+` where tenant_id = $1 and ($2::text = '' or plan_code = $2) order by plan_code, code limit $3`,
			p.TenantID, q.PlanCode, q.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			sc, err := scanScenario(rows)
			if err != nil {
				return err
			}
			out = append(out, sc)
		}
		return rows.Err()
	})
	return out, err
}

// GetScenario returns one scenario by code.
func (s *Postgres) GetScenario(ctx context.Context, code string) (Scenario, error) {
	p, _ := tenant.FromContext(ctx)
	var sc Scenario
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		sc, err = scanScenario(tx.QueryRow(ctx, scenarioSelect+` where tenant_id = $1 and code = $2`, p.TenantID, code))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Scenario{}, scenarioNotFound()
	}
	return sc, err
}

// checkActiveLimit refuses an active scenario that would be the 11th active
// one of its plan (FR-22.15). The advisory lock serialises scenario writes of
// one plan, so two concurrent creates cannot both pass the count.
func checkActiveLimit(ctx context.Context, tx pgx.Tx, tenantID, planID, planCode, exceptID string) error {
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 22))`, tenantID+"/scenario/"+planID); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRow(ctx, `select count(*) from planning_scenario where tenant_id = $1 and plan_id = $2 and active and ($3 = '' or id::text <> $3)`,
		tenantID, planID, exceptID).Scan(&n); err != nil {
		return err
	}
	if n >= domain.MaxActiveScenarios {
		return httpx.NewProblem(http.StatusConflict, CodeScenarioLimit, "plan "+planCode+" already has 10 active scenarios; deactivate one first")
	}
	return nil
}

// scenarioPlan returns the id and owning legal entity of the plan a new
// scenario belongs to. A plan that has never run, or that the caller's
// legal-entity scope cannot see, is 404 PLAN-NOT-FOUND.
func scenarioPlan(ctx context.Context, tx pgx.Tx, tenantID, planCode string) (string, string, error) {
	var id, entity string
	err := tx.QueryRow(ctx, `select id::text, coalesce(legal_entity_id::text, '') from plan where tenant_id = $1 and code = $2`, tenantID, planCode).Scan(&id, &entity)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", httpx.NewProblem(http.StatusNotFound, CodePlanNotFound, "plan "+planCode+" has no operational run yet; run it once before adding scenarios")
	}
	return id, entity, err
}

// CreateScenario stores a validated spec at version 1 under the plan it names,
// owned by that plan's legal entity; the plan must exist and be visible to the
// caller (404 PLAN-NOT-FOUND). A code in use is 409
// PLAN-SCENARIO-EXISTS; an 11th active scenario of a plan is 409
// PLAN-SCENARIO-LIMIT. With an Idempotency-Key, a retry of the same body
// returns the scenario of the first call (replayed true) and another body
// under the same key is 422.
func (s *Postgres) CreateScenario(ctx context.Context, req ScenarioCreate) (Scenario, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out Scenario
	replayed := false
	var key, hash *string
	if req.IdempotencyKey != "" {
		key, hash = &req.IdempotencyKey, &req.InputHash
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			var stored string
			err := tx.QueryRow(ctx, `select input_hash from planning_scenario where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key).Scan(&stored)
			if err == nil {
				if stored != *hash {
					return idempotencyReuse()
				}
				sc, err := scanScenario(tx.QueryRow(ctx, scenarioSelect+` where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
				out, replayed = sc, true
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		sp := req.Spec
		planID, entity, err := scenarioPlan(ctx, tx, p.TenantID, sp.PlanCode)
		if err != nil {
			return err
		}
		if sp.Active {
			if err := checkActiveLimit(ctx, tx, p.TenantID, planID, sp.PlanCode, ""); err != nil {
				return err
			}
		}
		itemFactors, added, lead, err := scenarioJSON(sp)
		if err != nil {
			return err
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into planning_scenario (tenant_id, id, plan_id, code, plan_code, legal_entity_id, name, demand_factor, item_factors,
			added_demand, lead_time_overrides, active, idempotency_key, input_hash)
			values ($1, $2, $3, $4, $5, nullif($6, '')::uuid, $7, $8::numeric, $9, $10, $11, $12, $13, $14)`,
			p.TenantID, id, planID, sp.Code, sp.PlanCode, entity, sp.Name, sp.DemandFactor.String(), itemFactors, added, lead, sp.Active, key, hash); err != nil {
			return err
		}
		out = Scenario{ID: id, Version: 1, Spec: sp}
		return nil
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		switch pgErr.ConstraintName {
		case scenarioCodeConstraint:
			return Scenario{}, false, httpx.NewProblem(http.StatusConflict, CodeScenarioExists, "a scenario with code "+req.Spec.Code+" already exists")
		case scenarioIdempotencyConstraint:
			return Scenario{}, false, idempotencyReuse()
		}
	}
	return out, replayed, err
}

// UpdateScenario replaces the settings of the scenario spec.Code under
// If-Match: expectedVersion must be its current version (412 otherwise). The
// plan a scenario belongs to cannot change.
func (s *Postgres) UpdateScenario(ctx context.Context, expectedVersion int, spec domain.ScenarioSpec) (Scenario, error) {
	p, _ := tenant.FromContext(ctx)
	var out Scenario
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id, planID, planCode string
		var version int
		var active bool
		err := tx.QueryRow(ctx, `select id::text, version, plan_id::text, plan_code, active from planning_scenario where tenant_id = $1 and code = $2 for update`, p.TenantID, spec.Code).
			Scan(&id, &version, &planID, &planCode, &active)
		if errors.Is(err, pgx.ErrNoRows) {
			return scenarioNotFound()
		}
		if err != nil {
			return err
		}
		if version != expectedVersion {
			return versionConflict("scenario", version)
		}
		if spec.PlanCode != planCode {
			return httpx.NewProblem(http.StatusUnprocessableEntity, codeScenarioPlan, "the plan of a scenario cannot be changed")
		}
		if spec.Active && !active {
			if err := checkActiveLimit(ctx, tx, p.TenantID, planID, planCode, id); err != nil {
				return err
			}
		}
		itemFactors, added, lead, err := scenarioJSON(spec)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update planning_scenario set name = $3, demand_factor = $4::numeric, item_factors = $5, added_demand = $6,
			lead_time_overrides = $7, active = $8, version = version + 1, updated_at = now() where tenant_id = $1 and id = $2`,
			p.TenantID, id, spec.Name, spec.DemandFactor.String(), itemFactors, added, lead, spec.Active); err != nil {
			return err
		}
		out = Scenario{ID: id, Version: version + 1, Spec: spec}
		return nil
	})
	return out, err
}

const codeScenarioPlan = "PLAN-SCENARIO-PLAN"
