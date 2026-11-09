package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
	budgetevents "github.com/BenjaminSRussell/passage/services/budgeting/internal/events"
)

// Budget planning problem codes (listed in the service README).
const (
	CodePlanNotFound      = "BUD-0040"
	CodePlanDuplicate     = "BUD-0041"
	CodePlanNotDraft      = "BUD-0042"
	CodeNotStageOwner     = "BUD-0043"
	CodePlanSeparation    = "BUD-0044"
	CodePlanIncomplete    = "BUD-0045"
	CodePlanStale         = "BUD-0046"
	CodeStageNotOpen      = "BUD-0047"
	CodeScenarioConflict  = "BUD-0048"
	CodeScenarioNotFound  = "BUD-0049"
	CodeGridStale         = "BUD-0050"
	CodeScenarioLimit     = "BUD-0051"
	CodeScenarioDuplicate = "BUD-0052"
	CodePlanRuleMismatch  = "BUD-0053"
	gridLimit             = 10000
)

type NewPlan struct {
	LegalEntityID string
	RuleVersionID string
	LedgerID      string
	Name          string
	FiscalYear    int
}

// Plan is a planning process (FR-38.2).
type Plan struct {
	ID                 string `json:"id"`
	LegalEntityID      string `json:"legalEntityId"`
	RuleVersionID      string `json:"ruleVersionId"`
	LedgerID           string `json:"ledgerId,omitempty"`
	Name               string `json:"name"`
	FiscalYear         int    `json:"fiscalYear"`
	Currency           string `json:"currency"`
	Status             string `json:"status"`
	Submission         int    `json:"submission"`
	SubmittedBy        string `json:"submittedBy,omitempty"`
	ApprovedScenarioID string `json:"approvedScenarioId,omitempty"`
	RegisterEntryID    string `json:"registerEntryId,omitempty"`
	Version            int64  `json:"version"`
	CreatedBy          string `json:"createdBy"`
	CreatedAt          string `json:"createdAt"`
}

type Scenario struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Kind         string          `json:"kind"`
	CopiedFromID string          `json:"copiedFromId,omitempty"`
	Version      int64           `json:"version"`
	Total        decimal.Decimal `json:"total"`
}

// StageOwner is the person who signs a stage for one org unit.
type StageOwner struct {
	Stage     string `json:"stage"`
	OrgUnitID string `json:"orgUnitId"`
	Owner     string `json:"owner"`
	SignedBy  string `json:"signedBy,omitempty"`
	SignedAt  string `json:"signedAt,omitempty"`
}

type PlanView struct {
	Plan
	Scenarios   []Scenario   `json:"scenarios"`
	StageOwners []StageOwner `json:"stageOwners"`
}

type NewScenario struct {
	Name          string
	Kind          string
	CopyFromID    string
	AdjustmentPct decimal.Decimal
}

type GridFilter struct{ Account, CostCenter, Period string }

type Grid struct {
	Scenario Scenario          `json:"scenario"`
	Cells    []domain.PlanCell `json:"cells"`
	Total    decimal.Decimal   `json:"total"`
}

// GridWritten reports a bulk cell write; Version is the grid's new ETag.
type GridWritten struct {
	ScenarioID string          `json:"scenarioId"`
	Version    int64           `json:"version"`
	Cells      int             `json:"cells"`
	Total      decimal.Decimal `json:"total"`
}

type SignOffRequest struct {
	PlanID     string
	Stage      string
	OrgUnitID  string
	ScenarioID string
	Submission int
}

type RejectRequest struct {
	PlanID     string
	Reason     string
	Submission int
}

func planProblem(status int, code, detail string) error {
	return httpx.NewProblem(status, code, detail)
}

func actorOf(ctx context.Context) (string, error) {
	p, _ := tenant.FromContext(ctx)
	if p.UserID == "" {
		return "", planProblem(http.StatusUnauthorized, "AUTH-0001", "the acting user is required")
	}
	return p.UserID, nil
}

const planColumns = `id::text, legal_entity_id::text, rule_version_id::text, coalesce(ledger_id::text, ''), name, fiscal_year, currency, status,
	submission, coalesce(submitted_by, ''), coalesce(approved_scenario_id::text, ''), coalesce(register_entry_id::text, ''), version, created_by, created_at`

