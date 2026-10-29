package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

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

const planColumns = `plan_id::text, t0, packs, entities, abort_offset_min, rolled_back, rollback_approvers,
	rekey_journal_lines, rekey_document_lines, rekey_excluded, version, created_at`

func scanPlan(row pgx.Row) (domain.Plan, error) {
	var p domain.Plan
	var packs []string
	if err := row.Scan(&p.PlanID, &p.T0, &packs, &p.Entities, &p.AbortOffsetMin, &p.RolledBack, &p.Approvers,
		&p.Rekey.JournalLines, &p.Rekey.DocumentLines, &p.Rekey.Excluded, &p.Version, &p.CreatedAt); err != nil {
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
		go_nogo, pack, completed_by, completed_at, started_at, started_by
		from cutover_step where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = any($1::uuid[]) order by plan_id, seq`, planIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var planID string
		var st domain.Step
		if err := rows.Scan(&planID, &st.Seq, &st.OffsetMin, &st.Name, &st.Owner, &st.PlannedDurationMin, &st.ActualDurationMin, &st.Status,
			&st.GoNoGo, &st.Pack, &st.CompletedBy, &st.CompletedAt, &st.StartedAt, &st.StartedBy); err != nil {
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
// optimistic version check. A nil ActualDurationMin measures the duration from the step's start.
type StepCompletion struct {
	PlanID            string
	Seq               int
	ActualDurationMin *int
	ExpectedVersion   int
	By                string
}

// StepStart is a request to start one runbook step. ExpectedVersion 0 skips the version check.
type StepStart struct {
	PlanID          string
	Seq             int
	ExpectedVersion int
	By              string
}

// lockPlan reads the plan under its row lock and checks the caller's expected version.
func lockPlan(ctx context.Context, tx pgx.Tx, planID string, expected int) (domain.Plan, error) {
	plan, err := getPlan(ctx, tx, planID, true)
	if err != nil {
		return domain.Plan{}, err
	}
	if expected != 0 && expected != plan.Version {
		return domain.Plan{}, fmt.Errorf("%w: plan is at version %d, request expected %d", ErrVersion, plan.Version, expected)
	}
	return plan, nil
}

func stepAt(plan *domain.Plan, seq int) *domain.Step {
	for i := range plan.Steps {
		if plan.Steps[i].Seq == seq {
			return &plan.Steps[i]
		}
	}
	return nil
}

func savePlanVersion(ctx context.Context, tx pgx.Tx, plan domain.Plan) error {
	if _, err := tx.Exec(ctx, `update cutover_plan set version = $2 where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1`, plan.PlanID, plan.Version); err != nil {
		return fmt.Errorf("update cutover_plan: %w", err)
	}
	return nil
}

// StartStep records the start of a runbook step under the plan's row lock (FR-49.6): the timer
// on the runbook runs from it. The go/no-go gate applies as for completion (CUT-1601).
func (s *Postgres) StartStep(ctx context.Context, idem Idem, st StepStart) (domain.Plan, bool, error) {
	var out domain.Plan
	var replayed bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, idem, http.StatusOK, func() (domain.Plan, error) {
			plan, err := lockPlan(ctx, tx, st.PlanID, st.ExpectedVersion)
			if err != nil {
				return domain.Plan{}, err
			}
			if err := plan.Start(st.Seq, time.Now().UTC(), st.By); err != nil {
				return domain.Plan{}, err
			}
			step := stepAt(&plan, st.Seq)
			if _, err := tx.Exec(ctx, `update cutover_step set started_at = $3, started_by = $4
				where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1 and seq = $2`, plan.PlanID, step.Seq, step.StartedAt, step.StartedBy); err != nil {
				return domain.Plan{}, fmt.Errorf("update cutover_step: %w", err)
			}
			return plan, savePlanVersion(ctx, tx, plan)
		})
		return err
	})
	return out, replayed, err
}

// CompleteStep marks the step done under the plan's row lock, checks the expected version,
// applies the go/no-go sequencing rules and writes cutover.step.completed.
func (s *Postgres) CompleteStep(ctx context.Context, idem Idem, c StepCompletion) (domain.Plan, bool, error) {
	var out domain.Plan
	var replayed bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, idem, http.StatusOK, func() (domain.Plan, error) {
			plan, err := lockPlan(ctx, tx, c.PlanID, c.ExpectedVersion)
			if err != nil {
				return domain.Plan{}, err
			}
			now := time.Now().UTC()
			var actual int
			if c.ActualDurationMin != nil {
				actual = *c.ActualDurationMin
			} else if actual, err = plan.MeasuredDuration(c.Seq, now); err != nil {
				return domain.Plan{}, err
			}
			if err := plan.Complete(c.Seq, actual); err != nil {
				return domain.Plan{}, err
			}
			step := stepAt(&plan, c.Seq)
			step.CompletedBy, step.CompletedAt = c.By, &now
			if _, err := tx.Exec(ctx, `update cutover_step set status = $3, actual_duration_min = $4, completed_by = $5, completed_at = $6
				where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1 and seq = $2`, plan.PlanID, step.Seq, step.Status, step.ActualDurationMin, c.By, now); err != nil {
				return domain.Plan{}, fmt.Errorf("update cutover_step: %w", err)
			}
			if err := savePlanVersion(ctx, tx, plan); err != nil {
				return domain.Plan{}, err
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

// RollbackRequest executes a plan's rollback with two distinct approvers. Postings are the
// Passage postings since go-live to export for re-keying (FR-49.9).
type RollbackRequest struct {
	PlanID          string
	Approvers       []string
	Postings        []domain.RekeyPosting
	ExpectedVersion int
	By              string
}

// Rollback records the rollback under the plan's row lock, stores the re-keying lines built
// from the postings and writes cutover.plan.rolledback, all in one transaction: a posting the
// export rejects leaves the plan as it was.
func (s *Postgres) Rollback(ctx context.Context, idem Idem, r RollbackRequest) (domain.Plan, bool, error) {
	var out domain.Plan
	var replayed bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, idem, http.StatusOK, func() (domain.Plan, error) {
			plan, err := lockPlan(ctx, tx, r.PlanID, r.ExpectedVersion)
			if err != nil {
				return domain.Plan{}, err
			}
			if err := plan.Rollback(r.Approvers...); err != nil {
				return domain.Plan{}, err
			}
			export, err := domain.BuildRekeyExport(plan, r.Postings)
			if err != nil {
				return domain.Plan{}, err
			}
			plan.Rekey = export.Summary()
			if err := insertRekeyLines(ctx, tx, plan.PlanID, domain.KindJournal, export.Journals); err != nil {
				return domain.Plan{}, err
			}
			if err := insertRekeyLines(ctx, tx, plan.PlanID, domain.KindDocument, export.Documents); err != nil {
				return domain.Plan{}, err
			}
			if _, err := tx.Exec(ctx, `update cutover_plan set rolled_back = true, rollback_approvers = $2, rolled_back_by = $3,
				rolled_back_at = now(), version = $4, rekey_journal_lines = $5, rekey_document_lines = $6, rekey_excluded = $7
				where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1`,
				plan.PlanID, plan.Approvers, r.By, plan.Version, plan.Rekey.JournalLines, plan.Rekey.DocumentLines, plan.Rekey.Excluded); err != nil {
				return domain.Plan{}, fmt.Errorf("update cutover_plan: %w", err)
			}
			if err := enqueue(ctx, tx, mfevents.CutoverPlanRolledBack, "cutover_plan", plan.PlanID, mfevents.CutoverPlanRolledBackData{
				PlanID: plan.PlanID, Approvers: plan.Approvers, ExecutedBy: r.By,
				RekeyJournalLines: plan.Rekey.JournalLines, RekeyDocumentLines: plan.Rekey.DocumentLines,
			}); err != nil {
				return domain.Plan{}, fmt.Errorf("enqueue cutover.plan.rolledback: %w", err)
			}
			return plan, nil
		})
		return err
	})
	return out, replayed, err
}

// insertRekeyLines stores one re-keying file's lines in file order (seq from 1).
func insertRekeyLines(ctx context.Context, tx pgx.Tx, planID string, kind domain.PostingKind, lines []domain.RekeyPosting) error {
	if len(lines) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for i, l := range lines {
		batch.Queue(`insert into rekey_line (tenant_id, plan_id, kind, seq, entity, document_ref, line, posted_at, account, dimensions, currency, amount, text)
			values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			planID, string(kind), i+1, l.Entity, l.DocumentRef, l.Line, l.PostedAt, l.Account, l.Dimensions, l.Currency, l.Amount.String(), l.Text)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("insert rekey_line: %w", err)
	}
	return nil
}

