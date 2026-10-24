package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/batch/internal/domain"
	svcevents "github.com/BenjaminSRussell/passage/services/batch/internal/events"
	"github.com/BenjaminSRussell/passage/services/batch/internal/schema"
)

// Store is the Wave 04 persistence port.
type Store interface {
	Ping(ctx context.Context) error
	SaveDefinition(ctx context.Context, def Definition) (Definition, error)
	GetActiveDefinition(ctx context.Context, entity, legalEntityID string) (Definition, error)
	Submit(ctx context.Context, in Submit) (Instance, error)
	GetInstance(ctx context.Context, id string) (Instance, error)
	ListWorkItems(ctx context.Context, assignee, status string) ([]WorkItem, error)
	Act(ctx context.Context, workItemID string, act domain.Action) (WorkItem, error)
	SaveJob(ctx context.Context, job Job) (Job, error)
	GetJob(ctx context.Context, code string) (Job, error)
	StartRun(ctx context.Context, code string, rows int64, triggeredBy string) (Run, error)
	GetRun(ctx context.Context, id string) (Run, error)
	PublishTemplate(ctx context.Context, tpl Template, allowed map[string]bool) (Template, error)
	EnqueueMessage(ctx context.Context, msg Message) (Message, error)
	RetryMessage(ctx context.Context, id string) (Message, error)
	ListMessages(ctx context.Context, status string) ([]Message, error)
	PutPreference(ctx context.Context, pref Preference) (Preference, error)
	RegisterSchema(ctx context.Context, eventType string, body schema.JSONSchema) error
	GetSchema(ctx context.Context, eventType string) (schema.JSONSchema, error)
}

// Postgres implements Store.
type Postgres struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

type Definition struct {
	ID            string         `json:"id"`
	Entity        string         `json:"entity"`
	LegalEntityID string         `json:"legalEntityId"`
	Version       int            `json:"version"`
	Status        string         `json:"status"`
	Steps         []domain.Step  `json:"steps"`
	CommentReq    bool           `json:"commentRequired"`
	AdminUserID   string         `json:"adminUserId,omitempty"`
}

type Submit struct {
	Entity        string
	RecordID      string
	LegalEntityID string
	Assignees     []string
}

type Instance struct {
	ID          string `json:"id"`
	Entity      string `json:"entity"`
	RecordID    string `json:"recordId"`
	Status      string `json:"status"`
	SubmittedBy string `json:"submittedBy"`
	CurrentStep int    `json:"currentStep"`
}

type WorkItem struct {
	ID         string `json:"id"`
	InstanceID string `json:"instanceId"`
	Step       int    `json:"step"`
	AssigneeID string `json:"assigneeId"`
	Status     string `json:"status"`
	DueAt      string `json:"dueAt"`
}

type Job struct {
	ID             string `json:"id"`
	Code           string `json:"code"`
	Handler        string `json:"handler"`
	PartitionCount int    `json:"partitionCount"`
	Concurrency    int    `json:"concurrency"`
	Exclusive      bool   `json:"exclusive"`
	Status         string `json:"status"`
	Strategy       string `json:"partitionStrategy"`
	Priority       string `json:"priority"`
}

type Run struct {
	ID        string `json:"id"`
	JobID     string `json:"jobId"`
	Status    string `json:"status"`
	RowsTotal int64  `json:"rowsTotal"`
	RowsDone  int64  `json:"rowsDone"`
}

type Template struct {
	ID       string `json:"id"`
	Code     string `json:"code"`
	Channel  string `json:"channel"`
	Language string `json:"language"`
	Layer    string `json:"layer"`
	Version  int    `json:"version"`
	Subject  string `json:"subject"`
	Body     string `json:"body"`
	Status   string `json:"status"`
}

type Message struct {
	ID              string `json:"id"`
	Channel         string `json:"channel"`
	RecipientUserID string `json:"recipientUserId,omitempty"`
	RecipientAddr   string `json:"recipientAddress,omitempty"`
	TemplateCode    string `json:"templateCode,omitempty"`
	Subject         string `json:"subject"`
	Body            string `json:"body"`
	Status          string `json:"status"`
	Attempts        int    `json:"attempts"`
}

type Preference struct {
	UserID     string   `json:"userId"`
	EventClass string   `json:"eventClass"`
	Channels   []string `json:"channels"`
	Digest     string   `json:"digest"`
}

