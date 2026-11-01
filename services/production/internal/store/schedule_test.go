package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
	"github.com/BenjaminSRussell/passage/services/production/migrations"
)

// scheduledRoute is a two-operation route with queue and transit hours on a quantity of 100:
// op 10 queues 2 h, sets up 1 h, runs 0.1 h per unit (10 h) and moves 0.5 h; op 20 runs 0.05 h per unit (5 h).
func scheduledRoute(t *testing.T, resA, resB string) []domain.Operation {
	t.Helper()
	ops, err := domain.PlanOperations([]domain.Operation{
		{OperNumber: 10, ResourceID: resA, SetupH: d("1"), RunHPer: d("0.1"), QueueBeforeH: d("2"), TransitH: d("0.5"), LaborRate: d("30")},
		{OperNumber: 20, ResourceID: resB, RunHPer: d("0.05"), LaborRate: d("30")},
	}, d("100"))
	if err != nil {
		t.Fatal(err)
	}
	return ops
}

func estimatedOrder(t *testing.T, s *Postgres, ctx context.Context, key, le string, mut func(*NewOrder)) (*stepper, Order) {
	t.Helper()
	in := sampleOrder(t, key, le)
	if mut != nil {
		mut(&in)
	}
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	return st, st.must(domain.CmdEstimate, nil)
}

func jobsOf(t *testing.T, s *Postgres, ctx context.Context, orderID string) []Job {
	t.Helper()
	page, err := s.ListJobs(ctx, JobFilter{OrderID: orderID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}

// BR-21.1 Estimated to Scheduled: the jobs are dated one operation after the other, the operations
// carry their windows, the order its dates, and the event carries the jobs.
func TestSchedule_ForwardJobs(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(181))
	le, resA, resB := ids.New(), ids.New(), ids.New()
	st, _ := estimatedOrder(t, s, ctx, "sched-fwd", le, func(in *NewOrder) { in.Operations = scheduledRoute(t, resA, resB) })
	at := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	o := st.must(domain.CmdSchedule, func(tr *Transition) {
		tr.Schedule = &ScheduleParams{Method: domain.MethodJobs, Direction: domain.Forward, At: &at}
	})
	if o.Status != domain.Scheduled || o.StartDate == nil || *o.StartDate != "2026-11-02" || o.EndDate == nil || *o.EndDate != "2026-11-03" || o.SchedulingMethod != domain.MethodJobs {
		t.Fatalf("order: %s %v %v %s", o.Status, o.StartDate, o.EndDate, o.SchedulingMethod)
	}
	// 2 queue + 1 setup + 10 run + 0.5 transit + 5 run = 18.5 h
	if end := at.Add(18*time.Hour + 30*time.Minute); !o.Operations[1].PlannedEnd.Equal(end) || !o.Operations[0].PlannedStart.Equal(at) ||
		!o.Operations[0].PlannedEnd.Equal(at.Add(13*time.Hour+30*time.Minute)) || !o.Operations[1].PlannedStart.Equal(*o.Operations[0].PlannedEnd) {
		t.Fatalf("windows: %+v %+v", o.Operations[0], o.Operations[1])
	}
	jobs := jobsOf(t, s, ctx, o.ID)
	want := []struct {
		oper int
		typ  domain.JobType
		res  string
		h    string
	}{{10, domain.JobQueueBefore, resA, "2"}, {10, domain.JobSetup, resA, "1"}, {10, domain.JobProcess, resA, "10"}, {10, domain.JobTransport, resA, "0.5"}, {20, domain.JobProcess, resB, "5"}}
	if len(jobs) != len(want) {
		t.Fatalf("%d jobs: %+v", len(jobs), jobs)
	}
	for i, w := range want {
		j := jobs[i]
		if j.OperNumber != w.oper || j.JobType != w.typ || j.ResourceID != w.res || !j.PlannedHours.Equal(d(w.h)) || j.Status != "planned" || j.OrderNumber != o.OrderNumber {
			t.Errorf("job %d = %+v, want %+v", i, j, w)
		}
		if i > 0 && !j.PlannedStart.Equal(jobs[i-1].PlannedEnd) {
			t.Errorf("job %d does not follow job %d", i, i-1)
		}
	}
	types := outboxTypes(t, pool, o.ID)
	if types[len(types)-2] != prodevents.OrderStatusChanged || types[len(types)-1] != prodevents.OrderScheduled {
		t.Fatalf("events %v", types)
	}
}

// The operations method gives each operation one process job (setup and run together).
func TestSchedule_OperationsMethodDefault(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(182))
	st, _ := estimatedOrder(t, s, ctx, "sched-ops", ids.New(), func(in *NewOrder) { in.Operations = scheduledRoute(t, ids.New(), ids.New()) })
	now := time.Date(2026, 11, 2, 9, 0, 0, 500, time.UTC)
	o := st.must(domain.CmdSchedule, func(tr *Transition) { tr.Now = now })
	if o.SchedulingMethod != domain.MethodOperations || *o.StartDate != "2026-11-02" {
		t.Fatalf("defaults: %s %v", o.SchedulingMethod, o.StartDate)
	}
	var process []Job
	for _, j := range jobsOf(t, s, ctx, o.ID) {
		if j.JobType == domain.JobProcess {
			process = append(process, j)
		}
		if j.JobType == domain.JobSetup {
			t.Error("no separate setup job under the operations method")
		}
	}
	if len(process) != 2 || !process[0].PlannedHours.Equal(d("11")) || !process[0].PlannedStart.Equal(now.Truncate(time.Second).Add(2*time.Hour)) {
		t.Fatalf("process jobs %+v", process)
	}
}

