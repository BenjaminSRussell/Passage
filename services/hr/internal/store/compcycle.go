package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
)

const (
	CodeCycleNotFound  = "HCM-CMP-NOT-FOUND"
	CodeCycleState     = "HCM-CMP-STATE"
	CodeCompNotAnnual  = "HCM-CMP-006"
	CodeCompNoEmploy   = "HCM-CMP-007"
	CodeCompStale      = "HCM-CMP-008"
	CodeWorksheetEmpty = "HCM-CMP-009"
	cycleManagerInput  = "MANAGER_INPUT"
	cycleApproval      = "APPROVAL"
	cycleApplied       = "APPLIED"
	worksheetDraft     = "DRAFT"
	worksheetSubmitted = "SUBMITTED"
)

type CycleBudget struct {
	DepartmentID string `json:"departmentId"`
	BudgetAmount string `json:"budgetAmount"`
}

type CompCycle struct {
	ID              string                `json:"id"`
	Name            string                `json:"name"`
	Kind            string                `json:"kind"`
	EffectiveDate   string                `json:"effectiveDate"`
	Currency        string                `json:"currency"`
	AllowOverBudget bool                  `json:"allowOverBudget"`
	GuidelineMatrix []domain.GuidelineRow `json:"guidelineMatrix"`
	Budgets         []CycleBudget         `json:"budgets"`
	Status          string                `json:"status"`
	AppliedAt       *time.Time            `json:"appliedAt,omitempty"`
}

const cycleSelect = `select id::text, name, kind, to_char(effective_date, 'YYYY-MM-DD'), currency, allow_over_budget, guideline_matrix, status, applied_at from hr_comp_cycle`

func loadCycle(ctx context.Context, tx pgx.Tx, tenantID, id string, lock bool) (CompCycle, error) {
	q := cycleSelect + ` where tenant_id = $1 and id = $2`
	if lock {
		q += ` for update`
	}
	var c CompCycle
	var matrix []byte
	err := tx.QueryRow(ctx, q, tenantID, id).Scan(&c.ID, &c.Name, &c.Kind, &c.EffectiveDate, &c.Currency, &c.AllowOverBudget, &matrix, &c.Status, &c.AppliedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, problem(http.StatusNotFound, CodeCycleNotFound, "compensation cycle not found")
	}
	if err != nil {
		return c, err
	}
	c.AppliedAt = utcPtr(c.AppliedAt)
	if err := json.Unmarshal(matrix, &c.GuidelineMatrix); err != nil {
		return c, err
	}
	rows, err := tx.Query(ctx, `select department_id::text, budget_amount::text from hr_comp_cycle_budget where tenant_id = $1 and cycle_id = $2 order by department_id`, tenantID, id)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	c.Budgets = []CycleBudget{}
	for rows.Next() {
		var b CycleBudget
		if err := rows.Scan(&b.DepartmentID, &b.BudgetAmount); err != nil {
			return c, err
		}
		c.Budgets = append(c.Budgets, b)
	}
	return c, rows.Err()
}

