package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	mfevents "github.com/BenjaminSRussell/passage/tools/migration-factory/internal/events"
)

// CreatePlan stores a generated plan and its steps.
func (s *Postgres) CreatePlan(ctx context.Context, idem Idem, plan domain.Plan, createdBy string) (domain.Plan, bool, error) {
	var out domain.Plan
	var replayed bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, idem, http.StatusCreated, func() (domain.Plan, error) {
			packs := make([]string, len(plan.Packs))
			for i, p := range plan.Packs {
				packs[i] = string(p)
			}
			if err := tx.QueryRow(ctx, `insert into cutover_plan (tenant_id, plan_id, t0, packs, entities, abort_offset_min, version, created_by)
				values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6, $7) returning created_at`,
				plan.PlanID, plan.T0, packs, plan.Entities, plan.AbortOffsetMin, plan.Version, createdBy).Scan(&plan.CreatedAt); err != nil {
				return domain.Plan{}, fmt.Errorf("insert cutover_plan: %w", err)
			}
			for _, st := range plan.Steps {
				if _, err := tx.Exec(ctx, `insert into cutover_step (tenant_id, plan_id, seq, offset_minutes, name, owner, planned_duration_min,
					actual_duration_min, status, go_nogo, pack)
					values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
					plan.PlanID, st.Seq, st.OffsetMin, st.Name, st.Owner, st.PlannedDurationMin, st.ActualDurationMin, st.Status, st.GoNoGo, st.Pack); err != nil {
					return domain.Plan{}, fmt.Errorf("insert cutover_step: %w", err)
				}
			}
			return plan, nil
		})
		return err
	})
	return out, replayed, err
}

const planColumns = `plan_id::text, t0, packs, entities, abort_offset_min, rolled_back, rollback_approvers, version, created_at`

func scanPlan(row pgx.Row) (domain.Plan, error) {
	var p domain.Plan
	var packs []string
	if err := row.Scan(&p.PlanID, &p.T0, &packs, &p.Entities, &p.AbortOffsetMin, &p.RolledBack, &p.Approvers, &p.Version, &p.CreatedAt); err != nil {
		return domain.Plan{}, err
	}
	for _, pk := range packs {
		p.Packs = append(p.Packs, domain.Pack(pk))
	}
	return p, nil
}

// loadSteps fills the steps of every plan in plans, ordered by seq.
func loadSteps(ctx context.Context, tx pgx.Tx, plans []*domain.Plan) error {
	if len(plans) == 0 {
		return nil
	}
	byID := make(map[string]*domain.Plan, len(plans))
	planIDs := make([]string, 0, len(plans))
	for _, p := range plans {
		byID[p.PlanID] = p
		planIDs = append(planIDs, p.PlanID)
	}
	rows, err := tx.Query(ctx, `select plan_id::text, seq, offset_minutes, name, owner, planned_duration_min, actual_duration_min, status,
		go_nogo, pack, completed_by, completed_at
		from cutover_step where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = any($1::uuid[]) order by plan_id, seq`, planIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var planID string
		var st domain.Step
		if err := rows.Scan(&planID, &st.Seq, &st.OffsetMin, &st.Name, &st.Owner, &st.PlannedDurationMin, &st.ActualDurationMin, &st.Status,
			&st.GoNoGo, &st.Pack, &st.CompletedBy, &st.CompletedAt); err != nil {
			return err
		}
		byID[planID].Steps = append(byID[planID].Steps, st)
	}
	return rows.Err()
}

func getPlan(ctx context.Context, tx pgx.Tx, id string, forUpdate bool) (domain.Plan, error) {
	q := `select ` + planColumns + ` from cutover_plan where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1`
	if forUpdate {
		q += ` for update`
	}
	p, err := scanPlan(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Plan{}, ErrNotFound
	}
	if err != nil {
		return domain.Plan{}, err
	}
	if err := loadSteps(ctx, tx, []*domain.Plan{&p}); err != nil {
		return domain.Plan{}, err
	}
	return p, nil
}

// GetPlan returns a plan with its steps.
func (s *Postgres) GetPlan(ctx context.Context, id string) (domain.Plan, error) {
	var out domain.Plan
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = getPlan(ctx, tx, id, false)
		return err
	})
	return out, err
}

// PlanFilter pages ListPlans. After is the plan id of the last row of the previous page.
type PlanFilter struct {
	After string
	Limit int
}

// ListPlans returns plans newest first, each with its steps.
func (s *Postgres) ListPlans(ctx context.Context, f PlanFilter) ([]domain.Plan, error) {
	out := []domain.Plan{}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var after *time.Time
		var afterID *string
		if f.After != "" {
			var at time.Time
			err := tx.QueryRow(ctx, `select created_at from cutover_plan where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1`, f.After).Scan(&at)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			after, afterID = &at, &f.After
		}
		rows, err := tx.Query(ctx, `select `+planColumns+` from cutover_plan
			where tenant_id = current_setting('passage.tenant_id', true)::uuid and ($1::timestamptz is null or (created_at, plan_id) < ($1, $2::uuid))
			order by created_at desc, plan_id desc limit $3`, after, afterID, clampLimit(f.Limit))
		if err != nil {
			return err
		}
		for rows.Next() {
			p, err := scanPlan(rows)
			if err != nil {
				rows.Close()
				return err
			}
			out = append(out, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		ptrs := make([]*domain.Plan, len(out))
		for i := range out {
			ptrs[i] = &out[i]
		}
		return loadSteps(ctx, tx, ptrs)
	})
	return out, err
}

// StepCompletion is a request to mark one runbook step done. ExpectedVersion 0 skips the
// optimistic version check.
type StepCompletion struct {
	PlanID            string
	Seq               int
	ActualDurationMin int
	ExpectedVersion   int
	By                string
}

// CompleteStep marks the step done under the plan's row lock, checks the expected version,
// applies the go/no-go sequencing rules and writes cutover.step.completed.
func (s *Postgres) CompleteStep(ctx context.Context, idem Idem, c StepCompletion) (domain.Plan, bool, error) {
	var out domain.Plan
	var replayed bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, idem, http.StatusOK, func() (domain.Plan, error) {
			plan, err := getPlan(ctx, tx, c.PlanID, true)
			if err != nil {
				return domain.Plan{}, err
			}
			if c.ExpectedVersion != 0 && c.ExpectedVersion != plan.Version {
				return domain.Plan{}, fmt.Errorf("%w: plan is at version %d, request expected %d", ErrVersion, plan.Version, c.ExpectedVersion)
			}
			if err := plan.Complete(c.Seq, c.ActualDurationMin); err != nil {
				return domain.Plan{}, err
			}
			var step *domain.Step
			for i := range plan.Steps {
				if plan.Steps[i].Seq == c.Seq {
					step = &plan.Steps[i]
				}
			}
			now := time.Now().UTC()
			step.CompletedBy, step.CompletedAt = c.By, &now
			if _, err := tx.Exec(ctx, `update cutover_step set status = $3, actual_duration_min = $4, completed_by = $5, completed_at = $6
				where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1 and seq = $2`, plan.PlanID, step.Seq, step.Status, step.ActualDurationMin, c.By, now); err != nil {
				return domain.Plan{}, fmt.Errorf("update cutover_step: %w", err)
			}
			if _, err := tx.Exec(ctx, `update cutover_plan set version = $2 where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1`, plan.PlanID, plan.Version); err != nil {
				return domain.Plan{}, fmt.Errorf("update cutover_plan: %w", err)
			}
			if err := enqueue(ctx, tx, mfevents.CutoverStepCompleted, "cutover_plan", plan.PlanID, mfevents.CutoverStepCompletedData{
				PlanID: plan.PlanID, StepID: step.Seq, Name: step.Name, ActualDurationMin: step.ActualDurationMin, Status: step.Status, CompletedBy: c.By,
			}); err != nil {
				return domain.Plan{}, fmt.Errorf("enqueue cutover.step.completed: %w", err)
			}
			return plan, nil
		})
		return err
	})
	return out, replayed, err
}

// RollbackRequest executes a plan's rollback with two distinct approvers.
type RollbackRequest struct {
	PlanID          string
	Approvers       []string
	ExpectedVersion int
	By              string
}

// Rollback records the rollback under the plan's row lock and writes cutover.plan.rolledback.
func (s *Postgres) Rollback(ctx context.Context, idem Idem, r RollbackRequest) (domain.Plan, bool, error) {
	var out domain.Plan
	var replayed bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, idem, http.StatusOK, func() (domain.Plan, error) {
			plan, err := getPlan(ctx, tx, r.PlanID, true)
			if err != nil {
				return domain.Plan{}, err
			}
			if r.ExpectedVersion != 0 && r.ExpectedVersion != plan.Version {
				return domain.Plan{}, fmt.Errorf("%w: plan is at version %d, request expected %d", ErrVersion, plan.Version, r.ExpectedVersion)
			}
			if err := plan.Rollback(r.Approvers...); err != nil {
				return domain.Plan{}, err
			}
			if _, err := tx.Exec(ctx, `update cutover_plan set rolled_back = true, rollback_approvers = $2, rolled_back_by = $3,
				rolled_back_at = now(), version = $4 where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1`, plan.PlanID, plan.Approvers, r.By, plan.Version); err != nil {
				return domain.Plan{}, fmt.Errorf("update cutover_plan: %w", err)
			}
			if err := enqueue(ctx, tx, mfevents.CutoverPlanRolledBack, "cutover_plan", plan.PlanID, mfevents.CutoverPlanRolledBackData{
				PlanID: plan.PlanID, Approvers: plan.Approvers, ExecutedBy: r.By,
			}); err != nil {
				return domain.Plan{}, fmt.Errorf("enqueue cutover.plan.rolledback: %w", err)
			}
			return plan, nil
		})
		return err
	})
	return out, replayed, err
}
