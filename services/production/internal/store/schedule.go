package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

// ScheduleParams are the parameters of `:schedule`: the method (operations or jobs), the
// direction and the instant the plan is anchored on (nil: now for forward, the end of the
// delivery date for backward).
type ScheduleParams struct {
	Method    domain.SchedulingMethod
	Direction domain.Direction
	At        *time.Time
}

// scheduleOrder dates the order's route with infinite capacity (BR-21.1 Estimated to Scheduled):
// it resolves the anchor, plans the jobs, replaces the order's jobs and the operations' windows,
// and sets the order's dates, method and schedule start. The caller writes the status and the event.
func scheduleOrder(ctx context.Context, tx pgx.Tx, tenantID string, h orderHead, t Transition) (*domain.Schedule, error) {
	params := scheduleParams(t)
	var delivery *string
	if err := tx.QueryRow(ctx, `select delivery_date::text from production_order where tenant_id = $1 and id = $2`, tenantID, h.id).Scan(&delivery); err != nil {
		return nil, err
	}
	now := t.Now
	if now.IsZero() {
		now = time.Now()
	}
	anchor, err := domain.ResolveAnchor(params.Direction, params.At, delivery, now)
	if err != nil {
		return nil, domainProblem(err)
	}
	return writeSchedule(ctx, tx, tenantID, h.id, h.legalEntityID, params.Method, params.Direction, anchor)
}

// scheduleParams are the `:schedule` options of t, or the defaults (operations, forward, no instant).
func scheduleParams(t Transition) ScheduleParams {
	if t.Schedule != nil {
		return *t.Schedule
	}
	return ScheduleParams{Method: domain.MethodOperations, Direction: domain.Forward}
}