func scanPlan(row pgx.Row) (Plan, error) {
	var p Plan
	var created time.Time
	err := row.Scan(&p.ID, &p.LegalEntityID, &p.RuleVersionID, &p.LedgerID, &p.Name, &p.FiscalYear, &p.Currency, &p.Status, &p.Submission,
		&p.SubmittedBy, &p.ApprovedScenarioID, &p.RegisterEntryID, &p.Version, &p.CreatedBy, &created)
	p.CreatedAt = stamp(created)
	return p, err
}

func loadPlan(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (Plan, error) {
	q := `select ` + planColumns + ` from budget_plan_process where tenant_id = $1 and id = $2`
	if lock {
		q += ` for update`
	}
	p, err := scanPlan(tx.QueryRow(ctx, q, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, planProblem(http.StatusNotFound, CodePlanNotFound, "budget plan not found")
	}
	return p, err
}

func loadDraftPlan(ctx context.Context, tx pgx.Tx, tenantID, id string) (Plan, error) {
	p, err := loadPlan(ctx, tx, tenantID, id, true)
	if err == nil && p.Status != domain.PlanDraft {
		return p, planProblem(http.StatusConflict, CodePlanNotDraft, fmt.Sprintf("the plan is %s; only a Draft plan can be changed", p.Status))
	}
	return p, err
}

func loadScenario(ctx context.Context, tx pgx.Tx, tenantID, planID, id string, lock bool) (Scenario, error) {
	q := `select s.id::text, s.name, s.kind, coalesce(s.copied_from_id::text, ''), s.version,
		(select coalesce(sum(c.amount), 0)::text from budget_plan_cell c where c.tenant_id = s.tenant_id and c.scenario_id = s.id)
		from budget_plan_scenario s where s.tenant_id = $1 and s.process_id = $2 and s.id = $3`
	if lock {
		q += ` for update of s`
	}
	var sc Scenario
	var total string
	err := tx.QueryRow(ctx, q, tenantID, planID, id).Scan(&sc.ID, &sc.Name, &sc.Kind, &sc.CopiedFromID, &sc.Version, &total)
	if errors.Is(err, pgx.ErrNoRows) {
		return sc, planProblem(http.StatusNotFound, CodeScenarioNotFound, "scenario not found in this plan")
	}
	sc.Total = parseDec(total)
	return sc, err
}

func loadPlanView(ctx context.Context, tx pgx.Tx, tenantID, id string) (PlanView, error) {
	p, err := loadPlan(ctx, tx, tenantID, id, false)
	if err != nil {
		return PlanView{}, err
	}
	v := PlanView{Plan: p, Scenarios: []Scenario{}, StageOwners: []StageOwner{}}
	rows, err := tx.Query(ctx, `select s.id::text, s.name, s.kind, coalesce(s.copied_from_id::text, ''), s.version,
		(select coalesce(sum(c.amount), 0)::text from budget_plan_cell c where c.tenant_id = s.tenant_id and c.scenario_id = s.id)
		from budget_plan_scenario s where s.tenant_id = $1 and s.process_id = $2 order by s.created_at, s.name`, tenantID, id)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var sc Scenario
		var total string
		if err := rows.Scan(&sc.ID, &sc.Name, &sc.Kind, &sc.CopiedFromID, &sc.Version, &total); err != nil {
			rows.Close()
			return v, err
		}
		sc.Total = parseDec(total)
		v.Scenarios = append(v.Scenarios, sc)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return v, err
	}
	owners, err := tx.Query(ctx, `select stage, org_unit_id::text, owner, coalesce(signed_by, ''), coalesce(signed_at::text, '')
		from budget_plan_stage_owner where tenant_id = $1 and process_id = $2 order by stage desc, org_unit_id`, tenantID, id)
	if err != nil {
		return v, err
	}
	defer owners.Close()
	for owners.Next() {
		var o StageOwner
		if err := owners.Scan(&o.Stage, &o.OrgUnitID, &o.Owner, &o.SignedBy, &o.SignedAt); err != nil {
			return v, err
		}
		v.StageOwners = append(v.StageOwners, o)
	}
	return v, owners.Err()
}

func bumpPlan(ctx context.Context, tx pgx.Tx, tenantID, id string) error {
	_, err := tx.Exec(ctx, `update budget_plan_process set version = version + 1 where tenant_id = $1 and id = $2`, tenantID, id)
	return err
}

func logDecision(ctx context.Context, tx pgx.Tx, tenantID string, p Plan, stage, orgUnitID, actor, decision, reason string) error {
	_, err := tx.Exec(ctx, `insert into budget_plan_decision (tenant_id, id, legal_entity_id, process_id, submission, stage, org_unit_id, actor, decision, reason)
		values ($1, $2, $3, $4, $5, $6, nullif($7, '')::uuid, $8, $9, nullif($10, ''))`,
		tenantID, ids.New(), p.LegalEntityID, p.ID, p.Submission, stage, orgUnitID, actor, decision, reason)
	return err
}

// CreatePlan opens a planning process on a control rule version with a Baseline scenario.
func (s *Postgres) CreatePlan(ctx context.Context, key, hash string, in NewPlan) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	actor, err := actorOf(ctx)
	if err != nil {
		return 0, nil, err
	}
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		rule, err := loadRule(ctx, tx, p.TenantID, in.RuleVersionID)
		if err != nil {
			return 0, nil, err
		}
		if rule.LegalEntityID != in.LegalEntityID {
			return 0, nil, planProblem(http.StatusUnprocessableEntity, CodePlanRuleMismatch, "the control rule version belongs to another legal entity")
		}
		id := ids.New()
		var created string
		err = tx.QueryRow(ctx, `insert into budget_plan_process (tenant_id, id, legal_entity_id, rule_version_id, ledger_id, name, fiscal_year, currency, created_by)
			values ($1, $2, $3, $4, nullif($5, '')::uuid, $6, $7, $8, $9)
			on conflict (tenant_id, legal_entity_id, fiscal_year, name) do nothing returning id::text`,
			p.TenantID, id, in.LegalEntityID, in.RuleVersionID, in.LedgerID, in.Name, in.FiscalYear, rule.Currency, actor).Scan(&created)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, planProblem(http.StatusConflict, CodePlanDuplicate, "a plan with this name exists for the legal entity and fiscal year")
		}
		if err != nil {
			return 0, nil, err
		}
		if _, err := tx.Exec(ctx, `insert into budget_plan_scenario (tenant_id, id, legal_entity_id, process_id, name, kind) values ($1, $2, $3, $4, 'Baseline', 'Baseline')`,
			p.TenantID, ids.New(), in.LegalEntityID, id); err != nil {
			return 0, nil, err
		}
		view, err := loadPlanView(ctx, tx, p.TenantID, id)
		return http.StatusCreated, view, err
	})
}

