package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

func (s *Postgres) PutTemplate(ctx context.Context, in domain.Template) (domain.Template, error) {
	tasks := make([]domain.TaskTemplate, len(in.Tasks))
	for i, t := range in.Tasks {
		tasks[i] = domain.TaskTemplate{ID: t.Key, Name: t.Name, DependsOn: t.DependsOn}
	}
	if err := domain.ValidateDAG(tasks); err != nil {
		return domain.Template{}, err
	}
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return domain.Template{}, errors.New("put template: no principal")
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `insert into close_template(tenant_id, id, name, version, updated_by) values(current_setting('passage.tenant_id')::uuid, $1, $2, 1, $3)
			on conflict (tenant_id, id) do update set name = excluded.name, version = close_template.version + 1, updated_by = excluded.updated_by, updated_at = now()`,
			in.ID, in.Name, p.UserID)
		if err != nil {
			return err
		}
		_ = tag
		if _, err := tx.Exec(ctx, `delete from close_template_task where template_id = $1`, in.ID); err != nil {
			return err
		}
		for i, t := range in.Tasks {
			deps := t.DependsOn
			if deps == nil {
				deps = []string{}
			}
			if _, err := tx.Exec(ctx, `insert into close_template_task(tenant_id, template_id, task_key, name, description, module, assignee_role, due_offset_days, depends_on, evidence_required, manual_override_allowed, position)
				values(current_setting('passage.tenant_id')::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
				in.ID, t.Key, t.Name, t.Description, t.Module, t.AssigneeRole, t.DueOffsetDays, deps, t.EvidenceRequired, t.ManualOverride, i); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `select version from close_template where id = $1`, in.ID).Scan(&in.Version)
	})
	if err != nil {
		return domain.Template{}, fmt.Errorf("put template: %w", err)
	}
	return in, nil
}

func (s *Postgres) GetTemplate(ctx context.Context, id string) (domain.Template, error) {
	var out domain.Template
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `select id::text, name, version from close_template where id = $1`, id).Scan(&out.ID, &out.Name, &out.Version); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select task_key, name, description, module, assignee_role, due_offset_days, depends_on, evidence_required, manual_override_allowed from close_template_task where template_id = $1 order by position`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t domain.TemplateTask
			if err := rows.Scan(&t.Key, &t.Name, &t.Description, &t.Module, &t.AssigneeRole, &t.DueOffsetDays, &t.DependsOn, &t.EvidenceRequired, &t.ManualOverride); err != nil {
				return err
			}
			out.Tasks = append(out.Tasks, t)
		}
		return rows.Err()
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, &domain.Error{Code: domain.CodeNoRun, Detail: "template not found"}
	}
	if err != nil {
		return out, fmt.Errorf("get template: %w", err)
	}
	return out, nil
}

