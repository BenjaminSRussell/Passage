package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	mfevents "github.com/BenjaminSRussell/passage/tools/migration-factory/internal/events"
)

// readinessFacts reads what the go/no-go checklist is evaluated over inside tx: the latest run of
// each (pack, company) of the plan, the parallel runs of the month before T-0, the decisions so far
// and the GO approvals of the current round.
func readinessFacts(ctx context.Context, tx pgx.Tx, plan domain.Plan) (domain.ReadinessFacts, error) {
	f := domain.ReadinessFacts{Plan: plan}
	packs := make([]string, len(plan.Packs))
	for i, p := range plan.Packs {
		packs[i] = string(p)
	}
	rows, err := tx.Query(ctx, `select distinct on (pack, source_company) pack, source_company, run_id::text, status, created_at, payables_staged, receivables_load, source_system
		from pack_run
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and pack = any($1) and source_company = any($2)
		order by pack, source_company, created_at desc, run_id desc`, packs, plan.Entities)
	if err != nil {
		return f, err
	}
	var pending, loaded []int
	var systems []string
	for rows.Next() {
		var pf domain.PackFact
		var pack, status, system string
		var payables *int
		var recv *string
		if err := rows.Scan(&pack, &pf.Company, &pf.RunID, &status, &pf.CreatedAt, &payables, &recv, &system); err != nil {
			rows.Close()
			return f, err
		}
		pf.Pack, pf.Status = domain.Pack(pack), domain.RunStatus(status)
		// NULL: a run stored before the count was kept (0012) and not backfilled (0014).
		pf.PayablesStaged, pf.PayablesUncounted = payables != nil && *payables > 0, payables == nil
		if recv != nil {
			pf.ReceivablesLoad = *recv
		}
		if domain.TargetOfPack(pf.Pack) != "" {
			loaded = append(loaded, len(f.Packs))
		}
		if pf.Pack == domain.PackGLBalances {
			pending = append(pending, len(f.Packs))
			systems = append(systems, system)
		}
		f.Packs = append(f.Packs, pf)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return f, err
	}
	for _, i := range loaded {
		if err := tx.QueryRow(ctx, `select exists (select 1 from opening_load where tenant_id = current_setting('passage.tenant_id', true)::uuid and run_id = $1::uuid
			and status = 'RECONCILED' and rejected = 0 and pending = 0)`, f.Packs[i].RunID).Scan(&f.Packs[i].TargetLoaded); err != nil {
			return f, err
		}
	}
	for n, i := range pending {
		if f.Packs[i].GLPosted, err = openingPosted(ctx, tx, systems[n], f.Packs[i].Company); err != nil {
			return f, err
		}
	}
	rows, err = tx.Query(ctx, `select parallel_run_id::text, period, source_system, state, unexplained_count from parallel_run
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and period = $1 order by source_system`, plan.ParallelPeriod())
	if err != nil {
		return f, err
	}
	for rows.Next() {
		var pr domain.ParallelFact
		if err := rows.Scan(&pr.ParallelRunID, &pr.Period, &pr.SourceSystem, &pr.State, &pr.UnexplainedCount); err != nil {
			rows.Close()
			return f, err
		}
		f.Parallel = append(f.Parallel, pr)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return f, err
	}
	if f.Decisions, err = loadDecisions(ctx, tx, plan.PlanID); err != nil {
		return f, err
	}
	f.Pending, err = loadApprovals(ctx, tx, plan.PlanID, f.Round())
	return f, err
}