func (s *Postgres) CreateCompCycle(ctx context.Context, in CompCycle, idempotencyKey string) (CompCycle, bool, error) {
	p := principal(ctx)
	key := idemKey(idempotencyKey)
	var out CompCycle
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			var existing string
			err := tx.QueryRow(ctx, `select id::text from hr_comp_cycle where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key).Scan(&existing)
			if err == nil {
				out, err = loadCycle(ctx, tx, p.TenantID, existing, false)
				return err
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		matrix, err := json.Marshal(in.GuidelineMatrix)
		if err != nil {
			return err
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into hr_comp_cycle (tenant_id, id, name, kind, effective_date, currency, allow_over_budget, guideline_matrix, status, idempotency_key)
			values ($1, $2, $3, $4, $5::date, $6, $7, $8, 'PLANNING', $9)`, p.TenantID, id, in.Name, in.Kind, in.EffectiveDate, in.Currency, in.AllowOverBudget, matrix, key); err != nil {
			return err
		}
		for _, b := range in.Budgets {
			if _, err := tx.Exec(ctx, `insert into hr_comp_cycle_budget (tenant_id, cycle_id, department_id, budget_amount) values ($1, $2, $3, $4::numeric)`,
				p.TenantID, id, b.DepartmentID, b.BudgetAmount); err != nil {
				return err
			}
		}
		inserted = true
		out, err = loadCycle(ctx, tx, p.TenantID, id, false)
		return err
	})
	if idempotencyRace(err) {
		return CompCycle{}, false, idempotencyInFlight()
	}
	if err == nil && key != nil && !inserted && (out.Name != in.Name || out.EffectiveDate != in.EffectiveDate || len(out.Budgets) != len(in.Budgets)) {
		return CompCycle{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

func (s *Postgres) GetCompCycle(ctx context.Context, id string) (CompCycle, error) {
	p := principal(ctx)
	var out CompCycle
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = loadCycle(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

func (s *Postgres) ListCompCycles(ctx context.Context, after string, limit int) ([]CompCycle, string, error) {
	p := principal(ctx)
	out := []CompCycle{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text from hr_comp_cycle where tenant_id = $1 and id::text > $2 order by id limit $3`, p.TenantID, after, limit+1)
		if err != nil {
			return err
		}
		found, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range found {
			c, err := loadCycle(ctx, tx, p.TenantID, id, false)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return nil
	})
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	return out, next, err
}

var cycleMoves = map[string]string{"PLANNING": "MANAGER_INPUT", "MANAGER_INPUT": "APPROVAL"}

func (s *Postgres) AdvanceCompCycle(ctx context.Context, id, to string) (CompCycle, error) {
	p := principal(ctx)
	var out CompCycle
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := loadCycle(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if cur.Status == to {
			out = cur
			return nil
		}
		if cycleMoves[cur.Status] != to && !(to == "CANCELED" && cur.Status != cycleApplied && cur.Status != "CANCELED") {
			return problem(http.StatusConflict, CodeCycleState, fmt.Sprintf("a %s cycle cannot become %s", cur.Status, to))
		}
		if _, err := tx.Exec(ctx, `update hr_comp_cycle set status = $3, updated_at = now() where tenant_id = $1 and id = $2`, p.TenantID, id, to); err != nil {
			return err
		}
		out, err = loadCycle(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}

type WorksheetRowInput struct {
	WorkerID      string
	Rating        int
	ProposedPct   string
	Justification *string
}

type WorksheetRow struct {
	WorkerID        string  `json:"workerId"`
	ManagerWorkerID string  `json:"managerWorkerId"`
	EmploymentID    string  `json:"employmentId"`
	DepartmentID    string  `json:"departmentId"`
	CurrentPay      string  `json:"currentPay"`
	Rating          int     `json:"rating"`
	CompaRatio      *string `json:"compaRatio,omitempty"`
	GuidelinePctMin *string `json:"guidelinePctMin,omitempty"`
	GuidelinePctMax *string `json:"guidelinePctMax,omitempty"`
	ProposedPct     string  `json:"proposedPct"`
	ProposedAmount  string  `json:"proposedAmount"`
	Justification   *string `json:"justification,omitempty"`
	Status          string  `json:"status"`
	ActionID        *string `json:"actionId,omitempty"`
}

const worksheetSelect = `select worker_id::text, manager_worker_id::text, employment_id::text, department_id::text, current_pay::text, rating, compa_ratio::text,
	guideline_pct_min::text, guideline_pct_max::text, proposed_pct::text, proposed_amount::text, justification, status, action_id::text from hr_comp_worksheet_row`

func scanWorksheetRow(row pgx.Row) (WorksheetRow, error) {
	var r WorksheetRow
	err := row.Scan(&r.WorkerID, &r.ManagerWorkerID, &r.EmploymentID, &r.DepartmentID, &r.CurrentPay, &r.Rating, &r.CompaRatio,
		&r.GuidelinePctMin, &r.GuidelinePctMax, &r.ProposedPct, &r.ProposedAmount, &r.Justification, &r.Status, &r.ActionID)
	return r, err
}

func listWorksheet(ctx context.Context, tx pgx.Tx, tenantID, cycleID, managerID string) ([]WorksheetRow, error) {
	rows, err := tx.Query(ctx, worksheetSelect+` where tenant_id = $1 and cycle_id = $2 and manager_worker_id = $3 order by worker_id`, tenantID, cycleID, managerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorksheetRow{}
	for rows.Next() {
		r, err := scanWorksheetRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Postgres) GetWorksheet(ctx context.Context, cycleID, managerID string) ([]WorksheetRow, error) {
	p := principal(ctx)
	var out []WorksheetRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := loadCycle(ctx, tx, p.TenantID, cycleID, false); err != nil {
			return err
		}
		var err error
		out, err = listWorksheet(ctx, tx, p.TenantID, cycleID, managerID)
		return err
	})
	return out, err
}

type payFacts struct {
	employmentID, departmentID, planID, jobCode, legalEntityID string
	pay                                                        decimal.Decimal
}

func workerPayFacts(ctx context.Context, tx pgx.Tx, tenantID, workerID string, c CompCycle, at time.Time) (payFacts, *domain.Error, error) {
	var f payFacts
	var amount, basis, currency string
	err := tx.QueryRow(ctx, `select e.id::text, e.legal_entity_id::text, pos.department_id::text, pos.job_code, fc.plan_id::text, fc.amount::text, pl.pay_basis, fc.currency
		from employment e
		join position_assignment pa on pa.tenant_id = e.tenant_id and pa.employment_id = e.id and pa.is_primary and pa.validity @> $3::timestamptz
		join hr_position pos on pos.tenant_id = pa.tenant_id and pos.id = pa.position_id
		join hr_fixed_compensation fc on fc.tenant_id = e.tenant_id and fc.employment_id = e.id and fc.validity @> $3::timestamptz
		join hr_compensation_plan pl on pl.tenant_id = fc.tenant_id and pl.id = fc.plan_id
		where e.tenant_id = $1 and e.worker_id = $2 and e.assignment_kind = 'PRIMARY' and e.validity @> $3::timestamptz
		order by e.valid_from limit 1`, tenantID, workerID, at).Scan(&f.employmentID, &f.legalEntityID, &f.departmentID, &f.jobCode, &f.planID, &amount, &basis, &currency)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, &domain.Error{Code: CodeCompNoEmploy, Detail: "worker " + workerID + " has no primary employment with a position and compensation on the effective date"}, nil
	}
	if err != nil {
		return f, nil, err
	}
	if basis != "ANNUAL" || currency != c.Currency {
		return f, &domain.Error{Code: CodeCompNotAnnual, Detail: "worker " + workerID + " is not paid an annual amount in the cycle currency"}, nil
	}
	f.pay = decimal.RequireFromString(amount)
	return f, nil, nil
}

func (s *Postgres) SaveWorksheet(ctx context.Context, cycleID, managerID string, in []WorksheetRowInput) ([]WorksheetRow, error) {
	p := principal(ctx)
	var out []WorksheetRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := loadCycle(ctx, tx, p.TenantID, cycleID, true)
		if err != nil {
			return err
		}
		if c.Status != cycleManagerInput {
			return problem(http.StatusConflict, CodeCycleState, "worksheets can be saved only while the cycle is in MANAGER_INPUT")
		}
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from worker where tenant_id = $1 and id = $2)`, p.TenantID, managerID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return problem(http.StatusNotFound, CodeWorkerNotFound, "manager not found")
		}
		at, err := time.Parse("2006-01-02", c.EffectiveDate)
		if err != nil {
			return err
		}
		places := money.MinorUnits(c.Currency)
		for _, r := range in {
			f, de, err := workerPayFacts(ctx, tx, p.TenantID, r.WorkerID, c, at)
			if err != nil {
				return err
			}
			if de != nil {
				return problem(http.StatusUnprocessableEntity, de.Code, de.Detail)
			}
			pct := decimal.RequireFromString(r.ProposedPct)
			var compa *string
			var lo, hi *string
			var jobMin, jobMax *string
			err = tx.QueryRow(ctx, `select pay_min::text, pay_max::text from hr_job where tenant_id = $1 and code = $2 and pay_currency = $3`, p.TenantID, f.jobCode, c.Currency).Scan(&jobMin, &jobMax)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if jobMin != nil {
				ratio, ok := domain.CompaRatio(f.pay, decimal.RequireFromString(*jobMin), decimal.RequireFromString(*jobMax))
				if ok {
					text := ratio.StringFixed(2)
					compa = &text
					if cell, found := domain.GuidelineFor(c.GuidelineMatrix, r.Rating, ratio); found {
						a, b := cell.PctMin.String(), cell.PctMax.String()
						lo, hi = &a, &b
					}
				}
			}
			tag, err := tx.Exec(ctx, `insert into hr_comp_worksheet_row (tenant_id, cycle_id, worker_id, manager_worker_id, employment_id, department_id, legal_entity_id, current_pay, rating, compa_ratio,
					guideline_pct_min, guideline_pct_max, proposed_pct, proposed_amount, justification, status)
				values ($1, $2, $3, $4, $5, $6, $15, $7::numeric, $8, $9::numeric, $10::numeric, $11::numeric, $12::numeric, $13::numeric, $14, 'DRAFT')
				on conflict (tenant_id, cycle_id, worker_id) do update set manager_worker_id = excluded.manager_worker_id, employment_id = excluded.employment_id,
					department_id = excluded.department_id, legal_entity_id = excluded.legal_entity_id, current_pay = excluded.current_pay, rating = excluded.rating, compa_ratio = excluded.compa_ratio,
					guideline_pct_min = excluded.guideline_pct_min, guideline_pct_max = excluded.guideline_pct_max, proposed_pct = excluded.proposed_pct,
					proposed_amount = excluded.proposed_amount, justification = excluded.justification
				where hr_comp_worksheet_row.status = 'DRAFT'`,
				p.TenantID, cycleID, r.WorkerID, managerID, f.employmentID, f.departmentID, f.pay.String(), r.Rating, compa, lo, hi, pct.String(),
				domain.ProposedAmount(f.pay, pct, places).String(), r.Justification, f.legalEntityID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return problem(http.StatusConflict, CodeCycleState, "worker "+r.WorkerID+" is already on a submitted worksheet")
			}
		}
		out, err = listWorksheet(ctx, tx, p.TenantID, cycleID, managerID)
		return err
	})
	return out, err
}

func (s *Postgres) SubmitWorksheet(ctx context.Context, cycleID, managerID string, onlyDirectReports bool) ([]WorksheetRow, error) {
	p := principal(ctx)
	var out []WorksheetRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := loadCycle(ctx, tx, p.TenantID, cycleID, true)
		if err != nil {
			return err
		}
		if c.Status != cycleManagerInput {
			return problem(http.StatusConflict, CodeCycleState, "worksheets can be submitted only while the cycle is in MANAGER_INPUT")
		}
		rows, err := listWorksheet(ctx, tx, p.TenantID, cycleID, managerID)
		if err != nil {
			return err
		}
		var drafts []string
		for _, r := range rows {
			if r.Status == worksheetDraft {
				drafts = append(drafts, r.WorkerID)
			}
		}
		var onlyWorkers []string
		reports := map[string]bool{}
		if onlyDirectReports {
			found, err := directReportsTx(ctx, tx, p.TenantID, managerID, drafts, s.now().UTC(), s.depth)
			if err != nil {
				return err
			}
			for _, id := range found {
				reports[id] = true
			}
			onlyWorkers = append([]string{}, found...)
		}
		mine := rows[:0:0]
		for _, r := range rows {
			if r.Status == worksheetDraft && (!onlyDirectReports || reports[r.WorkerID]) {
				mine = append(mine, r)
			}
		}
		if len(mine) == 0 {
			return problem(http.StatusUnprocessableEntity, CodeWorksheetEmpty, "the worksheet has no draft rows to submit")
		}
		for _, r := range mine {
			if r.Justification != nil {
				continue
			}
			pct := decimal.RequireFromString(r.ProposedPct)
			var cell domain.GuidelineRow
			found := r.GuidelinePctMin != nil
			if found {
				cell = domain.GuidelineRow{PctMin: decimal.RequireFromString(*r.GuidelinePctMin), PctMax: decimal.RequireFromString(*r.GuidelinePctMax)}
			}
			if domain.OutsideGuideline(pct, cell, found) {
				return problem(http.StatusUnprocessableEntity, domain.CodeCompOutsideGuideline,
					fmt.Sprintf("proposed %s %% for worker %s is outside the guideline; a justification is required", pct.String(), r.WorkerID))
			}
		}
		totals := map[string]decimal.Decimal{}
		for _, r := range mine {
			totals[r.DepartmentID] = totals[r.DepartmentID].Add(decimal.RequireFromString(r.ProposedAmount))
		}
		departments := make([]string, 0, len(totals))
		for d := range totals {
			departments = append(departments, d)
		}
		sort.Strings(departments)
		for _, d := range departments {
			var others, budget string
			var err error
			others, err = withAllEntities(ctx, tx, func() (string, error) {
				var sum string
				err := tx.QueryRow(ctx, `select coalesce(sum(proposed_amount), 0)::text from hr_comp_worksheet_row
					where tenant_id = $1 and cycle_id = $2 and department_id = $3 and status = 'SUBMITTED'`, p.TenantID, cycleID, d).Scan(&sum)
				return sum, err
			})
			if err != nil {
				return err
			}
			err = tx.QueryRow(ctx, `select budget_amount::text from hr_comp_cycle_budget where tenant_id = $1 and cycle_id = $2 and department_id = $3`, p.TenantID, cycleID, d).Scan(&budget)
			if errors.Is(err, pgx.ErrNoRows) {
				budget = "0"
			} else if err != nil {
				return err
			}
			total := totals[d].Add(decimal.RequireFromString(others))
			if err := domain.CheckDepartmentBudget(total, decimal.RequireFromString(budget), c.AllowOverBudget, d); err != nil {
				return problem(http.StatusUnprocessableEntity, domain.CodeCompOverBudget, err.(*domain.Error).Detail)
			}
		}
		if _, err := tx.Exec(ctx, `update hr_comp_worksheet_row set status = 'SUBMITTED' where tenant_id = $1 and cycle_id = $2 and manager_worker_id = $3 and status = 'DRAFT'
				and ($4::uuid[] is null or worker_id = any($4))`,
			p.TenantID, cycleID, managerID, onlyWorkers); err != nil {
			return err
		}
		out, err = listWorksheet(ctx, tx, p.TenantID, cycleID, managerID)
		return err
	})
	return out, err
}

