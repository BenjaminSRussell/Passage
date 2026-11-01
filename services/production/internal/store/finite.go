package store

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

// FiniteDates are the operation windows finite-capacity scheduling gave an order (FR-23.10).
type FiniteDates struct {
	OrderID string
	// RunID names the scheduling run the windows come from; it is kept on the order.
	RunID   string
	Windows []domain.OperationWindow
	// IfMatch is the order version the caller saw; 0 skips the check (a service applying a run).
	// It is checked only when the windows change something, so a redelivery stays a no-op.
	IfMatch int
	Actor   string
	TraceID string
}

// ApplyFiniteDates places the order's operations and jobs inside the windows scheduling found
// and dates the order from them (BR-21.1: the infinite-capacity dates of :schedule are replaced).
// It runs under the order row lock. Windows equal to the dates the order already has change
// nothing (a redelivered plan is a no-op: no version, no event). Otherwise IfMatch, when set, must
// be the order's version (412), and the operations' windows and jobs move, the order takes its first start and last end, the basis becomes
// finite, the version moves and order.scheduled.v1 (reason finite) carries the new job set.
// Job feedback stays with its job; an operation with feedback is refused (PRODUCTION_FINITE_LOCKED).
// changed is false for a no-op.
func (s *Postgres) ApplyFiniteDates(ctx context.Context, in FiniteDates) (out Order, changed bool, err error) {
	p, _ := tenant.FromContext(ctx)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		h, err := lockOrder(ctx, tx, p.TenantID, in.OrderID, 0)
		if err != nil {
			return err
		}
		if err := domain.CheckFiniteStatus(h.status); err != nil {
			return domainProblem(err)
		}
		o, err := loadOrder(ctx, tx, p.TenantID, in.OrderID)
		if err != nil {
			return err
		}
		if o.StartDate == nil {
			return domainProblem(&domain.Error{Code: domain.CodeFiniteState, Detail: "the order has no schedule; run :schedule first"})
		}
		ops, jobs, err := loadPlannedJobs(ctx, tx, p.TenantID, in.OrderID)
		if err != nil {
			return err
		}
		locked := map[int]bool{}
		for _, op := range ops {
			if op.Status != domain.OperOpen || !op.HoursReported.IsZero() || !op.QtyGood.IsZero() || !op.QtyError.IsZero() {
				locked[op.OperNumber] = true
			}
		}
		plan, err := domain.ApplyFiniteWindows(ops, jobs, in.Windows, locked)
		if err != nil {
			return domainProblem(err)
		}
		if o.ScheduleBasis == domain.BasisFinite && sameJobs(jobs, plan.Jobs) && sameWindows(ops, plan.Windows) {
			out = o
			return nil
		}
		if in.IfMatch != 0 && h.version != in.IfMatch {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict,
				fmt.Sprintf("order was changed by another user; current version is %d", h.version))
		}
		now := time.Now().UTC().Truncate(time.Second)
		for _, w := range plan.Windows {
			if _, err := tx.Exec(ctx, `update production_operation set planned_start = $4, planned_end = $5
				where tenant_id = $1 and order_id = $2 and oper_number = $3`, p.TenantID, in.OrderID, w.OperNumber, w.Start, w.End); err != nil {
				return err
			}
		}
		for _, j := range plan.Jobs {
			if _, err := tx.Exec(ctx, `update production_job set planned_start = $3, planned_end = $4 where tenant_id = $1 and id = $2`,
				p.TenantID, j.ID, j.Start, j.End); err != nil {
				return err
			}
		}
		var runID *string
		if in.RunID != "" {
			runID = &in.RunID
		}
		compressed := plan.Compressed
		if compressed == nil {
			compressed = []int{}
		}
		if _, err := tx.Exec(ctx, `update production_order set start_date = $3::date, end_date = $4::date, schedule_start = $5,
			schedule_basis = 'finite', finite_run_id = $6, finite_scheduled_at = $7, finite_compressed_opers = $8,
			version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, in.OrderID, plan.StartDate(), plan.EndDate(), plan.Start, runID, now, compressed); err != nil {
			return err
		}
		if out, err = loadOrder(ctx, tx, p.TenantID, in.OrderID); err != nil {
			return err
		}
		changed = true
		return enqueueFinite(ctx, tx, p.TenantID, out, in.Actor, plan.Schedule)
	})
	return out, changed, err
}

// sameJobs reports whether two job sets (same order, same ids) have the same dates.
func sameJobs(a, b []domain.PlannedJob) bool {
	if len(a) != len(b) {
		return false
	}
	byID := map[string]domain.PlannedJob{}
	for _, j := range a {
		byID[j.ID] = j
	}
	for _, j := range b {
		o, ok := byID[j.ID]
		if !ok || !o.Start.Equal(j.Start) || !o.End.Equal(j.End) {
			return false
		}
	}
	return true
}

// sameWindows reports whether the operations already have the windows of the plan.
func sameWindows(ops []domain.Operation, windows []domain.OperationWindow) bool {
	byOp := map[int]domain.Operation{}
	for _, op := range ops {
		byOp[op.OperNumber] = op
	}
	for _, w := range windows {
		op := byOp[w.OperNumber]
		if op.PlannedStart == nil || op.PlannedEnd == nil || !op.PlannedStart.Equal(w.Start) || !op.PlannedEnd.Equal(w.End) {
			return false
		}
	}
	return true
}

// loadPlannedJobs reads the order's operations and its jobs in route order.
func loadPlannedJobs(ctx context.Context, tx pgx.Tx, tenantID, orderID string) ([]domain.Operation, []domain.PlannedJob, error) {
	ops, err := loadOperations(ctx, tx, tenantID, orderID)
	if err != nil {
		return nil, nil, err
	}
	rows, err := tx.Query(ctx, `select id::text, oper_number, seq, job_type, resource_id::text, planned_start, planned_end, planned_hours::text
		from production_job where tenant_id = $1 and order_id = $2 order by oper_number, seq`, tenantID, orderID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var jobs []domain.PlannedJob
	for rows.Next() {
		var j domain.PlannedJob
		var typ, hours string
		if err := rows.Scan(&j.ID, &j.OperNumber, &j.Seq, &typ, &j.ResourceID, &j.Start, &j.End, &hours); err != nil {
			return nil, nil, err
		}
		j.Type, j.Hours = domain.JobType(typ), decimal.RequireFromString(hours)
		jobs = append(jobs, j)
	}
	return ops, jobs, rows.Err()
}

// enqueueFinite writes order.scheduled.v1 for finite dates: reason finite, forward (the plan's own
// direction is scheduling's), the order's whole job set with the ids it keeps.
func enqueueFinite(ctx context.Context, tx pgx.Tx, tenantID string, o Order, actor string, plan domain.Schedule) error {
	return enqueueScheduled(ctx, tx, tenantID, o, actor, "finite", domain.Forward, plan)
}