// RekeyLines returns one re-keying file of a rolled-back plan in file order. A plan that has
// not been rolled back has no files (domain.ErrTransition).
func (s *Postgres) RekeyLines(ctx context.Context, planID string, kind domain.PostingKind) ([]domain.RekeyPosting, error) {
	out := []domain.RekeyPosting{}
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var rolledBack bool
		err := tx.QueryRow(ctx, `select rolled_back from cutover_plan where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1`, planID).Scan(&rolledBack)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !rolledBack {
			return fmt.Errorf("%w: plan is not rolled back, so it has no re-keying files", domain.ErrTransition)
		}
		rows, err := tx.Query(ctx, `select entity, document_ref, line, posted_at, account, dimensions, currency, amount::text, text
			from rekey_line where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1 and kind = $2 order by seq`, planID, string(kind))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			l := domain.RekeyPosting{Kind: kind}
			var amount string
			if err := rows.Scan(&l.Entity, &l.DocumentRef, &l.Line, &l.PostedAt, &l.Account, &l.Dimensions, &l.Currency, &amount, &l.Text); err != nil {
				return err
			}
			if l.Amount, err = decimal.NewFromString(amount); err != nil {
				return fmt.Errorf("rekey_line amount %q: %w", amount, err)
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	return out, err
}