func (s *Postgres) ApplyCompCycle(ctx context.Context, cycleID string) (CompCycle, []string, error) {
	p := principal(ctx)
	var out CompCycle
	var actionIDs []string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		c, err := loadCycle(ctx, tx, p.TenantID, cycleID, true)
		if err != nil {
			return err
		}
		if c.Status != cycleApproval {
			return problem(http.StatusConflict, CodeCycleState, "a cycle is applied from APPROVAL; this one is "+c.Status)
		}
		if all, err := scopeIsAll(ctx, tx); err != nil {
			return err
		} else if !all {
			return problem(http.StatusForbidden, "AUTH-0003", "applying a cycle needs access to every legal entity")
		}
		at, err := time.Parse("2006-01-02", c.EffectiveDate)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, worksheetSelect+` where tenant_id = $1 and cycle_id = $2 and status = 'SUBMITTED' order by worker_id`, p.TenantID, cycleID)
		if err != nil {
			return err
		}
		var submitted []WorksheetRow
		for rows.Next() {
			r, err := scanWorksheetRow(rows)
			if err != nil {
				rows.Close()
				return err
			}
			submitted = append(submitted, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		totals := map[string]decimal.Decimal{}
		for _, r := range submitted {
			totals[r.DepartmentID] = totals[r.DepartmentID].Add(decimal.RequireFromString(r.ProposedAmount))
		}
		for d, total := range totals {
			budget := decimal.Zero
			var text string
			err := tx.QueryRow(ctx, `select budget_amount::text from hr_comp_cycle_budget where tenant_id = $1 and cycle_id = $2 and department_id = $3`, p.TenantID, cycleID, d).Scan(&text)
			if err == nil {
				budget = decimal.RequireFromString(text)
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err := domain.CheckDepartmentBudget(total, budget, c.AllowOverBudget, d); err != nil {
				return problem(http.StatusUnprocessableEntity, domain.CodeCompOverBudget, err.(*domain.Error).Detail)
			}
		}
		places := money.MinorUnits(c.Currency)
		actionIDs = []string{}
		for _, r := range submitted {
			f, de, err := workerPayFacts(ctx, tx, p.TenantID, r.WorkerID, c, at)
			if err != nil {
				return err
			}
			if de != nil {
				return problem(http.StatusUnprocessableEntity, de.Code, de.Detail)
			}
			if !f.pay.Equal(decimal.RequireFromString(r.CurrentPay)) {
				return problem(http.StatusConflict, CodeCompStale, "worker "+r.WorkerID+" pay changed since the worksheet was saved")
			}
			newPay := f.pay.Add(decimal.RequireFromString(r.ProposedAmount)).Round(places)
			employmentID, kind, src := f.employmentID, sourceCompCycle, cycleID
			act, err := s.submitActionTx(ctx, tx, p, ActionInput{
				ActionType: hrevents.ActionCompChange, WorkerID: r.WorkerID, EmploymentID: &employmentID, EffectiveDate: at,
				Payload: ActionPayload{CompPlanID: f.planID, Amount: newPay.StringFixed(places), Currency: c.Currency},
				Reason:  "compensation cycle " + c.Name, SourceKind: &kind, SourceID: &src,
			}, nil)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update hr_comp_worksheet_row set action_id = $4 where tenant_id = $1 and cycle_id = $2 and worker_id = $3`, p.TenantID, cycleID, r.WorkerID, act.ID); err != nil {
				return err
			}
			actionIDs = append(actionIDs, act.ID)
		}
		if _, err := tx.Exec(ctx, `update hr_comp_cycle set status = 'APPLIED', applied_at = now(), updated_at = now() where tenant_id = $1 and id = $2`, p.TenantID, cycleID); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "compensationcycle", cycleID, hrevents.CompensationCycleApplied, p.TenantID, hrevents.CompensationCycleAppliedData{
			CycleID: cycleID, EffectiveDate: c.EffectiveDate, ActionIDs: actionIDs,
		}); err != nil {
			return err
		}
		out, err = loadCycle(ctx, tx, p.TenantID, cycleID, false)
		return err
	})
	return out, actionIDs, err
}