func principal(ctx context.Context) (tenant.Principal, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok || !ids.Valid(p.TenantID) {
		return tenant.Principal{}, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing tenant")
	}
	return p, nil
}

func (s *Postgres) SaveDefinition(ctx context.Context, def Definition) (Definition, error) {
	p, err := principal(ctx)
	if err != nil {
		return Definition{}, err
	}
	if def.ID == "" {
		def.ID = ids.New()
	}
	if def.Version == 0 {
		def.Version = 1
	}
	if def.Status == "" {
		def.Status = "active"
	}
	steps, err := json.Marshal(def.Steps)
	if err != nil {
		return Definition{}, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into workflow_definition
			(tenant_id, id, entity, legal_entity_id, version, status, comment_required, admin_user_id, steps)
			values ($1,$2,$3,$4,$5,$6,$7,nullif($8,'')::uuid,$9)`,
			p.TenantID, def.ID, def.Entity, def.LegalEntityID, def.Version, def.Status, def.CommentReq, def.AdminUserID, steps)
		return err
	})
	return def, err
}

func (s *Postgres) GetActiveDefinition(ctx context.Context, entity, legalEntityID string) (Definition, error) {
	p, err := principal(ctx)
	if err != nil {
		return Definition{}, err
	}
	var def Definition
	var steps []byte
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select id, entity, legal_entity_id, version, status, comment_required, coalesce(admin_user_id::text,''), steps
			from workflow_definition where tenant_id=$1 and entity=$2 and legal_entity_id=$3 and status='active'
			order by version desc limit 1`, p.TenantID, entity, legalEntityID).
			Scan(&def.ID, &def.Entity, &def.LegalEntityID, &def.Version, &def.Status, &def.CommentReq, &def.AdminUserID, &steps)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Definition{}, httpx.NewProblem(http.StatusNotFound, domain.CodeUnknown, "no active definition")
	}
	if err != nil {
		return Definition{}, err
	}
	_ = json.Unmarshal(steps, &def.Steps)
	return def, nil
}

func (s *Postgres) Submit(ctx context.Context, in Submit) (Instance, error) {
	p, err := principal(ctx)
	if err != nil {
		return Instance{}, err
	}
	def, err := s.GetActiveDefinition(ctx, in.Entity, in.LegalEntityID)
	if err != nil {
		return Instance{}, err
	}
	if len(in.Assignees) == 0 && len(def.Steps) > 0 {
		in.Assignees = domain.ResolveAssignees(def.Steps[0], nil)
	}
	inst := Instance{ID: ids.New(), Entity: in.Entity, RecordID: in.RecordID, Status: domain.InstanceSubmitted, SubmittedBy: p.UserID, CurrentStep: 1}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var open int
		if err := tx.QueryRow(ctx, `select count(*) from workflow_instance where tenant_id=$1 and entity=$2 and record_id=$3 and status in ('submitted','in_review')`,
			p.TenantID, in.Entity, in.RecordID).Scan(&open); err != nil {
			return err
		}
		if open > 0 {
			return httpx.NewProblem(http.StatusConflict, domain.CodeAlreadyInApproval, "document already in approval")
		}
		if _, err := tx.Exec(ctx, `insert into workflow_instance
			(tenant_id, id, legal_entity_id, definition_id, definition_version, entity, record_id, status, submitted_by, current_step, temporal_run_id)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,1,$10)`,
			p.TenantID, inst.ID, in.LegalEntityID, def.ID, def.Version, in.Entity, in.RecordID, inst.Status, nullUser(p.UserID), "wf-"+inst.ID); err != nil {
			return err
		}
		assignees := in.Assignees
		if len(assignees) == 0 {
			assignees = []string{def.AdminUserID}
		}
		due := time.Now().UTC().Add(4 * time.Hour)
		for _, assignee := range assignees {
			if assignee == "" {
				continue
			}
			if _, err := tx.Exec(ctx, `insert into workflow_work_item
				(tenant_id, id, instance_id, step, assignee_id, status, due_at)
				values ($1,$2,$3,1,$4,'pending',$5)`, p.TenantID, ids.New(), inst.ID, assignee, due); err != nil {
				return err
			}
		}
		return enqueue(ctx, tx, p.TenantID, "instance", inst.ID, svcevents.InstanceSubmitted, map[string]string{
			"instance_id": inst.ID, "entity": in.Entity, "record_id": in.RecordID,
		})
	})
	return inst, err
}