type StageOwnerInput struct{ Stage, OrgUnitID, Owner string }

// SetStageOwners replaces the stage owners of a Draft plan. One person may not own both stages: the reviewer
// of a plan never approves it (7.38.17).
func (s *Postgres) SetStageOwners(ctx context.Context, key, hash, planID string, owners []StageOwnerInput) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		plan, err := loadDraftPlan(ctx, tx, p.TenantID, planID)
		if err != nil {
			return 0, nil, err
		}
		reviewers := map[string]bool{}
		for _, o := range owners {
			if o.Stage == domain.StageReview {
				reviewers[o.Owner] = true
			}
		}
		for _, o := range owners {
			if o.Stage == domain.StageApprove && reviewers[o.Owner] {
				return 0, nil, planProblem(http.StatusUnprocessableEntity, CodePlanSeparation, "a person cannot own both the Review and the Approve stage")
			}
		}
		if _, err := tx.Exec(ctx, `delete from budget_plan_stage_owner where tenant_id = $1 and process_id = $2`, p.TenantID, planID); err != nil {
			return 0, nil, err
		}
		for _, o := range owners {
			if _, err := tx.Exec(ctx, `insert into budget_plan_stage_owner (tenant_id, legal_entity_id, process_id, stage, org_unit_id, owner) values ($1, $2, $3, $4, $5, $6)`,
				p.TenantID, plan.LegalEntityID, planID, o.Stage, o.OrgUnitID, o.Owner); err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
					return 0, nil, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "each stage and org unit can have one owner")
				}
				return 0, nil, err
			}
		}
		if err := bumpPlan(ctx, tx, p.TenantID, planID); err != nil {
			return 0, nil, err
		}
		view, err := loadPlanView(ctx, tx, p.TenantID, planID)
		return http.StatusOK, view, err
	})
}