// A backward schedule ends at the end of the delivery date and needs a delivery date or an instant.
func TestSchedule_Backward(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(183))
	back := func(tr *Transition) {
		tr.Schedule = &ScheduleParams{Method: domain.MethodJobs, Direction: domain.Backward}
	}
	st, _ := estimatedOrder(t, s, ctx, "sched-back", ids.New(), func(in *NewOrder) { in.Operations = scheduledRoute(t, ids.New(), ids.New()) })
	o := st.must(domain.CmdSchedule, back)
	jobs := jobsOf(t, s, ctx, o.ID)
	last := jobs[len(jobs)-1]
	if !last.PlannedEnd.Equal(time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC)) || *o.EndDate != "2026-11-30" || *o.StartDate != "2026-11-30" {
		t.Fatalf("ends %v dates %v %v", last.PlannedEnd, o.StartDate, o.EndDate)
	}

	st2, e := estimatedOrder(t, s, ctx, "sched-back-nodate", ids.New(), func(in *NewOrder) { in.DeliveryDate = nil })
	_, err := st2.step(domain.CmdSchedule, back)
	expectProblem(t, err, 422, domain.CodeScheduleInput)
	if got, _ := s.GetOrder(ctx, e.ID); got.Status != domain.Estimated || got.Version != e.Version || len(jobsOf(t, s, ctx, e.ID)) != 0 {
		t.Fatalf("a refused schedule changes nothing: %s v%d", got.Status, got.Version)
	}
}

// A reset to Estimated and a cancel take the schedule back; scheduling again replaces it, not adds to it.
func TestSchedule_ResetAndCancelWithdrawIt(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(184))
	st, _ := estimatedOrder(t, s, ctx, "sched-reset", ids.New(), func(in *NewOrder) { in.Operations = scheduledRoute(t, ids.New(), ids.New()) })
	o := st.must(domain.CmdSchedule, func(tr *Transition) {
		tr.Schedule = &ScheduleParams{Method: domain.MethodJobs, Direction: domain.Forward}
	})
	first := len(jobsOf(t, s, ctx, o.ID))
	o = st.must(domain.CmdRelease, nil)
	if len(jobsOf(t, s, ctx, o.ID)) != first || o.StartDate == nil {
		t.Fatal("release keeps the schedule")
	}
	o = st.must(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Scheduled })
	if o.StartDate == nil {
		t.Fatal("a reset to Scheduled keeps the schedule")
	}
	o = st.must(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Estimated })
	if o.StartDate != nil || o.EndDate != nil || len(jobsOf(t, s, ctx, o.ID)) != 0 || o.Operations[0].PlannedStart != nil || o.Operations[1].PlannedEnd != nil {
		t.Fatalf("a reset to Estimated clears dates, windows and jobs: %v %v %+v", o.StartDate, o.EndDate, o.Operations[0])
	}
	o = st.must(domain.CmdSchedule, nil)
	if n := len(jobsOf(t, s, ctx, o.ID)); n != 4 || o.SchedulingMethod != domain.MethodOperations {
		t.Fatalf("rescheduled with the operations method: %d jobs", n)
	}
	o = st.must(domain.CmdCancel, nil)
	if o.StartDate != nil || len(jobsOf(t, s, ctx, o.ID)) != 0 {
		t.Fatal("a cancelled order holds no jobs")
	}
}