func (s *Postgres) GetInstance(ctx context.Context, id string) (Instance, error) {
	p, err := principal(ctx)
	if err != nil {
		return Instance{}, err
	}
	var inst Instance
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select id, entity, record_id, status, submitted_by::text, coalesce(current_step,0)
			from workflow_instance where tenant_id=$1 and id=$2`, p.TenantID, id).
			Scan(&inst.ID, &inst.Entity, &inst.RecordID, &inst.Status, &inst.SubmittedBy, &inst.CurrentStep)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Instance{}, httpx.NewProblem(http.StatusNotFound, domain.CodeUnknown, "instance not found")
	}
	return inst, err
}

func (s *Postgres) ListWorkItems(ctx context.Context, assignee, status string) ([]WorkItem, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if assignee == "me" {
		assignee = p.UserID
	}
	var items []WorkItem
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := `select id, instance_id, step, assignee_id::text, status, due_at from workflow_work_item where tenant_id=$1`
		args := []any{p.TenantID}
		if assignee != "" {
			args = append(args, assignee)
			q += ` and assignee_id=$2`
		}
		if status != "" {
			args = append(args, status)
			q += ` and status=$` + itoa(len(args))
		}
		q += ` order by due_at`
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item WorkItem
			var due time.Time
			if err := rows.Scan(&item.ID, &item.InstanceID, &item.Step, &item.AssigneeID, &item.Status, &due); err != nil {
				return err
			}
			item.DueAt = due.UTC().Format(time.RFC3339)
			items = append(items, item)
		}
		return rows.Err()
	})
	if items == nil {
		items = []WorkItem{}
	}
	return items, err
}

func (s *Postgres) Act(ctx context.Context, workItemID string, act domain.Action) (WorkItem, error) {
	p, err := principal(ctx)
	if err != nil {
		return WorkItem{}, err
	}
	if act.Actor == "" {
		act.Actor = p.UserID
	}
	var out WorkItem
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var inst domain.Instance
		var item domain.WorkItem
		var def domain.Definition
		var due time.Time
		if err := tx.QueryRow(ctx, `select i.id, i.status, i.submitted_by::text, i.current_step,
			w.id, w.instance_id, w.step, w.assignee_id::text, coalesce(w.original_assignee_id::text,''), w.status, coalesce(w.comment,''), w.delegation_depth, w.due_at,
			d.comment_required, d.delegation_max, coalesce(d.admin_user_id::text,'')
			from workflow_work_item w
			join workflow_instance i on i.tenant_id=w.tenant_id and i.id=w.instance_id
			join workflow_definition d on d.tenant_id=i.tenant_id and d.id=i.definition_id
			where w.tenant_id=$1 and w.id=$2`, p.TenantID, workItemID).
			Scan(&inst.ID, &inst.Status, &inst.SubmittedBy, &inst.CurrentStep,
				&item.ID, &item.InstanceID, &item.Step, &item.AssigneeID, &item.OriginalAssigneeID, &item.Status, &item.Comment, &item.DelegationDepth, &due,
				&def.CommentRequired, &def.DelegationMax, &def.AdminUserID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NewProblem(http.StatusNotFound, domain.CodeUnknown, "work item not found")
			}
			return err
		}
		if act.Kind == "recall" {
			open, err := listItemsTx(ctx, tx, p.TenantID, inst.ID)
			if err != nil {
				return err
			}
			if err := domain.CanRecall(inst, open, act.Actor); err != nil {
				return problem(err)
			}
			if _, err := tx.Exec(ctx, `update workflow_instance set status='recalled', completed_at=now() where tenant_id=$1 and id=$2`, p.TenantID, inst.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `update workflow_work_item set status='canceled' where tenant_id=$1 and instance_id=$2 and status in ('pending','claimed')`, p.TenantID, inst.ID); err != nil {
				return err
			}
			out = WorkItem{ID: item.ID, InstanceID: inst.ID, Step: item.Step, AssigneeID: item.AssigneeID, Status: domain.ItemCanceled, DueAt: due.UTC().Format(time.RFC3339)}
			return enqueue(ctx, tx, p.TenantID, "instance", inst.ID, svcevents.InstanceCompleted, map[string]string{"instance_id": inst.ID, "outcome": "recalled"})
		}
		nextInst, nextItem, err := domain.ApplyAction(inst, item, def, act)
		if err != nil {
			return problem(err)
		}
		if _, err := tx.Exec(ctx, `update workflow_work_item set status=$3, comment=$4, original_assignee_id=nullif($5,'')::uuid, acted_at=now()
			where tenant_id=$1 and id=$2`, p.TenantID, item.ID, nextItem.Status, nextItem.Comment, nextItem.OriginalAssigneeID); err != nil {
			return err
		}
		if act.Kind == "delegate" {
			if _, err := tx.Exec(ctx, `insert into workflow_work_item
				(tenant_id, id, instance_id, step, assignee_id, original_assignee_id, status, due_at, delegation_depth)
				values ($1,$2,$3,$4,$5,$6,'pending',$7,$8)`,
				p.TenantID, ids.New(), inst.ID, item.Step, act.Delegate, item.AssigneeID, due, item.DelegationDepth+1); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update workflow_instance set status=$3, outcome_reason=$4 where tenant_id=$1 and id=$2`,
			p.TenantID, inst.ID, nextInst.Status, nextInst.OutcomeReason); err != nil {
			return err
		}
		out = WorkItem{ID: item.ID, InstanceID: inst.ID, Step: item.Step, AssigneeID: nextItem.AssigneeID, Status: nextItem.Status, DueAt: due.UTC().Format(time.RFC3339)}
		if nextInst.Status == domain.InstanceApproved || nextInst.Status == domain.InstanceRejected {
			return enqueue(ctx, tx, p.TenantID, "instance", inst.ID, svcevents.InstanceCompleted, map[string]string{"instance_id": inst.ID, "outcome": nextInst.Status})
		}
		return nil
	})
	return out, err
}