func (s *Postgres) CreateRun(ctx context.Context, templateID, fiscalPeriodID, actor string, entities []string, key, hash string) (domain.CloseRun, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return domain.CloseRun{}, errors.New("create run: no principal")
	}
	tmpl, err := s.GetTemplate(ctx, templateID)
	if err != nil {
		return domain.CloseRun{}, err
	}
	specs := make([]domain.TaskTemplate, len(tmpl.Tasks))
	byKey := map[string]domain.TemplateTask{}
	for i, t := range tmpl.Tasks {
		specs[i] = domain.TaskTemplate{ID: t.Key, Name: t.Name, DependsOn: t.DependsOn}
		byKey[t.Key] = t
	}
	tasks, err := domain.Instantiate(specs, entities)
	if err != nil {
		return domain.CloseRun{}, err
	}
	var run domain.CloseRun
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var periodEnd time.Time
		if err := tx.QueryRow(ctx, `select end_date from period_status where fiscal_period_id = $1 and end_date is not null limit 1`, fiscalPeriodID).Scan(&periodEnd); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return &domain.Error{Code: domain.CodeNoPeriod, Detail: "period is not registered"}
			}
			return err
		}
		run.ID = ids.New()
		run.TemplateID, run.FiscalPeriodID, run.LegalEntityIDs, run.Status, run.CreatedBy = templateID, fiscalPeriodID, entities, "in_progress", actor
		run.PeriodEnd = periodEnd.Format("2006-01-02")
		run.TaskCount = len(tasks)
		tag, err := tx.Exec(ctx, `insert into close_run(tenant_id, id, template_id, template_version, fiscal_period_id, period_end, legal_entity_ids, status, created_by, idempotency_key, request_hash)
			values(current_setting('passage.tenant_id')::uuid, $1, $2, $3, $4, $5, $6::uuid[], 'in_progress', $7, $8, $9)
			on conflict (tenant_id, idempotency_key) where idempotency_key <> '' do nothing`,
			run.ID, templateID, tmpl.Version, fiscalPeriodID, periodEnd, entities, actor, key, hash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var storedHash string
			if err := tx.QueryRow(ctx, `select id::text, template_id::text, fiscal_period_id::text, period_end::text, legal_entity_ids, status, created_by, request_hash from close_run where idempotency_key = $1`, key).
				Scan(&run.ID, &run.TemplateID, &run.FiscalPeriodID, &run.PeriodEnd, &run.LegalEntityIDs, &run.Status, &run.CreatedBy, &storedHash); err != nil {
				return err
			}
			if storedHash != hash {
				return reuse("Idempotency-Key was already used with a different request")
			}
			return nil
		}
		idByLogical := map[string]string{}
		type row struct {
			task domain.Task
			id   string
			due  time.Time
		}
		inserted := make([]row, 0, len(tasks))
		for _, task := range tasks {
			spec := byKey[task.TemplateID]
			id := ids.New()
			due := periodEnd.AddDate(0, 0, spec.DueOffsetDays).Add(23*time.Hour + 59*time.Minute)
			idByLogical[task.ID] = id
			inserted = append(inserted, row{task: task, id: id, due: due})
		}
		for _, row := range inserted {
			spec := byKey[row.task.TemplateID]
			deps := make([]string, len(row.task.DependsOn))
			for i, d := range row.task.DependsOn {
				deps[i] = idByLogical[d]
			}
			if _, err := tx.Exec(ctx, `insert into close_task(tenant_id, id, run_id, legal_entity_id, task_key, module, name, description, assignee_role, due_at, depends_on, status, evidence_required, manual_override_allowed)
				values(current_setting('passage.tenant_id')::uuid, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10::uuid[], 'not_started', $11, $12)`,
				row.id, run.ID, row.task.Entity, spec.Key, spec.Module, spec.Name, spec.Description, spec.AssigneeRole, row.due, deps, spec.EvidenceRequired, spec.ManualOverride); err != nil {
				return err
			}
		}
		evt, err := events.New(domain.EventCloseRunCreated, eventSource, run.ID, p.TenantID, domain.RunCreated{
			RunID: run.ID, TemplateID: templateID, FiscalPeriodID: fiscalPeriodID, LegalEntityIDs: entities, TaskCount: run.TaskCount,
		})
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, "close_run", run.ID, evt)
	})
	if err != nil {
		return domain.CloseRun{}, fmt.Errorf("create close run: %w", err)
	}
	return run, nil
}

