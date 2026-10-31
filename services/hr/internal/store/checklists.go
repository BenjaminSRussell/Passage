package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
)

const (
	CodeTemplateNotFound  = "HCM-CHK-TEMPLATE-NOT-FOUND"
	CodeTemplateDuplicate = "HCM-CHK-TEMPLATE-DUPLICATE"
	CodeChecklistNotFound = "HCM-CHK-NOT-FOUND"
	CodeTaskNotFound      = "HCM-CHK-TASK-NOT-FOUND"
	CodeChecklistState    = "HCM-CHK-STATE"
	CodeTaskEvidence      = "HCM-CHK-EVIDENCE"
	CodeOpenTasks         = "HCM-CHK-OPEN-TASKS"
)

type TemplateTask struct {
	Code             string  `json:"code"`
	Title            string  `json:"title"`
	OwnerRole        string  `json:"ownerRole"`
	DueOffsetDays    int     `json:"dueOffsetDays"`
	Instructions     *string `json:"instructions,omitempty"`
	EvidenceRequired bool    `json:"evidenceRequired"`
}

type ChecklistTemplate struct {
	ID        string         `json:"id"`
	Code      string         `json:"code"`
	Kind      string         `json:"kind"`
	Name      string         `json:"name"`
	Tasks     []TemplateTask `json:"tasks"`
	CreatedAt time.Time      `json:"createdAt"`
}

type WorkerTask struct {
	ID               string     `json:"id"`
	ChecklistID      string     `json:"checklistId"`
	WorkerID         string     `json:"workerId"`
	TaskCode         string     `json:"taskCode"`
	Title            string     `json:"title"`
	OwnerRole        string     `json:"ownerRole"`
	OwnerWorkerID    *string    `json:"ownerWorkerId,omitempty"`
	DueOn            string     `json:"dueOn"`
	EvidenceRequired bool       `json:"evidenceRequired"`
	Status           string     `json:"status"`
	EvidenceRef      *string    `json:"evidenceRef,omitempty"`
	CompletedBy      *string    `json:"completedBy,omitempty"`
	CompletedAt      *time.Time `json:"completedAt,omitempty"`
}

type Checklist struct {
	ID            string       `json:"id"`
	WorkerID      string       `json:"workerId"`
	TemplateID    string       `json:"templateId"`
	Kind          string       `json:"kind"`
	EffectiveDate string       `json:"effectiveDate"`
	Status        string       `json:"status"`
	CompletedAt   *time.Time   `json:"completedAt,omitempty"`
	Tasks         []WorkerTask `json:"tasks"`
}

const taskColumns = `id::text, checklist_id::text, worker_id::text, task_code, title, owner_role, owner_worker_id::text, to_char(due_on, 'YYYY-MM-DD'),
	evidence_required, status, evidence_ref, completed_by, completed_at`

func scanTask(row pgx.Row) (WorkerTask, error) {
	var t WorkerTask
	err := row.Scan(&t.ID, &t.ChecklistID, &t.WorkerID, &t.TaskCode, &t.Title, &t.OwnerRole, &t.OwnerWorkerID, &t.DueOn, &t.EvidenceRequired,
		&t.Status, &t.EvidenceRef, &t.CompletedBy, &t.CompletedAt)
	t.CompletedAt = utcPtr(t.CompletedAt)
	return t, err
}

const checklistColumns = `id::text, worker_id::text, template_id::text, kind, to_char(effective_date, 'YYYY-MM-DD'), status, completed_at`

func scanChecklist(row pgx.Row) (Checklist, error) {
	var c Checklist
	err := row.Scan(&c.ID, &c.WorkerID, &c.TemplateID, &c.Kind, &c.EffectiveDate, &c.Status, &c.CompletedAt)
	c.CompletedAt = utcPtr(c.CompletedAt)
	c.Tasks = []WorkerTask{}
	return c, err
}