// CreateScenario adds a scenario to a Draft plan, up to 10, optionally copying another scenario's cells
// with a percentage adjustment.
func (s *Postgres) CreateScenario(ctx context.Context, key, hash, planID string, in NewScenario) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		plan, err := loadDraftPlan(ctx, tx, p.TenantID, planID)
		if err != nil {
			return 0, nil, err
		}
		var count int
		if err := tx.QueryRow(ctx, `select count(*) from budget_plan_scenario where tenant_id = $1 and process_id = $2`, p.TenantID, planID).Scan(&count); err != nil {
			return 0, nil, err
		}
		if count >= domain.MaxScenarios {
			return 0, nil, planProblem(http.StatusConflict, CodeScenarioLimit, fmt.Sprintf("a plan holds at most %d scenarios", domain.MaxScenarios))
		}
		if in.CopyFromID != "" {
			if _, err := loadScenario(ctx, tx, p.TenantID, planID, in.CopyFromID, false); err != nil {
				return 0, nil, err
			}
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into budget_plan_scenario (tenant_id, id, legal_entity_id, process_id, name, kind, copied_from_id) values ($1, $2, $3, $4, $5, $6, nullif($7, '')::uuid)`,
			p.TenantID, id, plan.LegalEntityID, planID, in.Name, in.Kind, in.CopyFromID); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
				return 0, nil, planProblem(http.StatusConflict, CodeScenarioDuplicate, "a scenario with this name exists in the plan")
			}
			return 0, nil, err
		}
		if in.CopyFromID != "" {
			if _, err := tx.Exec(ctx, `insert into budget_plan_cell (tenant_id, legal_entity_id, process_id, scenario_id, account, cost_center, period, amount)
				select tenant_id, legal_entity_id, process_id, $3, account, cost_center, period, round(amount * (1 + $4::numeric / 100), 4)
				from budget_plan_cell where tenant_id = $1 and scenario_id = $2`, p.TenantID, in.CopyFromID, id, in.AdjustmentPct.String()); err != nil {
				return 0, nil, err
			}
		}
		sc, err := loadScenario(ctx, tx, p.TenantID, planID, id, false)
		return http.StatusCreated, sc, err
	})
}

// UpsertCells writes cells into a scenario's grid under optimistic concurrency: version must equal the grid's
// current ETag, otherwise nothing is written (BUD-0050).
func (s *Postgres) UpsertCells(ctx context.Context, key, hash, planID, scenarioID string, version int64, cells []domain.PlanCell) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		plan, err := loadDraftPlan(ctx, tx, p.TenantID, planID)
		if err != nil {
			return 0, nil, err
		}
		sc, err := loadScenario(ctx, tx, p.TenantID, planID, scenarioID, true)
		if err != nil {
			return 0, nil, err
		}
		if sc.Version != version {
			return 0, nil, planProblem(http.StatusPreconditionFailed, CodeGridStale, fmt.Sprintf("the grid is at version %d; reload it and retry", sc.Version))
		}
		accounts, centers, periods, amounts := make([]string, len(cells)), make([]string, len(cells)), make([]string, len(cells)), make([]string, len(cells))
		for i, c := range cells {
			if !domain.PeriodInYear(c.Period, plan.FiscalYear) {
				return 0, nil, httpx.NewProblem(http.StatusBadRequest, "COM-0001", fmt.Sprintf("period %s is outside fiscal year %d", c.Period, plan.FiscalYear))
			}
			accounts[i], centers[i], periods[i], amounts[i] = c.Account, c.CostCenter, c.Period, c.Amount.StringFixed(4)
		}
		if _, err := tx.Exec(ctx, `insert into budget_plan_cell (tenant_id, legal_entity_id, process_id, scenario_id, account, cost_center, period, amount)
			select $1, $2, $3, $4, a, cc, pe, am::numeric from unnest($5::text[], $6::text[], $7::text[], $8::text[]) as t(a, cc, pe, am)
			on conflict (tenant_id, scenario_id, account, cost_center, period) do update set amount = excluded.amount, updated_at = now()`,
			p.TenantID, plan.LegalEntityID, planID, scenarioID, accounts, centers, periods, amounts); err != nil {
			return 0, nil, err
		}
		if _, err := tx.Exec(ctx, `update budget_plan_scenario set version = version + 1 where tenant_id = $1 and id = $2`, p.TenantID, scenarioID); err != nil {
			return 0, nil, err
		}
		after, err := loadScenario(ctx, tx, p.TenantID, planID, scenarioID, false)
		return http.StatusOK, GridWritten{ScenarioID: scenarioID, Version: after.Version, Cells: len(cells), Total: after.Total}, err
	})
}

func (s *Postgres) ListPlans(ctx context.Context, legalEntityID string) ([]Plan, error) {
	out := []Plan{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		rows, err := tx.Query(ctx, `select `+planColumns+` from budget_plan_process where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2)
			order by fiscal_year desc, name limit $3`, tenantID, legalEntityID, listLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanPlan(rows)
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) GetPlan(ctx context.Context, id string) (PlanView, error) {
	var v PlanView
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		var err error
		v, err = loadPlanView(ctx, tx, tenantID, id)
		return err
	})
	return v, err
}

func (s *Postgres) GetGrid(ctx context.Context, planID, scenarioID string, f GridFilter) (Grid, error) {
	g := Grid{Cells: []domain.PlanCell{}}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		var err error
		if g.Scenario, err = loadScenario(ctx, tx, tenantID, planID, scenarioID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select account, cost_center, period, amount::text from budget_plan_cell
			where tenant_id = $1 and scenario_id = $2 and ($3 = '' or account = $3) and ($4 = '' or cost_center = $4) and ($5 = '' or period = $5)
			order by account, cost_center, period limit $6`, tenantID, scenarioID, f.Account, f.CostCenter, f.Period, gridLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c domain.PlanCell
			var amount string
			if err := rows.Scan(&c.Account, &c.CostCenter, &c.Period, &amount); err != nil {
				return err
			}
			c.Amount = parseDec(amount)
			g.Total = g.Total.Add(c.Amount)
			g.Cells = append(g.Cells, c)
		}
		return rows.Err()
	})
	return g, err
}

