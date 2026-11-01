package store

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
)

// MaxLateOrders bounds the late orders inquiry.
const MaxLateOrders = 500

// ResourceKPI is a resource's use by one run (PRD 4.23.14 Resource utilization):
// its bookings as primary or secondary resource against the open minutes of
// its calendar, in its own time zone, over the span of the run's jobs.
type ResourceKPI struct {
	ResourceID     string  `json:"resourceId"`
	ResourceCode   string  `json:"resourceCode"`
	Jobs           int     `json:"jobs"`
	BookedMinutes  int     `json:"bookedMinutes"`
	OpenMinutes    int     `json:"openMinutes"`
	UtilizationPct float64 `json:"utilizationPct"`
}

// RunKPIs are the measures of the jobs a run still holds. A later run or a
// move replaces jobs, so the figures describe the plan as it stands for the
// operations this run placed; PlacedJobs against the run's operations count
// shows how much of the run is still current.
type RunKPIs struct {
	RunID             string        `json:"runId"`
	PlacedJobs        int           `json:"placedJobs"`
	Orders            int           `json:"orders"`
	LateJobs          int           `json:"lateJobs"`
	LateOrders        int           `json:"lateOrders"`
	MaxLateMinutes    int           `json:"maxLateMinutes"`
	ChangeoverMinutes int           `json:"changeoverMinutes"`
	BookedMinutes     int           `json:"bookedMinutes"`
	SpanStart         *time.Time    `json:"spanStart"`
	SpanEnd           *time.Time    `json:"spanEnd"`
	Resources         []ResourceKPI `json:"resources"`
}

// LateOrder is an order with a job past its due date or an operation the
// scheduler could not place (the Late orders inquiry, PRD 4.23.14). DueAt is
// the order's delivery date: the latest due date of its operations.
type LateOrder struct {
	OrderID            string     `json:"orderId"`
	DueAt              *time.Time `json:"dueAt"`
	PlannedStart       *time.Time `json:"plannedStart"`
	PlannedEnd         *time.Time `json:"plannedEnd"`
	LateMinutes        int        `json:"lateMinutes"`
	LateOperations     int        `json:"lateOperations"`
	UnplacedOperations int        `json:"unplacedOperations"`
	Operations         int        `json:"operations"`
	Firmed             bool       `json:"firmed"`
	// DependentOrders is the pegged demand: orders pegged to consume this
	// order's output (order pegs), which a late end delays in turn.
	DependentOrders int `json:"dependentOrders"`
}

// LateOrderFilter selects late orders: those whose latest job is at least
// MinLateMinutes late, plus every order with an unplaced operation.
type LateOrderFilter struct {
	MinLateMinutes int
	Limit          int
}

// RunKPIStore is the run measures and the late orders inquiry.
type RunKPIStore interface {
	RunKPIs(ctx context.Context, runID string) (RunKPIs, error)
	LateOrders(ctx context.Context, f LateOrderFilter) ([]LateOrder, bool, error)
}