func checklistTasks(ctx context.Context, tx pgx.Tx, tenantID, checklistID string) ([]WorkerTask, error) {
	rows, err := tx.Query(ctx, `select `+taskColumns+` from hr_worker_task where tenant_id = $1 and checklist_id = $2 order by due_on, task_code`, tenantID, checklistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkerTask{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func loadChecklist(ctx context.Context, tx pgx.Tx, tenantID, where string, args ...any) (Checklist, error) {
	c, err := scanChecklist(tx.QueryRow(ctx, `select `+checklistColumns+` from hr_checklist where tenant_id = $1 and `+where, append([]any{tenantID}, args...)...))
	if err != nil {
		return c, err
	}
	c.Tasks, err = checklistTasks(ctx, tx, tenantID, c.ID)
	return c, err
}

func checklistEvent(c Checklist) hrevents.ChecklistChangedData {
	return hrevents.ChecklistChangedData{ID: c.ID, WorkerID: c.WorkerID, Kind: c.Kind, EffectiveDate: c.EffectiveDate, Status: c.Status, TaskCount: len(c.Tasks)}
}

func loadTemplate(ctx context.Context, tx pgx.Tx, tenantID, where string, args ...any) (ChecklistTemplate, error) {
	var t ChecklistTemplate
	err := tx.QueryRow(ctx, `select id::text, code, kind, name, created_at from hr_checklist_template where tenant_id = $1 and `+where,
		append([]any{tenantID}, args...)...).Scan(&t.ID, &t.Code, &t.Kind, &t.Name, &t.CreatedAt)
	if err != nil {
		return t, err
	}
	t.CreatedAt = utc(t.CreatedAt)
	t.Tasks = []TemplateTask{}
	rows, err := tx.Query(ctx, `select code, title, owner_role, due_offset_days, instructions, evidence_required from hr_checklist_template_task
		where tenant_id = $1 and template_id = $2 order by position`, tenantID, t.ID)
	if err != nil {
		return t, err
	}
	defer rows.Close()
	for rows.Next() {
		var k TemplateTask
		if err := rows.Scan(&k.Code, &k.Title, &k.OwnerRole, &k.DueOffsetDays, &k.Instructions, &k.EvidenceRequired); err != nil {
			return t, err
		}
		t.Tasks = append(t.Tasks, k)
	}
	return t, rows.Err()
}

// CreateChecklistTemplate stores a template and its tasks (FR-42.19). A replayed
// Idempotency-Key returns the stored template.
func (s *Postgres) CreateChecklistTemplate(ctx context.Context, in ChecklistTemplate, idempotencyKey string) (ChecklistTemplate, bool, error) {
	p := principal(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out ChecklistTemplate
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := loadTemplate(ctx, tx, p.TenantID, `idempotency_key = $2`, *key)
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into hr_checklist_template (tenant_id, id, code, kind, name, idempotency_key) values ($1, $2, $3, $4, $5, $6)`,
			p.TenantID, id, in.Code, in.Kind, in.Name, key); err != nil {
			return err
		}
		for i, k := range in.Tasks {
			if _, err := tx.Exec(ctx, `insert into hr_checklist_template_task (tenant_id, template_id, code, position, title, owner_role, due_offset_days, instructions, evidence_required)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, p.TenantID, id, k.Code, i, k.Title, k.OwnerRole, k.DueOffsetDays, k.Instructions, k.EvidenceRequired); err != nil {
				return err
			}
		}
		var err error
		out, err = loadTemplate(ctx, tx, p.TenantID, `id = $2`, id)
		inserted = err == nil
		return err
	})
	if idempotencyRace(err) {
		return ChecklistTemplate{}, false, idempotencyInFlight()
	}
	if pgCode(err, uniqueViolation) {
		return ChecklistTemplate{}, false, problem(http.StatusConflict, CodeTemplateDuplicate, "a checklist template with this code already exists")
	}
	if err == nil && !inserted && (out.Code != in.Code || out.Kind != in.Kind || len(out.Tasks) != len(in.Tasks)) {
		return ChecklistTemplate{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

// ListChecklistTemplates returns the templates, optionally of one kind, by code.
func (s *Postgres) ListChecklistTemplates(ctx context.Context, kind string) ([]ChecklistTemplate, error) {
	p := principal(ctx)
	out := []ChecklistTemplate{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text from hr_checklist_template where tenant_id = $1 and ($2 = '' or kind = $2) order by code limit 200`, p.TenantID, kind)
		if err != nil {
			return err
		}
		var tids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			tids = append(tids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range tids {
			t, err := loadTemplate(ctx, tx, p.TenantID, `id = $2`, id)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return nil
	})
	return out, err
}

// StartChecklist instantiates a template for a worker on an action's effective
// date (BR-42.12): each task is due at the date plus its offset and is owned by
// the worker holding the role as-of that date (MANAGER by BR-42.1, WORKER the
// worker; HR_PARTNER and IT need the department and role mappings and stay
// unassigned). An OFFBOARDING checklist gets the RETURN_LOANED_ITEMS task. Starting
// the same template for the same date again returns the stored checklist.
func (s *Postgres) StartChecklist(ctx context.Context, workerID, templateID string, effective time.Time, legalEntityID *string) (Checklist, bool, error) {
	p := principal(ctx)
	var out Checklist
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from worker where tenant_id = $1 and id = $2)`, p.TenantID, workerID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
		}
		tpl, err := loadTemplate(ctx, tx, p.TenantID, `id = $2`, templateID)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeTemplateNotFound, "checklist template not found")
		}
		if err != nil {
			return err
		}
		existing, err := loadChecklist(ctx, tx, p.TenantID, `worker_id = $2 and template_id = $3 and effective_date = $4::date`, workerID, templateID, effective)
		if err == nil {
			out = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var manager *string
		var position string
		err = tx.QueryRow(ctx, `select position_id::text from position_assignment where tenant_id = $1 and worker_id = $2 and is_primary and validity @> $3::timestamptz`,
			p.TenantID, workerID, effective).Scan(&position)
		if err == nil {
			if manager, err = s.manager(ctx, tx, p.TenantID, workerID, position, effective); err != nil {
				return err
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		entity, err := workerEntity(ctx, tx, p.TenantID, workerID, effective.Format("2006-01-02"), legalEntityID)
		if err != nil {
			return err
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into hr_checklist (tenant_id, id, worker_id, legal_entity_id, template_id, kind, effective_date, created_by) values ($1, $2, $3, $8, $4, $5, $6::date, $7)`,
			p.TenantID, id, workerID, templateID, tpl.Kind, effective, p.UserID, entity); err != nil {
			return err
		}
		tasks := tpl.Tasks
		if tpl.Kind == domain.ChecklistOffboarding {
			tasks = append(append([]TemplateTask{}, tasks...), TemplateTask{Code: domain.ReturnLoanedItemsCode, Title: domain.ReturnLoanedItemsTitle, OwnerRole: domain.RoleHRPartner})
		}
		for _, k := range tasks {
			var owner *string
			switch k.OwnerRole {
			case domain.RoleWorker:
				owner = &workerID
			case domain.RoleManager:
				owner = manager
			}
			if _, err := tx.Exec(ctx, `insert into hr_worker_task (tenant_id, id, checklist_id, worker_id, legal_entity_id, task_code, title, owner_role, owner_worker_id, due_on, evidence_required)
				values ($1, $2, $3, $4, $11, $5, $6, $7, $8, $9::date, $10)`, p.TenantID, ids.New(), id, workerID, k.Code, k.Title, k.OwnerRole, owner,
				domain.TaskDueOn(effective, k.DueOffsetDays), k.EvidenceRequired, entity); err != nil {
				return err
			}
		}
		if out, err = loadChecklist(ctx, tx, p.TenantID, `id = $2`, id); err != nil {
			return err
		}
		inserted = true
		return enqueue(ctx, tx, "checklist", out.ID, hrevents.ChecklistChanged, p.TenantID, checklistEvent(out))
	})
	if pgCode(err, rlsViolation) {
		return Checklist{}, false, problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
	}
	return out, inserted, err
}

// GetChecklist returns a checklist with its tasks.
func (s *Postgres) GetChecklist(ctx context.Context, id string) (Checklist, error) {
	p := principal(ctx)
	var out Checklist
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = loadChecklist(ctx, tx, p.TenantID, `id = $2`, id)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Checklist{}, problem(http.StatusNotFound, CodeChecklistNotFound, "checklist not found")
	}
	return out, err
}

// ListWorkerChecklists returns a worker's checklists with their tasks, newest first.
func (s *Postgres) ListWorkerChecklists(ctx context.Context, workerID string) ([]Checklist, error) {
	p := principal(ctx)
	out := []Checklist{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text from hr_checklist where tenant_id = $1 and worker_id = $2 order by effective_date desc, id limit 100`, p.TenantID, workerID)
		if err != nil {
			return err
		}
		var cids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			cids = append(cids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range cids {
			c, err := loadChecklist(ctx, tx, p.TenantID, `id = $2`, id)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return nil
	})
	return out, err
}

// ListOwnedTasks returns the tasks a worker owns, optionally by status, soonest due first.
func (s *Postgres) ListOwnedTasks(ctx context.Context, ownerWorkerID, status string) ([]WorkerTask, error) {
	p := principal(ctx)
	out := []WorkerTask{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+taskColumns+` from hr_worker_task where tenant_id = $1 and owner_worker_id = $2 and ($3 = '' or status = $3)
			order by due_on, id limit $4`, p.TenantID, ownerWorkerID, status, maxCourseRows)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTask(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

func loansOutProblem(n int) error {
	return problem(http.StatusUnprocessableEntity, domain.CodeLoanedItemsOut, fmt.Sprintf("%d loaned items are still out for this worker; record returns or mark them lost", n))
}

// FinishTask marks a task DONE or SKIPPED. Evidence is required when the task
// says so. RETURN_LOANED_ITEMS can only be DONE, and only when no item is OUT
// (AC-42.15). Repeating the same outcome changes nothing.
func (s *Postgres) FinishTask(ctx context.Context, id, status string, evidenceRef *string) (WorkerTask, bool, error) {
	p := principal(ctx)
	var out WorkerTask
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanTask(tx.QueryRow(ctx, `select `+taskColumns+` from hr_worker_task where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeTaskNotFound, "task not found")
		}
		if err != nil {
			return err
		}
		if out.Status != domain.TaskOpen {
			if out.Status == status {
				return nil
			}
			return problem(http.StatusConflict, CodeChecklistState, "the task is already "+out.Status)
		}
		var checklistStatus string
		if err := tx.QueryRow(ctx, `select status from hr_checklist where tenant_id = $1 and id = $2`, p.TenantID, out.ChecklistID).Scan(&checklistStatus); err != nil {
			return err
		}
		if checklistStatus != domain.TaskOpen {
			return problem(http.StatusConflict, CodeChecklistState, "the checklist is already completed")
		}
		if status == domain.TaskDone && out.EvidenceRequired && (evidenceRef == nil || *evidenceRef == "") {
			return problem(http.StatusUnprocessableEntity, CodeTaskEvidence, "this task needs an evidence reference")
		}
		if out.TaskCode == domain.ReturnLoanedItemsCode {
			if status != domain.TaskDone {
				return problem(http.StatusUnprocessableEntity, domain.CodeLoanedItemsOut, "the loaned-items task cannot be skipped; return the items or mark them lost")
			}
			n, err := outstandingLoans(ctx, tx, p.TenantID, out.WorkerID)
			if err != nil {
				return err
			}
			if n > 0 {
				return loansOutProblem(n)
			}
		}
		out, err = scanTask(tx.QueryRow(ctx, `update hr_worker_task set status = $3, evidence_ref = $4, completed_by = $5, completed_at = now()
			where tenant_id = $1 and id = $2 returning `+taskColumns, p.TenantID, id, status, evidenceRef, p.UserID))
		changed = err == nil
		return err
	})
	return out, changed, err
}

// CompleteChecklist completes a checklist once every task is DONE or SKIPPED.
// An OFFBOARDING checklist also needs no item OUT, whatever its task says.
func (s *Postgres) CompleteChecklist(ctx context.Context, id string) (Checklist, bool, error) {
	p := principal(ctx)
	var out Checklist
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `select status from hr_checklist where tenant_id = $1 and id = $2 for update`, p.TenantID, id).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeChecklistNotFound, "checklist not found")
		}
		if err != nil {
			return err
		}
		if out, err = loadChecklist(ctx, tx, p.TenantID, `id = $2`, id); err != nil {
			return err
		}
		if status == domain.ChecklistCompleted {
			return nil
		}
		for _, t := range out.Tasks {
			if t.Status == domain.TaskOpen {
				return problem(http.StatusUnprocessableEntity, CodeOpenTasks, "task "+t.TaskCode+" is still open")
			}
		}
		if out.Kind == domain.ChecklistOffboarding {
			n, err := outstandingLoans(ctx, tx, p.TenantID, out.WorkerID)
			if err != nil {
				return err
			}
			if n > 0 {
				return loansOutProblem(n)
			}
		}
		if _, err := tx.Exec(ctx, `update hr_checklist set status = 'COMPLETED', completed_at = now() where tenant_id = $1 and id = $2`, p.TenantID, id); err != nil {
			return err
		}
		if out, err = loadChecklist(ctx, tx, p.TenantID, `id = $2`, id); err != nil {
			return err
		}
		changed = true
		return enqueue(ctx, tx, "checklist", out.ID, hrevents.ChecklistChanged, p.TenantID, checklistEvent(out))
	})
	return out, changed, err
}