// SubmitPlan moves a Draft plan to Submitted once both stages have owners and some scenario holds an amount.
// The submission counter ties the approval workflow to this round.
func (s *Postgres) SubmitPlan(ctx context.Context, key, hash, planID string) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	actor, err := actorOf(ctx)
	if err != nil {
		return 0, nil, err
	}
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		plan, err := loadDraftPlan(ctx, tx, p.TenantID, planID)
		if err != nil {
			return 0, nil, err
		}
		var reviewers, approvers int
		var amounts bool
		if err := tx.QueryRow(ctx, `select
			(select count(*) from budget_plan_stage_owner where tenant_id = $1 and process_id = $2 and stage = 'Review'),
			(select count(*) from budget_plan_stage_owner where tenant_id = $1 and process_id = $2 and stage = 'Approve'),
			exists (select 1 from budget_plan_cell where tenant_id = $1 and process_id = $2 and amount > 0)`, p.TenantID, planID).Scan(&reviewers, &approvers, &amounts); err != nil {
			return 0, nil, err
		}
		if reviewers == 0 || approvers == 0 || !amounts {
			return 0, nil, planProblem(http.StatusUnprocessableEntity, CodePlanIncomplete, "a plan needs a Review owner, an Approve owner and an amount in some scenario before it is submitted")
		}
		plan.Submission++
		if _, err := tx.Exec(ctx, `update budget_plan_process set status = 'Submitted', submission = $3, submitted_by = $4, version = version + 1 where tenant_id = $1 and id = $2`,
			p.TenantID, planID, plan.Submission, actor); err != nil {
			return 0, nil, err
		}
		if err := logDecision(ctx, tx, p.TenantID, plan, "Submit", "", actor, "Submitted", ""); err != nil {
			return 0, nil, err
		}
		if err := enqueue(ctx, tx, "plan", planID, budgetevents.PlanSubmitted, p.TenantID, budgetevents.PlanSubmittedData{
			PlanID: planID, LegalEntityID: plan.LegalEntityID, Name: plan.Name, FiscalYear: plan.FiscalYear, Submission: plan.Submission, SubmittedBy: actor,
		}); err != nil {
			return 0, nil, err
		}
		view, err := loadPlanView(ctx, tx, p.TenantID, planID)
		return http.StatusOK, view, err
	})
}