func (s *Postgres) ListTasks(ctx context.Context, runID, entity, status string) ([]domain.BoardTask, error) {
	var out []domain.BoardTask
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, run_id::text, legal_entity_id::text, task_key, module, name, assignee_role, coalesce(assignee_user_id,''), due_at, depends_on, status, coalesce(evidence_uri,''), coalesce(skip_reason,'')
			from close_task where run_id = $1 and ($2 = '' or legal_entity_id = $2::uuid) and ($3 = '' or status = $3) order by due_at, task_key`, runID, entity, status)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t domain.BoardTask
			if err := rows.Scan(&t.ID, &t.RunID, &t.LegalEntityID, &t.TaskKey, &t.Module, &t.Name, &t.AssigneeRole, &t.AssigneeUserID, &t.DueAt, &t.DependsOn, &t.Status, &t.EvidenceURI, &t.SkipReason); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list close tasks: %w", err)
	}
	return out, nil
}

func (s *Postgres) StartTask(ctx context.Context, taskID, actor string) (domain.BoardTask, error) {
	return s.mutateTask(ctx, taskID, actor, func(tasks []domain.Task, id string) error {
		return domain.Eligible(tasks, id)
	}, func(ctx context.Context, tx pgx.Tx, taskID, actor string) error {
		_, err := tx.Exec(ctx, `update close_task set status = 'in_progress', started_at = now() where id = $1 and status = 'not_started'`, taskID)
		return err
	})
}

func (s *Postgres) CompleteTask(ctx context.Context, taskID, actor, evidence string) (domain.BoardTask, error) {
	return s.mutateTask(ctx, taskID, actor, func(tasks []domain.Task, id string) error {
		return domain.Complete(tasks, id)
	}, func(ctx context.Context, tx pgx.Tx, taskID, actor string) error {
		_, err := tx.Exec(ctx, `update close_task set status = 'completed', completed_at = now(), completed_by = $2, evidence_uri = nullif($3,'') where id = $1`, taskID, actor, evidence)
		return err
	})
}

func (s *Postgres) SkipTask(ctx context.Context, taskID, actor, reason string) (domain.BoardTask, error) {
	return s.mutateTask(ctx, taskID, actor, func(tasks []domain.Task, id string) error {
		return domain.Skip(tasks, id, reason)
	}, func(ctx context.Context, tx pgx.Tx, taskID, actor string) error {
		_, err := tx.Exec(ctx, `update close_task set status = 'skipped', completed_at = now(), completed_by = $2, skip_reason = $3 where id = $1`, taskID, actor, reason)
		return err
	})
}

func (s *Postgres) ReassignTask(ctx context.Context, taskID, actor, assignee string) (domain.BoardTask, error) {
	var out domain.BoardTask
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `update close_task set assignee_user_id = $2 where id = $1`, taskID, assignee); err != nil {
			return err
		}
		var err error
		out, err = scanBoard(tx.QueryRow(ctx, boardSelect+` where id = $1`, taskID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, &domain.Error{Code: domain.CodeNoRun, Detail: "task not found"}
	}
	return out, err
}

func (s *Postgres) mutateTask(ctx context.Context, taskID, actor string, apply func([]domain.Task, string) error, write func(context.Context, pgx.Tx, string, string) error) (domain.BoardTask, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return domain.BoardTask{}, errors.New("task action: no principal")
	}
	var out domain.BoardTask
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var runID string
		if err := tx.QueryRow(ctx, `select run_id::text from close_task where id = $1`, taskID).Scan(&runID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select id::text, depends_on, status from close_task where run_id = $1`, runID)
		if err != nil {
			return err
		}
		var tasks []domain.Task
		for rows.Next() {
			var t domain.Task
			var status string
			if err := rows.Scan(&t.ID, &t.DependsOn, &status); err != nil {
				rows.Close()
				return err
			}
			t.Status = mapTaskStatus(status)
			tasks = append(tasks, t)
		}
		rows.Close()
		if err := apply(tasks, taskID); err != nil {
			return err
		}
		if err := write(ctx, tx, taskID, actor); err != nil {
			return err
		}
		out, err = scanBoard(tx.QueryRow(ctx, boardSelect+` where id = $1`, taskID))
		if err != nil {
			return err
		}
		if out.Status != "completed" && out.Status != "skipped" {
			return nil
		}
		evt, err := events.New(domain.EventCloseTaskCompleted, eventSource, out.ID, p.TenantID, domain.TaskCompletedEvent{
			RunID: out.RunID, TaskID: out.ID, Status: out.Status, Actor: actor, Late: time.Now().After(out.DueAt),
		})
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, "close_task", out.ID, evt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, &domain.Error{Code: domain.CodeNoRun, Detail: "task not found"}
	}
	return out, err
}

func mapTaskStatus(status string) domain.TaskStatus {
	switch status {
	case "completed":
		return domain.TaskCompleted
	case "skipped":
		return domain.TaskSkipped
	default:
		return domain.TaskPending
	}
}

const boardSelect = `select id::text, run_id::text, legal_entity_id::text, task_key, module, name, assignee_role, coalesce(assignee_user_id,''), due_at, depends_on, status, coalesce(evidence_uri,''), coalesce(skip_reason,'') from close_task`

func scanBoard(row pgx.Row) (domain.BoardTask, error) {
	var t domain.BoardTask
	err := row.Scan(&t.ID, &t.RunID, &t.LegalEntityID, &t.TaskKey, &t.Module, &t.Name, &t.AssigneeRole, &t.AssigneeUserID, &t.DueAt, &t.DependsOn, &t.Status, &t.EvidenceURI, &t.SkipReason)
	return t, err
}