func (s *Postgres) SaveJob(ctx context.Context, job Job) (Job, error) {
	p, err := principal(ctx)
	if err != nil {
		return Job{}, err
	}
	if job.ID == "" {
		job.ID = ids.New()
	}
	if job.PartitionCount == 0 {
		job.PartitionCount = 1
	}
	if job.Concurrency == 0 {
		job.Concurrency = 1
	}
	if job.Status == "" {
		job.Status = domain.JobActive
	}
	if job.Strategy == "" {
		job.Strategy = domain.PartitionRange
	}
	if job.Priority == "" {
		job.Priority = domain.PriorityB
	}
	if fields := domain.ValidateJob(domain.Job{Code: job.Code, Handler: job.Handler, PartitionCount: job.PartitionCount, Concurrency: job.Concurrency, PartitionStrategy: job.Strategy, Priority: job.Priority}); len(fields) > 0 {
		prob := httpx.NewProblem(http.StatusBadRequest, domain.CodeInvalid, "request failed validation")
		prob.Errors = fields
		return Job{}, prob
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into batch_job
			(tenant_id, id, code, handler, partition_strategy, partition_count, concurrency, priority, exclusive, status)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			on conflict (tenant_id, code) do update set handler=excluded.handler, partition_count=excluded.partition_count,
				concurrency=excluded.concurrency, exclusive=excluded.exclusive, status=excluded.status`,
			p.TenantID, job.ID, job.Code, job.Handler, job.Strategy, job.PartitionCount, job.Concurrency, job.Priority, job.Exclusive, job.Status)
		return err
	})
	return job, err
}

func (s *Postgres) GetJob(ctx context.Context, code string) (Job, error) {
	p, err := principal(ctx)
	if err != nil {
		return Job{}, err
	}
	var job Job
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select id, code, handler, partition_count, concurrency, exclusive, status, partition_strategy, priority
			from batch_job where tenant_id=$1 and code=$2`, p.TenantID, code).
			Scan(&job.ID, &job.Code, &job.Handler, &job.PartitionCount, &job.Concurrency, &job.Exclusive, &job.Status, &job.Strategy, &job.Priority)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, httpx.NewProblem(http.StatusNotFound, domain.CodeUnknown, "job not found")
	}
	return job, err
}