// SignOff records the acting user's sign-off of a stage for one org unit. The user must be that unit's owner of the
// stage, must not have submitted the plan, and an approver must not have reviewed it. When the last org unit has
// signed the stage completes; completing Approve posts the approved scenario as an Original register entry.
func (s *Postgres) SignOff(ctx context.Context, key, hash string, in SignOffRequest) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	actor, err := actorOf(ctx)
	if err != nil {
		return 0, nil, err
	}
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		plan, err := loadPlan(ctx, tx, p.TenantID, in.PlanID, true)
		if err != nil {
			return 0, nil, err
		}
		if plan.Submission != in.Submission {
			return 0, nil, planProblem(http.StatusConflict, CodePlanStale, fmt.Sprintf("submission %d is not the current submission %d", in.Submission, plan.Submission))
		}
		if plan.Status != domain.StageOpenStatus(in.Stage) {
			return 0, nil, planProblem(http.StatusConflict, CodeStageNotOpen, fmt.Sprintf("the %s stage is not open: the plan is %s", in.Stage, plan.Status))
		}
		var owner, signedBy string
		err = tx.QueryRow(ctx, `select owner, coalesce(signed_by, '') from budget_plan_stage_owner
			where tenant_id = $1 and process_id = $2 and stage = $3 and org_unit_id = $4 for update`, p.TenantID, in.PlanID, in.Stage, in.OrgUnitID).Scan(&owner, &signedBy)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != actor) {
			return 0, nil, planProblem(http.StatusForbidden, CodeNotStageOwner, "the acting user does not own this stage for the org unit")
		}
		if err != nil {
			return 0, nil, err
		}
		if actor == plan.SubmittedBy {
			return 0, nil, planProblem(http.StatusForbidden, CodePlanSeparation, "the user who submitted the plan cannot sign it off")
		}
		if in.Stage == domain.StageApprove {
			var reviewed bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from budget_plan_stage_owner where tenant_id = $1 and process_id = $2 and stage = 'Review' and signed_by = $3)`,
				p.TenantID, in.PlanID, actor).Scan(&reviewed); err != nil {
				return 0, nil, err
			}
			if reviewed {
				return 0, nil, planProblem(http.StatusForbidden, CodePlanSeparation, "the reviewer of the plan cannot approve it")
			}
			if plan.ApprovedScenarioID != "" && plan.ApprovedScenarioID != in.ScenarioID {
				return 0, nil, planProblem(http.StatusConflict, CodeScenarioConflict, "another org unit already approved a different scenario")
			}
			if _, err := loadScenario(ctx, tx, p.TenantID, in.PlanID, in.ScenarioID, false); err != nil {
				return 0, nil, err
			}
		}
		if signedBy == "" {
			if _, err := tx.Exec(ctx, `update budget_plan_stage_owner set signed_by = $5, signed_at = now()
				where tenant_id = $1 and process_id = $2 and stage = $3 and org_unit_id = $4`, p.TenantID, in.PlanID, in.Stage, in.OrgUnitID, actor); err != nil {
				return 0, nil, err
			}
			if in.Stage == domain.StageApprove {
				if _, err := tx.Exec(ctx, `update budget_plan_process set approved_scenario_id = $3 where tenant_id = $1 and id = $2`, p.TenantID, in.PlanID, in.ScenarioID); err != nil {
					return 0, nil, err
				}
				plan.ApprovedScenarioID = in.ScenarioID
			}
			if err := logDecision(ctx, tx, p.TenantID, plan, in.Stage, in.OrgUnitID, actor, "SignedOff", ""); err != nil {
				return 0, nil, err
			}
			if err := bumpPlan(ctx, tx, p.TenantID, in.PlanID); err != nil {
				return 0, nil, err
			}
		}
		var open int
		if err := tx.QueryRow(ctx, `select count(*) from budget_plan_stage_owner where tenant_id = $1 and process_id = $2 and stage = $3 and signed_by is null`,
			p.TenantID, in.PlanID, in.Stage).Scan(&open); err != nil {
			return 0, nil, err
		}
		if open == 0 {
			if err := completeStage(ctx, tx, p.TenantID, plan, in.Stage); err != nil {
				return 0, nil, err
			}
		}
		view, err := loadPlanView(ctx, tx, p.TenantID, in.PlanID)
		return http.StatusOK, view, err
	})
}

func completeStage(ctx context.Context, tx pgx.Tx, tenantID string, plan Plan, stage string) error {
	if stage == domain.StageReview {
		_, err := tx.Exec(ctx, `update budget_plan_process set status = 'Reviewed', version = version + 1 where tenant_id = $1 and id = $2`, tenantID, plan.ID)
		return err
	}
	rows, err := tx.Query(ctx, `select account, cost_center, period, amount::text from budget_plan_cell where tenant_id = $1 and scenario_id = $2`, tenantID, plan.ApprovedScenarioID)
	if err != nil {
		return err
	}
	var cells []domain.PlanCell
	for rows.Next() {
		var c domain.PlanCell
		var amount string
		if err := rows.Scan(&c.Account, &c.CostCenter, &c.Period, &amount); err != nil {
			rows.Close()
			return err
		}
		c.Amount = parseDec(amount)
		cells = append(cells, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	lines := domain.RegisterLines(cells)
	if len(lines) == 0 {
		return planProblem(http.StatusUnprocessableEntity, CodePlanIncomplete, "the approved scenario holds no amount to post")
	}
	rule, err := loadRule(ctx, tx, tenantID, plan.RuleVersionID)
	if err != nil {
		return err
	}
	entry, err := postEntry(ctx, tx, tenantID, rule, domain.EntryOriginal, lines)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update budget_plan_process set status = 'Approved', register_entry_id = $3, version = version + 1 where tenant_id = $1 and id = $2`,
		tenantID, plan.ID, entry.ID); err != nil {
		return err
	}
	return enqueue(ctx, tx, "plan", plan.ID, budgetevents.PlanApproved, tenantID, budgetevents.PlanApprovedData{
		PlanID: plan.ID, ScenarioID: plan.ApprovedScenarioID, RegisterEntryID: entry.ID, Total: entry.Total.String(),
	})
}