// The same Idempotency-Key replays the stored result and writes no second set of jobs.
func TestSchedule_ReplayWritesNoSecondSchedule(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(185))
	_, e := estimatedOrder(t, s, ctx, "sched-replay", ids.New(), nil)
	tr := Transition{OrderID: e.ID, IfMatch: e.Version, Command: domain.CmdSchedule, Actor: "planner-1", IdempotencyKey: "sched-replay-key", RequestHash: "h"}
	first, replayed, err := s.Transition(ctx, tr)
	if err != nil || replayed {
		t.Fatalf("first: %v %v", replayed, err)
	}
	again, replayed, err := s.Transition(ctx, tr)
	if err != nil || !replayed || again.Version != first.Version || len(jobsOf(t, s, ctx, e.ID)) != 1 {
		t.Fatalf("replay: %v %v v%d jobs %d", replayed, err, again.Version, len(jobsOf(t, s, ctx, e.ID)))
	}
	n := 0
	for _, typ := range outboxTypes(t, pool, e.ID) {
		if typ == prodevents.OrderScheduled {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d scheduled events", n)
	}
}

// The job list filters by resource, window, type and order status, pages by cursor, totals the filter,
// and shows nothing of another tenant.
func TestListJobs_FiltersPagesAndIsolation(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(186))
	le, shared := ids.New(), ids.New()
	at := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	var orders []Order
	for i, key := range []string{"jobs-a", "jobs-b"} {
		st, _ := estimatedOrder(t, s, ctx, key, le, func(in *NewOrder) { in.Operations = scheduledRoute(t, shared, ids.New()) })
		start := at.Add(time.Duration(i) * 48 * time.Hour)
		orders = append(orders, st.must(domain.CmdSchedule, func(tr *Transition) {
			tr.Schedule = &ScheduleParams{Method: domain.MethodJobs, Direction: domain.Forward, At: &start}
		}))
	}
	all, err := s.ListJobs(ctx, JobFilter{ResourceID: shared, Limit: 100})
	if err != nil || all.Total != 8 || len(all.Items) != 8 || !all.PlannedHours.Equal(d("27")) {
		t.Fatalf("resource filter: %+v %v", all, err)
	}
	for i := 1; i < len(all.Items); i++ {
		if all.Items[i].PlannedStart.Before(all.Items[i-1].PlannedStart) {
			t.Fatal("jobs are ordered by planned start")
		}
	}
	from, to := at.Add(24*time.Hour), at.Add(72*time.Hour)
	win, err := s.ListJobs(ctx, JobFilter{ResourceID: shared, From: &from, To: &to, Limit: 100})
	if err != nil || win.Total != 4 || win.Items[0].OrderID != orders[1].ID {
		t.Fatalf("window: %+v %v", win, err)
	}
	setup, err := s.ListJobs(ctx, JobFilter{JobType: domain.JobSetup, OrderStatus: domain.Scheduled, Limit: 100})
	if err != nil || setup.Total != 2 {
		t.Fatalf("type and status: %+v %v", setup, err)
	}
	var seen []string
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		p, err := s.ListJobs(ctx, JobFilter{ResourceID: shared, Limit: 3, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range p.Items {
			seen = append(seen, j.ID)
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if len(seen) != 8 {
		t.Fatalf("paging saw %d jobs", len(seen))
	}
	dup := map[string]bool{}
	for _, id := range seen {
		if dup[id] {
			t.Fatal("a job appears on two pages")
		}
		dup[id] = true
	}
	other, err := s.ListJobs(tenantCtx(testkit.TenantID(187)), JobFilter{Limit: 100})
	if err != nil || other.Total != 0 || len(other.Items) != 0 {
		t.Fatalf("another tenant sees %+v %v", other, err)
	}
}

// scheduledEvents decodes the order.scheduled.v1 payloads of an order, oldest first.
func scheduledEvents(t *testing.T, pool *pgxpool.Pool, orderID string) []prodevents.OrderScheduledData {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select payload from outbox where aggregate_id = $1 and type = $2 order by created_at, id`,
		orderID, prodevents.OrderScheduled)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []prodevents.OrderScheduledData
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var evt struct {
			Data prodevents.OrderScheduledData `json:"data"`
		}
		if err := json.Unmarshal(raw, &evt); err != nil {
			t.Fatal(err)
		}
		out = append(out, evt.Data)
	}
	return out
}

// An order edit on a dated order re-dates it: forward from the same start, with the changed hours,
// the added operation dated after the others, the new queue time as a job, and a redate event
// whose job ids are the stored ones.
func TestSchedule_OrderEditRedates(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(188))
	resA, resB := ids.New(), ids.New()
	st, _ := estimatedOrder(t, s, ctx, "sched-edit", ids.New(), func(in *NewOrder) { in.Operations = scheduledRoute(t, resA, resB) })
	at := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	o := st.must(domain.CmdSchedule, func(tr *Transition) {
		tr.Schedule = &ScheduleParams{Method: domain.MethodJobs, Direction: domain.Backward, At: &at}
	})
	start := *o.Operations[0].PlannedStart
	run, queue, res := d("0.1"), d("1.5"), ids.New()
	o, err := s.EditOrder(ctx, OrderEdit{OrderID: o.ID, IfMatch: o.Version, Actor: "engineer-1", Edits: domain.OrderEdits{Operations: []domain.OperationEdit{
		{Op: domain.EditChange, OperNumber: 20, RunHPer: &run, QueueBeforeH: &queue},
		{Op: domain.EditAdd, OperNumber: 30, ResourceID: &res, SetupH: &queue},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	st.version = o.Version
	// 13.5 h for op 10 as before; op 20 now 1.5 queue + 10 run; op 30 1.5 setup: 26.5 h from the same start.
	if !o.Operations[0].PlannedStart.Equal(start) || !o.Operations[1].PlannedStart.Equal(start.Add(13*time.Hour+30*time.Minute)) ||
		!o.Operations[2].PlannedEnd.Equal(start.Add(26*time.Hour+30*time.Minute)) || !o.Operations[1].QueueBeforeH.Equal(queue) {
		t.Fatalf("windows after the edit: %+v", o.Operations)
	}
	jobs := jobsOf(t, s, ctx, o.ID)
	// Op 30 has a setup job and its (zero-hour) process job.
	if len(jobs) != 8 || jobs[4].OperNumber != 20 || jobs[4].JobType != domain.JobQueueBefore || jobs[6].OperNumber != 30 || jobs[6].JobType != domain.JobSetup ||
		jobs[6].ResourceID != res || jobs[7].JobType != domain.JobProcess {
		t.Fatalf("jobs after the edit: %+v", jobs)
	}
	if want := start.Add(26*time.Hour + 30*time.Minute).Format(time.DateOnly); o.EndDate == nil || *o.EndDate != want {
		t.Fatalf("end date %v, want %s", o.EndDate, want)
	}
	evts := scheduledEvents(t, pool, o.ID)
	if len(evts) != 2 || evts[0].Reason != "schedule" || evts[0].Direction != "backward" || evts[1].Reason != "redate" || evts[1].Direction != "forward" ||
		len(evts[1].Jobs) != 8 || evts[1].Jobs[6].JobID != jobs[6].ID || evts[1].OrderVersion != o.Version || evts[1].Status != string(domain.Scheduled) {
		t.Fatalf("scheduled events: %+v", evts)
	}
	// An undated order is not dated by an edit.
	_, e := estimatedOrder(t, s, ctx, "sched-edit-undated", ids.New(), nil)
	if _, err := s.EditOrder(ctx, OrderEdit{OrderID: e.ID, IfMatch: e.Version, Actor: "engineer-1", Edits: domain.OrderEdits{Operations: []domain.OperationEdit{
		{Op: domain.EditChange, OperNumber: 10, RunHPer: &run}}}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetOrder(ctx, e.ID); got.StartDate != nil || len(jobsOf(t, s, ctx, e.ID)) != 0 || len(scheduledEvents(t, pool, e.ID)) != 0 {
		t.Fatal("an edit leaves an unscheduled order unscheduled")
	}
}

// FR-21.15 carries the jobs: splitting a dated order dates both orders from the original's start for
// their quantities; merging re-dates the survivor and the order merged away holds no jobs.
func TestSchedule_SplitAndMergeCarryTheJobs(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(189))
	base := sampleOrder(t, "sched-split", ids.New())
	o, _, err := s.CreateOrder(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	st.must(domain.CmdEstimate, nil)
	at := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	o = st.must(domain.CmdSchedule, func(tr *Transition) {
		tr.Schedule = &ScheduleParams{Method: domain.MethodJobs, Direction: domain.Forward, At: &at}
	})
	res, err := s.SplitOrder(ctx, Split{OrderID: o.ID, IfMatch: o.Version, SplitQty: d("40"), Actor: "planner-1", IdempotencyKey: "sched-split-1", RequestHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	// 0.5 h per unit: 60 units 30 h on the original, 40 units 20 h on the new order, both from 08:00.
	for _, c := range []struct {
		o     Order
		hours string
	}{{res.Original, "30"}, {res.New, "20"}} {
		jobs := jobsOf(t, s, ctx, c.o.ID)
		if c.o.Status != domain.Scheduled || len(jobs) != 1 || !jobs[0].PlannedHours.Equal(d(c.hours)) || !jobs[0].PlannedStart.Equal(at) ||
			c.o.StartDate == nil || *c.o.StartDate != "2026-11-02" || c.o.SchedulingMethod != domain.MethodJobs {
			t.Fatalf("order %s after the split: %+v %+v", c.o.OrderNumber, c.o, jobs)
		}
	}

	b, _, err := s.CreateOrder(ctx, sameShape(base, "sched-merge-b"))
	if err != nil {
		t.Fatal(err)
	}
	bst := &stepper{t: t, s: s, ctx: ctx, orderID: b.ID, version: b.Version}
	bst.must(domain.CmdEstimate, nil)
	b = bst.must(domain.CmdSchedule, nil)
	m, err := s.MergeOrders(ctx, Merge{Orders: []MergeRef{{res.Original.ID, res.Original.Version}, {b.ID, b.Version}}, Actor: "planner-1",
		IdempotencyKey: "sched-merge-1", RequestHash: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if jobs := jobsOf(t, s, ctx, m.Survivor.ID); len(jobs) != 1 || !jobs[0].PlannedHours.Equal(d("80")) || !jobs[0].PlannedStart.Equal(at) {
		t.Fatalf("survivor jobs: %+v", jobs)
	}
	gone, _ := s.GetOrder(ctx, b.ID)
	if gone.Status != domain.Cancelled || gone.StartDate != nil || len(jobsOf(t, s, ctx, b.ID)) != 0 || gone.Operations[0].PlannedStart != nil {
		t.Fatalf("merged order keeps a schedule: %+v", gone)
	}
}

// A sales line order has no route until its configuration resolves one: it is scheduled with no
// jobs at the instant, so it can still be released.
func TestSchedule_OrderWithoutOperations(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(190))
	in := salesLineOrder("sched-sales", ids.New(), ids.New(), ids.New(), ids.New())
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	st.must(domain.CmdEstimate, nil)
	at := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	o = st.must(domain.CmdSchedule, func(tr *Transition) {
		tr.Schedule = &ScheduleParams{Method: domain.MethodOperations, Direction: domain.Forward, At: &at}
	})
	if o.StartDate == nil || *o.StartDate != "2026-11-02" || *o.EndDate != "2026-11-02" || len(jobsOf(t, s, ctx, o.ID)) != 0 {
		t.Fatalf("order without a route: %v %v", o.StartDate, o.EndDate)
	}
	st.must(domain.CmdRelease, nil)
}

// RLS as passage_app: the jobs carry the order's legal entity, a principal scoped to one legal
// entity lists only its jobs, and the reset that deletes jobs needs only the slice's grants.
func TestListJobs_LegalEntityScopeAsAppRole(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s and %s are needed for the RLS test", testkit.EnvURL, testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(app)
	tid := testkit.TenantID(191)
	ctx := tenantCtx(tid)
	leA, leB := ids.New(), ids.New()
	steppers := map[string]*stepper{}
	for key, le := range map[string]string{"jobs-rls-a": leA, "jobs-rls-b": leB} {
		st, _ := estimatedOrder(t, s, ctx, key, le, nil)
		st.must(domain.CmdSchedule, nil)
		steppers[le] = st
	}
	if all, err := s.ListJobs(ctx, JobFilter{Limit: 100}); err != nil || all.Total != 2 {
		t.Fatalf("tenant-wide: %+v %v", all, err)
	}
	scoped := scopedCtx(tid, leA)
	in, err := s.ListJobs(scoped, JobFilter{Limit: 100})
	if err != nil || in.Total != 1 || len(in.Items) != 1 || in.Items[0].LegalEntityID != leA {
		t.Fatalf("scoped: %+v %v", in, err)
	}
	if leak, err := s.ListJobs(scoped, JobFilter{LegalEntityID: leB, Limit: 100}); err != nil || leak.Total != 0 {
		t.Fatalf("another legal entity: %+v %v", leak, err)
	}
	if leak, err := s.ListJobs(tenantCtx(testkit.TenantID(192)), JobFilter{Limit: 100}); err != nil || leak.Total != 0 {
		t.Fatalf("another tenant: %+v %v", leak, err)
	}
	st := steppers[leA]
	st.must(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Estimated })
	if left, err := s.ListJobs(ctx, JobFilter{Limit: 100}); err != nil || left.Total != 1 || left.Items[0].LegalEntityID != leB {
		t.Fatalf("after the reset: %+v %v", left, err)
	}
}