func (s *Postgres) StartRun(ctx context.Context, code string, rows int64, triggeredBy string) (Run, error) {
	p, err := principal(ctx)
	if err != nil {
		return Run{}, err
	}
	job, err := s.GetJob(ctx, code)
	if err != nil {
		return Run{}, err
	}
	var run Run
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var running int
		if err := tx.QueryRow(ctx, `select count(*) from batch_run where tenant_id=$1 and job_id=$2 and status in ('waiting','scheduled','executing')`,
			p.TenantID, job.ID).Scan(&running); err != nil {
			return err
		}
		if err := domain.CanStartRun(domain.Job{Exclusive: job.Exclusive, Status: job.Status}, running > 0); err != nil {
			return problem(err)
		}
		run = Run{ID: ids.New(), JobID: job.ID, Status: domain.RunExecuting, RowsTotal: rows}
		if _, err := tx.Exec(ctx, `insert into batch_run (tenant_id, id, job_id, status, started_at, rows_total, triggered_by)
			values ($1,$2,$3,$4,now(),$5,nullif($6,'')::uuid)`,
			p.TenantID, run.ID, job.ID, run.Status, rows, triggeredBy); err != nil {
			return err
		}
		return enqueue(ctx, tx, p.TenantID, "run", run.ID, svcevents.BatchRunStarted, map[string]string{"job_code": code, "run_id": run.ID})
	})
	return run, err
}

func (s *Postgres) GetRun(ctx context.Context, id string) (Run, error) {
	p, err := principal(ctx)
	if err != nil {
		return Run{}, err
	}
	var run Run
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select id, job_id, status, rows_total, rows_done from batch_run where tenant_id=$1 and id=$2`,
			p.TenantID, id).Scan(&run.ID, &run.JobID, &run.Status, &run.RowsTotal, &run.RowsDone)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, httpx.NewProblem(http.StatusNotFound, domain.CodeUnknown, "run not found")
	}
	return run, err
}

func (s *Postgres) PublishTemplate(ctx context.Context, tpl Template, allowed map[string]bool) (Template, error) {
	p, err := principal(ctx)
	if err != nil {
		return Template{}, err
	}
	if err := domain.ValidatePlaceholders(tpl.Subject, tpl.Body, allowed); err != nil {
		return Template{}, problem(err)
	}
	if tpl.ID == "" {
		tpl.ID = ids.New()
	}
	if tpl.Version == 0 {
		tpl.Version = 1
	}
	if tpl.Layer == "" {
		tpl.Layer = domain.LayerCustomer
	}
	tpl.Status = "published"
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into notify_template
			(tenant_id, id, code, channel, language, layer, version, status, subject, body)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			p.TenantID, tpl.ID, tpl.Code, tpl.Channel, tpl.Language, tpl.Layer, tpl.Version, tpl.Status, tpl.Subject, tpl.Body); err != nil {
			return err
		}
		return enqueue(ctx, tx, p.TenantID, "template", tpl.ID, svcevents.TemplatePublished, map[string]string{"code": tpl.Code, "language": tpl.Language})
	})
	return tpl, err
}

func (s *Postgres) EnqueueMessage(ctx context.Context, msg Message) (Message, error) {
	p, err := principal(ctx)
	if err != nil {
		return Message{}, err
	}
	if msg.ID == "" {
		msg.ID = ids.New()
	}
	msg.Status = domain.MsgQueued
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into notify_outbound_message
			(tenant_id, id, channel, recipient_user_id, recipient_address, template_code, subject, body_text, status, attempts, priority)
			values ($1,$2,$3,nullif($4,'')::uuid,$5,$6,$7,$8,$9,0,'immediate')`,
			p.TenantID, msg.ID, msg.Channel, msg.RecipientUserID, nullStr(msg.RecipientAddr), msg.TemplateCode, msg.Subject, msg.Body, msg.Status)
		return err
	})
	return msg, err
}

func (s *Postgres) RetryMessage(ctx context.Context, id string) (Message, error) {
	p, err := principal(ctx)
	if err != nil {
		return Message{}, err
	}
	var msg Message
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `update notify_outbound_message set status='queued', attempts=0, next_attempt_at=now()
			where tenant_id=$1 and id=$2 returning id, channel, coalesce(recipient_user_id::text,''), coalesce(recipient_address,''), coalesce(template_code,''), coalesce(subject,''), coalesce(body_text,''), status, attempts`,
			p.TenantID, id).Scan(&msg.ID, &msg.Channel, &msg.RecipientUserID, &msg.RecipientAddr, &msg.TemplateCode, &msg.Subject, &msg.Body, &msg.Status, &msg.Attempts)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, httpx.NewProblem(http.StatusNotFound, domain.CodeUnknown, "message not found")
	}
	return msg, err
}