// RunKPIs measures the jobs that still carry the run's id: how many orders
// and jobs, how late, the changeover minutes, and each resource's booked
// versus open minutes between the first start and last end of those jobs.
func (s *Postgres) RunKPIs(ctx context.Context, runID string) (RunKPIs, error) {
	out := RunKPIs{RunID: runID, Resources: []ResourceKPI{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := scanRun(tx.QueryRow(ctx, `select `+runCols+` from schedule_run where id = $1`, runID), nil); errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeRunNotFound, "schedule run not found")
		} else if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select resource_id::text, job_id::text, order_id::text, start_at, end_at, late_minutes, changeover_minutes
			from scheduled_job where run_id = $1`, runID)
		if err != nil {
			return err
		}
		defer rows.Close()
		jobs, orders, lateJobs, lateOrders := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
		segs := map[string][]domain.Busy{}
		var first, last time.Time
		for rows.Next() {
			var rid, jid, oid string
			var st, en time.Time
			var late, chg int
			if err := rows.Scan(&rid, &jid, &oid, &st, &en, &late, &chg); err != nil {
				return err
			}
			jobs[jid], orders[oid] = true, true
			if late > 0 {
				lateJobs[jid], lateOrders[oid] = true, true
				out.MaxLateMinutes = max(out.MaxLateMinutes, late)
			}
			out.ChangeoverMinutes += chg
			out.BookedMinutes += int(en.Sub(st) / time.Minute)
			segs[rid] = append(segs[rid], domain.Busy{ResourceID: rid, JobID: jid, Start: st, End: en})
			if first.IsZero() || st.Before(first) {
				first = st
			}
			if en.After(last) {
				last = en
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		out.PlacedJobs, out.Orders, out.LateJobs, out.LateOrders = len(jobs), len(orders), len(lateJobs), len(lateOrders)
		if len(segs) == 0 {
			return nil
		}
		out.SpanStart, out.SpanEnd = &first, &last
		// Secondary resources (tools, crews) the run booked with its jobs are
		// used too; they count on their own rows, not in the run's totals.
		srows, err := tx.Query(ctx, `select resource_id::text, job_id::text, start_at, end_at from scheduled_job_secondary where run_id = $1`, runID)
		if err != nil {
			return err
		}
		sec, err := scanBusy(srows)
		if err != nil {
			return err
		}
		for _, b := range sec {
			segs[b.ResourceID] = append(segs[b.ResourceID], b)
		}
		resources, _, err := loadResources(ctx, tx)
		if err != nil {
			return err
		}
		for _, r := range resources {
			if len(segs[r.ID]) == 0 {
				continue
			}
			k := domain.RunUtilization(r, segs[r.ID], first, last)
			out.Resources = append(out.Resources, ResourceKPI{ResourceID: r.ID, ResourceCode: r.Code, Jobs: k.Jobs, BookedMinutes: k.BookedMinutes,
				OpenMinutes: k.OpenMinutes, UtilizationPct: math.Round(k.UtilizationPct*10) / 10})
		}
		sort.SliceStable(out.Resources, func(i, j int) bool { return out.Resources[i].UtilizationPct > out.Resources[j].UtilizationPct })
		return nil
	})
	return out, err
}

// LateOrders lists orders with a job past its due date by at least
// MinLateMinutes (at least 1) and orders with an unplaced operation, the most
// late first (unplaced orders after the late ones), at most Limit; more than
// Limit sets truncated. Lateness is that of the jobs now in the plan.
func (s *Postgres) LateOrders(ctx context.Context, f LateOrderFilter) ([]LateOrder, bool, error) {
	out := []LateOrder{}
	limit := f.Limit
	if limit < 1 || limit > MaxLateOrders {
		limit = MaxLateOrders
	}
	minLate := max(f.MinLateMinutes, 1)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Candidate orders come from the partial indexes on late jobs and
		// unplaced operations, so the cost follows the late part of the plan,
		// not the whole of it.
		rows, err := tx.Query(ctx, `with cand as (
				select tenant_id, order_id from scheduled_job where late_minutes > 0 and late_minutes >= $1
				union
				select tenant_id, order_id from scheduling_operation where status = 'unplaced'
			)
			select o.order_id::text, max(o.due_at), min(j.start_at), max(j.end_at),
				coalesce(max(j.late_minutes), 0), count(distinct o.id) filter (where j.late_minutes > 0),
				count(distinct o.id) filter (where o.status = 'unplaced'), count(distinct o.id), coalesce(bool_or(j.firmed), false),
				(select count(*) from scheduling_order_peg p where p.tenant_id = o.tenant_id and p.parent_order_id = o.order_id)
			from cand c
			join scheduling_operation o on o.tenant_id = c.tenant_id and o.order_id = c.order_id
			left join scheduled_job j on j.tenant_id = o.tenant_id and j.job_id = o.id
			group by o.tenant_id, o.order_id
			order by coalesce(max(j.late_minutes), 0) desc, o.order_id
			limit $2`, minLate, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l LateOrder
			if err := rows.Scan(&l.OrderID, &l.DueAt, &l.PlannedStart, &l.PlannedEnd, &l.LateMinutes, &l.LateOperations, &l.UnplacedOperations,
				&l.Operations, &l.Firmed, &l.DependentOrders); err != nil {
				return err
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	truncated := len(out) > limit
	if truncated {
		out = out[:limit]
	}
	return out, truncated, err
}