// RejectPlan sends a Submitted or Reviewed plan back to Draft; any owner of the open stage may reject. Sign-offs
// are cleared so the next submission starts clean.
func (s *Postgres) RejectPlan(ctx context.Context, key, hash string, in RejectRequest) (int, []byte, error) {
	p, _ := tenant.FromContext(ctx)
	actor, err := actorOf(ctx)
	if err != nil {
		return 0, nil, err
	}
	return s.run(ctx, key, hash, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		plan, err := loadPlan(ctx, tx, p.TenantID, in.PlanID, true)
		if err != nil {
			return 0, nil, err
		}
		if plan.Submission != in.Submission {
			return 0, nil, planProblem(http.StatusConflict, CodePlanStale, fmt.Sprintf("submission %d is not the current submission %d", in.Submission, plan.Submission))
		}
		stage := domain.StageForStatus(plan.Status)
		if stage == "" {
			return 0, nil, planProblem(http.StatusConflict, CodeStageNotOpen, fmt.Sprintf("the plan is %s; there is no open stage to reject", plan.Status))
		}
		var owns bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from budget_plan_stage_owner where tenant_id = $1 and process_id = $2 and stage = $3 and owner = $4)`,
			p.TenantID, in.PlanID, stage, actor).Scan(&owns); err != nil {
			return 0, nil, err
		}
		if !owns {
			return 0, nil, planProblem(http.StatusForbidden, CodeNotStageOwner, "the acting user owns no org unit of the open stage")
		}
		if _, err := tx.Exec(ctx, `update budget_plan_stage_owner set signed_by = null, signed_at = null where tenant_id = $1 and process_id = $2`, p.TenantID, in.PlanID); err != nil {
			return 0, nil, err
		}
		if _, err := tx.Exec(ctx, `update budget_plan_process set status = 'Draft', approved_scenario_id = null, version = version + 1 where tenant_id = $1 and id = $2`, p.TenantID, in.PlanID); err != nil {
			return 0, nil, err
		}
		if err := logDecision(ctx, tx, p.TenantID, plan, stage, "", actor, "Rejected", in.Reason); err != nil {
			return 0, nil, err
		}
		if err := enqueue(ctx, tx, "plan", in.PlanID, budgetevents.PlanRejected, p.TenantID, budgetevents.PlanRejectedData{
			PlanID: in.PlanID, Submission: plan.Submission, Stage: stage, RejectedBy: actor, Reason: in.Reason,
		}); err != nil {
			return 0, nil, err
		}
		view, err := loadPlanView(ctx, tx, p.TenantID, in.PlanID)
		return http.StatusOK, view, err
	})
}