func (s *Postgres) ListMessages(ctx context.Context, status string) ([]Message, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	var out []Message
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := `select id, channel, coalesce(recipient_user_id::text,''), coalesce(recipient_address,''), coalesce(template_code,''), coalesce(subject,''), coalesce(body_text,''), status, attempts
			from notify_outbound_message where tenant_id=$1`
		args := []any{p.TenantID}
		if status != "" {
			args = append(args, status)
			q += ` and status=$2`
		}
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var msg Message
			if err := rows.Scan(&msg.ID, &msg.Channel, &msg.RecipientUserID, &msg.RecipientAddr, &msg.TemplateCode, &msg.Subject, &msg.Body, &msg.Status, &msg.Attempts); err != nil {
				return err
			}
			out = append(out, msg)
		}
		return rows.Err()
	})
	if out == nil {
		out = []Message{}
	}
	return out, err
}

func (s *Postgres) PutPreference(ctx context.Context, pref Preference) (Preference, error) {
	p, err := principal(ctx)
	if err != nil {
		return Preference{}, err
	}
	if pref.UserID == "" {
		pref.UserID = p.UserID
	}
	if pref.Digest == "" {
		pref.Digest = domain.DigestNone
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into notify_preference (tenant_id, user_id, event_class, channels, digest)
			values ($1,$2,$3,$4,$5)
			on conflict (tenant_id, user_id, event_class) do update set channels=excluded.channels, digest=excluded.digest`,
			p.TenantID, pref.UserID, pref.EventClass, pref.Channels, pref.Digest)
		return err
	})
	return pref, err
}

func (s *Postgres) RegisterSchema(ctx context.Context, eventType string, body schema.JSONSchema) error {
	p, err := principal(ctx)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var existing []byte
		err := tx.QueryRow(ctx, `select schema from event_schema where tenant_id=$1 and event_type=$2 order by version desc limit 1`,
			p.TenantID, eventType).Scan(&existing)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if len(existing) > 0 {
			var current schema.JSONSchema
			if err := json.Unmarshal(existing, &current); err != nil {
				return err
			}
			if err := schema.AssertBackwardCompatible(current, body); err != nil {
				return problem(err)
			}
		}
		_, err = tx.Exec(ctx, `insert into event_schema (tenant_id, event_type, version, schema)
			values ($1,$2,coalesce((select max(version) from event_schema where tenant_id=$1 and event_type=$2),0)+1,$3)`,
			p.TenantID, eventType, raw)
		return err
	})
}

func (s *Postgres) GetSchema(ctx context.Context, eventType string) (schema.JSONSchema, error) {
	p, err := principal(ctx)
	if err != nil {
		return schema.JSONSchema{}, err
	}
	var raw []byte
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select schema from event_schema where tenant_id=$1 and event_type=$2 order by version desc limit 1`,
			p.TenantID, eventType).Scan(&raw)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return schema.JSONSchema{}, httpx.NewProblem(http.StatusNotFound, domain.CodeUnknown, "schema not found")
	}
	if err != nil {
		return schema.JSONSchema{}, err
	}
	var body schema.JSONSchema
	if err := json.Unmarshal(raw, &body); err != nil {
		return schema.JSONSchema{}, err
	}
	return body, nil
}

func listItemsTx(ctx context.Context, tx pgx.Tx, tenantID, instanceID string) ([]domain.WorkItem, error) {
	rows, err := tx.Query(ctx, `select id, instance_id, step, assignee_id::text, status, delegation_depth from workflow_work_item where tenant_id=$1 and instance_id=$2`, tenantID, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []domain.WorkItem
	for rows.Next() {
		var item domain.WorkItem
		if err := rows.Scan(&item.ID, &item.InstanceID, &item.Step, &item.AssigneeID, &item.Status, &item.DelegationDepth); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func enqueue(ctx context.Context, tx pgx.Tx, tenantID, aggregate, id, typ string, data map[string]string) error {
	evt, err := events.New(typ, "/passage/workflow/batch", id, tenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregate, id, evt)
}

func problem(err error) error {
	var d *domain.Error
	if errors.As(err, &d) {
		status := http.StatusBadRequest
		if d.Code == domain.CodeRecallNotAllowed || d.Code == domain.CodeAlreadyInApproval || d.Code == domain.CodeRunOverlap || d.Code == domain.CodeJobHold {
			status = http.StatusConflict
		}
		if d.Code == domain.CodeSchemaIncompatible {
			status = http.StatusConflict
		}
		return httpx.NewProblem(status, d.Code, d.Detail)
	}
	return err
}

func nullUser(v string) any {
	if ids.Valid(v) {
		return v
	}
	return "00000000-0000-0000-0000-000000000099"
}

func nullStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func itoa(n int) string { return string(rune('0' + n)) }