// writeSchedule plans the order's current route and replaces its schedule: the jobs (new ids), the
// operations' windows, and the order's dates, method and schedule start.
func writeSchedule(ctx context.Context, tx pgx.Tx, tenantID, orderID, legalEntityID string, method domain.SchedulingMethod,
	dir domain.Direction, anchor time.Time) (*domain.Schedule, error) {
	ops, err := loadOperations(ctx, tx, tenantID, orderID)
	if err != nil {
		return nil, err
	}
	plan, err := domain.PlanSchedule(ops, method, dir, anchor)
	if err != nil {
		return nil, domainProblem(err)
	}
	if err := clearJobs(ctx, tx, tenantID, orderID); err != nil {
		return nil, err
	}
	for _, w := range plan.Windows {
		if _, err := tx.Exec(ctx, `update production_operation set planned_start = $4, planned_end = $5
			where tenant_id = $1 and order_id = $2 and oper_number = $3`, tenantID, orderID, w.OperNumber, w.Start, w.End); err != nil {
			return nil, err
		}
	}
	for i := range plan.Jobs {
		j := &plan.Jobs[i]
		j.ID = ids.New()
		if _, err := tx.Exec(ctx, `insert into production_job (tenant_id, id, legal_entity_id, order_id, oper_number, seq, job_type, resource_id,
			planned_start, planned_end, planned_hours) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			tenantID, j.ID, legalEntityID, orderID, j.OperNumber, j.Seq, string(j.Type), j.ResourceID, j.Start, j.End, j.Hours.String()); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `update production_order set start_date = $3::date, end_date = $4::date, scheduling_method = $5, schedule_start = $6
		where tenant_id = $1 and id = $2`, tenantID, orderID, plan.StartDate(), plan.EndDate(), string(method), plan.Start); err != nil {
		return nil, err
	}
	return &plan, nil
}

// redateOrder re-plans a dated order after its route or quantities changed (an order edit, an
// applied order change, a split, a merge, a trigger-inserted operation), so its jobs and windows
// match the route again. The re-date keeps the method and the schedule's start and plans forward
// from it: work already dated never moves earlier. It writes order.scheduled.v1 with the new
// jobs. An order without a schedule is left alone.
func redateOrder(ctx context.Context, tx pgx.Tx, tenantID, orderID, actor string) error {
	var start *time.Time
	var method, legalEntityID string
	if err := tx.QueryRow(ctx, `select schedule_start, scheduling_method, legal_entity_id::text from production_order where tenant_id = $1 and id = $2`,
		tenantID, orderID).Scan(&start, &method, &legalEntityID); err != nil {
		return err
	}
	if start == nil {
		return nil
	}
	plan, err := writeSchedule(ctx, tx, tenantID, orderID, legalEntityID, domain.SchedulingMethod(method), domain.Forward, *start)
	if err != nil {
		return err
	}
	o, err := loadOrder(ctx, tx, tenantID, orderID)
	if err != nil {
		return err
	}
	return enqueueScheduled(ctx, tx, tenantID, o, actor, "redate", domain.Forward, *plan)
}

func clearJobs(ctx context.Context, tx pgx.Tx, tenantID, orderID string) error {
	_, err := tx.Exec(ctx, `delete from production_job where tenant_id = $1 and order_id = $2`, tenantID, orderID)
	return err
}

// clearSchedule takes a schedule back: the jobs, the operations' windows and the order's dates.
func clearSchedule(ctx context.Context, tx pgx.Tx, tenantID, orderID string) error {
	if err := clearJobs(ctx, tx, tenantID, orderID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update production_operation set planned_start = null, planned_end = null
		where tenant_id = $1 and order_id = $2 and (planned_start is not null or planned_end is not null)`, tenantID, orderID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `update production_order set start_date = null, end_date = null, schedule_start = null where tenant_id = $1 and id = $2`, tenantID, orderID)
	return err
}

// enqueueScheduled writes order.scheduled.v1: the order's whole current job set, which replaces
// whatever a consumer held for the order.
func enqueueScheduled(ctx context.Context, tx pgx.Tx, tenantID string, o Order, actor, reason string, dir domain.Direction, plan domain.Schedule) error {
	jobs := make([]prodevents.ScheduledJob, len(plan.Jobs))
	for i, j := range plan.Jobs {
		jobs[i] = prodevents.ScheduledJob{JobID: j.ID, OperNumber: j.OperNumber, Seq: j.Seq, JobType: string(j.Type), ResourceID: j.ResourceID,
			PlannedStart: j.Start.UTC().Format(time.RFC3339), PlannedEnd: j.End.UTC().Format(time.RFC3339), PlannedHours: j.Hours.String()}
	}
	return enqueue(ctx, tx, o.ID, prodevents.OrderScheduled, tenantID, actor, prodevents.OrderScheduledData{
		OrderID: o.ID, OrderNumber: o.OrderNumber, LegalEntityID: o.LegalEntityID, SiteID: o.SiteID, Status: string(o.Status), Reason: reason,
		SchedulingMethod: string(o.SchedulingMethod), Direction: string(dir), Start: plan.Start.UTC().Format(time.RFC3339),
		End: plan.End.UTC().Format(time.RFC3339), StartDate: plan.StartDate(), EndDate: plan.EndDate(), DeliveryDate: o.DeliveryDate,
		Jobs: jobs, OrderVersion: o.Version, Actor: actor, OccurredAt: o.UpdatedAt.UTC().Format(time.RFC3339Nano),
	})
}

// JobFilter narrows the job list. From and To select the jobs that overlap the window
// [From, To); empty fields do not filter. Cursor is the id of the last job of the previous
// page (the list is ordered by planned start, then id).
type JobFilter struct {
	LegalEntityID string
	SiteID        string
	ResourceID    string
	OrderID       string
	JobType       domain.JobType
	OrderStatus   domain.Status
	From          *time.Time
	To            *time.Time
	Cursor        string
	Limit         int
}

// Job is one ProductionJob with the order it belongs to.
type Job struct {
	ID            string          `json:"id"`
	OrderID       string          `json:"orderId"`
	OrderNumber   string          `json:"orderNumber"`
	OrderStatus   domain.Status   `json:"orderStatus"`
	LegalEntityID string          `json:"legalEntityId"`
	SiteID        string          `json:"siteId"`
	ProductID     string          `json:"productId"`
	OperNumber    int             `json:"operNumber"`
	Seq           int             `json:"seq"`
	JobType       domain.JobType  `json:"jobType"`
	ResourceID    string          `json:"resourceId"`
	PlannedStart  time.Time       `json:"plannedStart"`
	PlannedEnd    time.Time       `json:"plannedEnd"`
	PlannedHours  decimal.Decimal `json:"plannedHours"`
	Status        string          `json:"status"`
}

// JobPage is a keyset page of jobs with the hours of the whole filter.
type JobPage struct {
	Items        []Job           `json:"items"`
	Total        int             `json:"total"`
	PlannedHours decimal.Decimal `json:"plannedHours"`
	NextCursor   string          `json:"nextCursor,omitempty"`
}

const jobsSelected = `with jobs as (
	select j.id, j.order_id, o.order_number, o.status as order_status, j.legal_entity_id, o.site_id, o.product_id, j.oper_number, j.seq,
		j.job_type, j.resource_id, j.planned_start, j.planned_end, j.planned_hours, j.status
	from production_job j
	join production_order o on o.tenant_id = j.tenant_id and o.id = j.order_id
	where j.tenant_id = $1
	  and ($2 = '' or j.legal_entity_id = nullif($2, '')::uuid)
	  and ($3 = '' or o.site_id = nullif($3, '')::uuid)
	  and ($4 = '' or j.resource_id = nullif($4, '')::uuid)
	  and ($5 = '' or j.order_id = nullif($5, '')::uuid)
	  and ($6 = '' or j.job_type = $6)
	  and ($7 = '' or o.status = $7)
	  and ($8::timestamptz is null or j.planned_end > $8)
	  and ($9::timestamptz is null or j.planned_start < $9)
)
`

// ListJobs returns the dated jobs across orders (the load a planner reads per resource and
// window), ordered by planned start, with the count and hours of the whole filter. RLS limits
// it to the caller's legal entities.
func (s *Postgres) ListJobs(ctx context.Context, f JobFilter) (JobPage, error) {
	p, _ := tenant.FromContext(ctx)
	if f.Limit <= 0 {
		f.Limit = 100
	}
	out := JobPage{Items: []Job{}}
	args := []any{p.TenantID, f.LegalEntityID, f.SiteID, f.ResourceID, f.OrderID, string(f.JobType), string(f.OrderStatus), f.From, f.To}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, jobsSelected+`select id::text, order_id::text, order_number, order_status, legal_entity_id::text, site_id::text,
				product_id::text, oper_number, seq, job_type, resource_id::text, planned_start, planned_end, planned_hours::text, status
			from jobs where ($10 = '' or (planned_start, id) > (select planned_start, id from jobs where id = nullif($10, '')::uuid))
			order by planned_start, id limit $11`, append(args, f.Cursor, f.Limit+1)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var j Job
			var orderStatus, jobType, hours string
			if err := rows.Scan(&j.ID, &j.OrderID, &j.OrderNumber, &orderStatus, &j.LegalEntityID, &j.SiteID, &j.ProductID, &j.OperNumber, &j.Seq,
				&jobType, &j.ResourceID, &j.PlannedStart, &j.PlannedEnd, &hours, &j.Status); err != nil {
				rows.Close()
				return err
			}
			j.OrderStatus, j.JobType, j.PlannedHours = domain.Status(orderStatus), domain.JobType(jobType), dec(hours)
			out.Items = append(out.Items, j)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		var sum string
		if err := tx.QueryRow(ctx, jobsSelected+`select count(*), coalesce(sum(planned_hours), 0)::text from jobs`, args...).Scan(&out.Total, &sum); err != nil {
			return err
		}
		out.PlannedHours = dec(sum)
		return nil
	})
	if err != nil {
		return JobPage{}, err
	}
	if len(out.Items) > f.Limit {
		out.Items = out.Items[:f.Limit]
		out.NextCursor = out.Items[f.Limit-1].ID
	}
	return out, nil
}