func loadApprovals(ctx context.Context, tx pgx.Tx, planID string, round int) ([]domain.GoApproval, error) {
	rows, err := tx.Query(ctx, `select round, approver, attestations, waivers, plan_version, approved_at from cutover_go_approval
		where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1 and round = $2 order by approved_at, approver`, planID, round)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.GoApproval{}
	for rows.Next() {
		var a domain.GoApproval
		var att, wv []byte
		if err := rows.Scan(&a.Round, &a.Approver, &att, &wv, &a.PlanVersion, &a.ApprovedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(wv, &a.Waivers); err != nil {
			return nil, fmt.Errorf("decode waivers of the GO approval by %s: %w", a.Approver, err)
		}
		if err := json.Unmarshal(att, &a.Attestations); err != nil {
			return nil, fmt.Errorf("decode attestations of the GO approval by %s: %w", a.Approver, err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func loadDecisions(ctx context.Context, tx pgx.Tx, planID string) ([]domain.Decision, error) {
	rows, err := tx.Query(ctx, `select seq, decision, approvers, reason, checklist, waivers, decided_by, decided_at, plan_version
		from cutover_decision where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = $1 order by seq`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Decision{}
	for rows.Next() {
		var d domain.Decision
		var checklist, wv []byte
		if err := rows.Scan(&d.Seq, &d.Decision, &d.Approvers, &d.Reason, &checklist, &wv, &d.DecidedBy, &d.DecidedAt, &d.PlanVersion); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(checklist, &d.Checklist); err != nil {
			return nil, fmt.Errorf("decode checklist of decision %d: %w", d.Seq, err)
		}
		if err := json.Unmarshal(wv, &d.Waivers); err != nil {
			return nil, fmt.Errorf("decode waivers of decision %d: %w", d.Seq, err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// loadLatestDecisions fills LatestDecision (without its checklist) on every plan in plans.
func loadLatestDecisions(ctx context.Context, tx pgx.Tx, plans []*domain.Plan) error {
	if len(plans) == 0 {
		return nil
	}
	byID := make(map[string]*domain.Plan, len(plans))
	planIDs := make([]string, 0, len(plans))
	for _, p := range plans {
		byID[p.PlanID] = p
		planIDs = append(planIDs, p.PlanID)
	}
	rows, err := tx.Query(ctx, `select distinct on (plan_id) plan_id::text, seq, decision, approvers, reason, decided_by, decided_at, plan_version
		from cutover_decision where tenant_id = current_setting('passage.tenant_id', true)::uuid and plan_id = any($1::uuid[]) order by plan_id, seq desc`, planIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var planID string
		var d domain.Decision
		if err := rows.Scan(&planID, &d.Seq, &d.Decision, &d.Approvers, &d.Reason, &d.DecidedBy, &d.DecidedAt, &d.PlanVersion); err != nil {
			return err
		}
		byID[planID].LatestDecision = &d
	}
	return rows.Err()
}

// GetReadiness returns the plan with the facts its go/no-go checklist is evaluated over.
func (s *Postgres) GetReadiness(ctx context.Context, planID string) (domain.ReadinessFacts, error) {
	var out domain.ReadinessFacts
	err := s.tx(ctx, func(tx pgx.Tx) error {
		plan, err := getPlan(ctx, tx, planID, false)
		if err != nil {
			return err
		}
		out, err = readinessFacts(ctx, tx, plan)
		return err
	})
	return out, err
}

// DecisionRequest records a go/no-go decision, or a first GO approval, on a plan. ExpectedVersion 0
// skips the version check. By is the authenticated user.
type DecisionRequest struct {
	PlanID          string
	Input           domain.DecisionInput
	ExpectedVersion int
}

// DecisionResult is the plan after the request and either the recorded decision or, for the first
// approval of a GO, the approval that waits for a second approver.
type DecisionResult struct {
	Plan     domain.Plan
	Decision *domain.Decision   `json:",omitempty"`
	Approval *domain.GoApproval `json:",omitempty"`
}

// RecordDecision evaluates the checklist and records the request under the plan's row lock: the
// facts are read in the same transaction the decision is stored in, so a GO rests on the pack runs
// and parallel runs as they are at that moment. A NO_GO, or a GO approved by a second, different
// user, is stored with its checklist and writes cutover.decision.recorded; a first GO approval is
// stored as an approval. Either moves the plan's version, all in one transaction.
func (s *Postgres) RecordDecision(ctx context.Context, idem Idem, r DecisionRequest) (DecisionResult, bool, error) {
	var out DecisionResult
	var replayed bool
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = idempotent(ctx, tx, idem, http.StatusCreated, func() (DecisionResult, error) {
			plan, err := lockPlan(ctx, tx, r.PlanID, r.ExpectedVersion)
			if err != nil {
				return DecisionResult{}, err
			}
			facts, err := readinessFacts(ctx, tx, plan)
			if err != nil {
				return DecisionResult{}, err
			}
			step, err := plan.PrepareDecision(r.Input, facts)
			if err != nil {
				return DecisionResult{}, err
			}
			items, err := domain.EvaluateChecklist(facts, step.Attestations, step.Waivers)
			if err != nil {
				return DecisionResult{}, err
			}
			if r.Input.Decision == domain.DecisionGo {
				if err := domain.RequireAllMet(items); err != nil {
					return DecisionResult{}, err
				}
			}
			plan.Version++
			if !step.Complete {
				a, err := insertApproval(ctx, tx, plan, facts.Round(), step)
				if err != nil {
					return DecisionResult{}, err
				}
				if err := savePlanVersion(ctx, tx, plan); err != nil {
					return DecisionResult{}, err
				}
				return DecisionResult{Plan: plan, Approval: &a}, nil
			}
			d := domain.Decision{Seq: facts.Round(), Decision: r.Input.Decision, Approvers: step.Approvers, Reason: step.Reason,
				Checklist: items, Waivers: domain.NormalizeWaivers(step.Waivers), DecidedBy: step.Approvers[len(step.Approvers)-1], PlanVersion: plan.Version}
			checklist, err := json.Marshal(items)
			if err != nil {
				return DecisionResult{}, err
			}
			waivers, err := json.Marshal(d.Waivers)
			if err != nil {
				return DecisionResult{}, err
			}
			if err := tx.QueryRow(ctx, `insert into cutover_decision (tenant_id, plan_id, seq, decision, approvers, reason, checklist, waivers, plan_version, decided_by)
				values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, $8, $9) returning decided_at`,
				plan.PlanID, d.Seq, d.Decision, d.Approvers, d.Reason, string(checklist), string(waivers), d.PlanVersion, d.DecidedBy).Scan(&d.DecidedAt); err != nil {
				return DecisionResult{}, fmt.Errorf("insert cutover_decision: %w", err)
			}
			if err := savePlanVersion(ctx, tx, plan); err != nil {
				return DecisionResult{}, err
			}
			open := domain.OpenItems(items)
			if open == nil {
				open = []string{}
			}
			if err := enqueue(ctx, tx, mfevents.CutoverDecisionRecorded, "cutover_plan", plan.PlanID, mfevents.CutoverDecisionRecordedData{
				PlanID: plan.PlanID, Decision: d.Decision, Approvers: d.Approvers, Reason: d.Reason, OpenItems: open, Waivers: eventWaivers(d.Waivers), DecidedBy: d.DecidedBy,
				Timestamp: d.DecidedAt.UTC().Format(time.RFC3339),
			}); err != nil {
				return DecisionResult{}, fmt.Errorf("enqueue cutover.decision.recorded: %w", err)
			}
			// The decision starts a new round: a rollback approval given before it is void.
			plan.PendingRollback = nil
			plan.LatestDecision = &domain.Decision{Seq: d.Seq, Decision: d.Decision, Approvers: d.Approvers, Reason: d.Reason, DecidedBy: d.DecidedBy,
				DecidedAt: d.DecidedAt, PlanVersion: d.PlanVersion}
			return DecisionResult{Plan: plan, Decision: &d}, nil
		})
		return err
	})
	return out, replayed, err
}

// insertApproval stores the first approval of a GO in the plan's current round.
func insertApproval(ctx context.Context, tx pgx.Tx, plan domain.Plan, round int, step domain.DecisionStep) (domain.GoApproval, error) {
	a := domain.GoApproval{Round: round, Approver: step.Approvers[0], Attestations: domain.ConfirmedAttestations(step.Attestations),
		Waivers: domain.NormalizeWaivers(step.Waivers), PlanVersion: plan.Version}
	att, err := json.Marshal(a.Attestations)
	if err != nil {
		return domain.GoApproval{}, err
	}
	wv, err := json.Marshal(a.Waivers)
	if err != nil {
		return domain.GoApproval{}, err
	}
	if err := tx.QueryRow(ctx, `insert into cutover_go_approval (tenant_id, plan_id, round, approver, attestations, waivers, plan_version)
		values (current_setting('passage.tenant_id', true)::uuid, $1, $2, $3, $4::jsonb, $5::jsonb, $6) returning approved_at`,
		plan.PlanID, a.Round, a.Approver, string(att), string(wv), a.PlanVersion).Scan(&a.ApprovedAt); err != nil {
		return domain.GoApproval{}, fmt.Errorf("insert cutover_go_approval: %w", err)
	}
	return a, nil
}

func eventWaivers(in []domain.Waiver) []mfevents.CutoverDecisionWaiver {
	out := make([]mfevents.CutoverDecisionWaiver, 0, len(in))
	for _, w := range in {
		out = append(out, mfevents.CutoverDecisionWaiver{Pack: w.Pack, Reason: w.Reason})
	}
	return out
}
