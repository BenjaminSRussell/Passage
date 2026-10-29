package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	"github.com/BenjaminSRussell/passage/services/eam/migrations"
)

var clock = time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

// fixture migrates a fresh schema and returns a repository on a fixed clock
// with one ACTIVE asset that has a counter. Tests skip without a database.
type fixture struct {
	ctx     context.Context
	pool    *pgxpool.Pool
	s       *Postgres
	t       string
	asset   string
	counter string
}

func setup(t *testing.T) fixture {
	t.Helper()
	pool := testkit.DB(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "EamPlanner", "EamPlanAdmin", "EamCounterRegister")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := NewWithClock(pool, func() time.Time { return clock })
	a, err := s.CreateAsset(ctx, Asset{Name: "Feed pump", AssetType: "PUMP", SiteID: testkit.TenantID(9), Criticality: 4}, "asset-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateAsset(ctx, a.ID, a.Version); err != nil {
		t.Fatal(err)
	}
	c, err := s.CreateCounter(ctx, a.ID, Counter{Name: "run hours", Unit: "h", Cumulative: true, Source: "MANUAL"}, "counter-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutFaultCatalog(ctx, "PUMP", pumpCatalog(), 0); err != nil {
		t.Fatal(err)
	}
	return fixture{ctx: ctx, pool: pool, s: s, t: testkit.TenantID(1), asset: a.ID, counter: c.ID}
}

// pumpCatalog is the PUMP fault catalog every fixture starts with (FR-44.16).
func pumpCatalog() domain.FaultCatalog {
	return domain.FaultCatalog{
		Symptoms:     []domain.FaultCode{{Code: "NOISE", Text: "Abnormal noise"}, {Code: "LEAK", Text: "Leak"}},
		Areas:        []domain.FaultCode{{Code: "BEARING", Text: "Bearing"}, {Code: "SEAL", Text: "Seal"}},
		Types:        []domain.FaultCode{{Code: "WEAR", Text: "Wear"}},
		Causes:       []domain.FaultCode{{Code: "FATIGUE", Text: "Fatigue"}},
		Remedies:     []domain.FaultCode{{Code: "REPLACE", Text: "Replace"}},
		Combinations: []domain.FaultCombination{{Symptom: "NOISE", Area: "BEARING", Type: "WEAR"}, {Symptom: "LEAK", Area: "SEAL", Type: "WEAR"}},
	}
}

var bearingWear = domain.Fault{Symptom: "NOISE", Area: "BEARING", Type: "WEAR", Cause: "FATIGUE", Remedy: "REPLACE"}

// fault registers the bearing-wear fault on a work order, so a CORRECTIVE or
// EMERGENCY order can complete (EAM-FLT-001).
func (f fixture) fault(t *testing.T, woID string) {
	t.Helper()
	if _, err := f.s.RegisterFault(f.ctx, woID, bearingWear, "", "tech-1", ""); err != nil {
		t.Fatal(err)
	}
}

// inTenant runs a read-only check inside a tenant-scoped transaction (RLS).
func (f fixture) inTenant(t *testing.T, fn func(tx pgx.Tx)) {
	t.Helper()
	err := dbx.WithTenantTx(f.ctx, f.pool, func(tx pgx.Tx) error { fn(tx); return nil })
	if err != nil {
		t.Fatal(err)
	}
}

// advance moves a work order through steps from version and returns the new version.
func (f fixture) advance(t *testing.T, woID string, version int, steps ...domain.Status) int {
	t.Helper()
	for _, st := range steps {
		wo, err := f.s.TransitionWorkOrder(f.ctx, woID, Transition{To: st, Version: version}, "")
		if err != nil {
			t.Fatalf("to %s: %v", st, err)
		}
		version = wo.Version
	}
	return version
}

// count runs a count(*) query inside the tenant.
func (f fixture) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	f.inTenant(t, func(tx pgx.Tx) {
		if err := tx.QueryRow(f.ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
	})
	return n
}

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

// TestPlanLine_GeneratesOneWorkOrderPerDueDate covers AC1 for a TIME line:
// the first order is generated when the plan is created, a rerun creates no
// duplicate, and completion resets the line so the next order follows the
// completion date (BR-44.1, BR-44.2).
func TestPlanLine_GeneratesOneWorkOrderPerDueDate(t *testing.T) {
	f := setup(t)
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	plan, err := f.s.CreatePlan(f.ctx, PlanInput{Code: "PM-PUMP", Name: "Pump PM", Currency: "USD",
		Lines: []PlanLineInput{{AssetID: f.asset, Kind: "TIME", IntervalValue: ptr(30), IntervalUnit: ptr("DAYS"),
			JobType: "LUBE", StartAt: &start}}}, "plan-1")
	if err != nil {
		t.Fatal(err)
	}
	lineID := plan.LineIDs[0]
	var due time.Time
	f.inTenant(t, func(tx pgx.Tx) {
		if err := tx.QueryRow(f.ctx, `select due_at from work_order where tenant_id = $1 and plan_line_id = $2`,
			f.t, lineID).Scan(&due); err != nil {
			t.Fatal(err)
		}
	})
	if want := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC); !due.Equal(want) {
		t.Fatalf("first due = %v, want %v", due, want)
	}

	res, err := f.s.SchedulePlans(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Generated != 0 {
		t.Fatalf("rerun generated %d orders, want 0 (open order exists)", res.Generated)
	}

	var woID string
	f.inTenant(t, func(tx pgx.Tx) {
		if err := tx.QueryRow(f.ctx, `select id::text from work_order where tenant_id = $1 and plan_line_id = $2`,
			f.t, lineID).Scan(&woID); err != nil {
			t.Fatal(err)
		}
	})
	version := f.advance(t, woID, 1, domain.StatusScheduled, domain.StatusReleased, domain.StatusInProgress)
	begin := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	end := time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)
	if _, err := f.s.TransitionWorkOrder(f.ctx, woID, Transition{To: domain.StatusCompleted, Version: version,
		ActualStart: &begin, ActualEnd: &end}, "complete-1"); err != nil {
		t.Fatal(err)
	}

	var orders int
	var nextDue time.Time
	f.inTenant(t, func(tx pgx.Tx) {
		if err := tx.QueryRow(f.ctx, `select count(*) from work_order where tenant_id = $1 and plan_line_id = $2`,
			f.t, lineID).Scan(&orders); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(f.ctx, `select next_due_at from plan_line where tenant_id = $1 and id = $2`,
			f.t, lineID).Scan(&nextDue); err != nil {
			t.Fatal(err)
		}
	})
	if orders != 2 {
		t.Fatalf("orders = %d, want 2 after completion reschedules", orders)
	}
	if want := time.Date(2026, 11, 30, 10, 0, 0, 0, time.UTC); !nextDue.Equal(want) {
		t.Fatalf("next due after completion = %v, want %v", nextDue, want)
	}
}

// TestCondition_BreachCreatesOneDedupedRequest covers AC4: the first breach
// creates a request and a second breach inside the cooldown creates none.
func TestCondition_BreachCreatesOneDedupedRequest(t *testing.T) {
	f := setup(t)
	temp, err := f.s.CreateCounter(f.ctx, f.asset, Counter{Name: "bearing temp", Unit: "C", Cumulative: false, Source: "IOT"}, "temp-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CreateRule(f.ctx, temp.ID, Rule{Name: "bearing hot", Operator: ">", Threshold: 85, Severity: 2}, "rule-1"); err != nil {
		t.Fatal(err)
	}
	at := clock.Add(-time.Hour)
	if _, _, err := f.s.RecordReading(f.ctx, temp.ID, ReadingInput{Value: 87, ReadingAt: at, Source: "IOT", MessageID: "m1"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.RecordReading(f.ctx, temp.ID, ReadingInput{Value: 88, ReadingAt: at.Add(time.Minute), Source: "IOT", MessageID: "m2"}, ""); err != nil {
		t.Fatal(err)
	}
	var requests int
	f.inTenant(t, func(tx pgx.Tx) {
		if err := tx.QueryRow(f.ctx, `select count(*) from maintenance_request where tenant_id = $1 and source = 'IOT'`,
			f.t).Scan(&requests); err != nil {
			t.Fatal(err)
		}
	})
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

// TestCounter_RejectsDecreaseWithoutReset covers AC6 (EAM-CTR-001) and the
// replay of a repeated idempotency key.
func TestCounter_RejectsDecreaseWithoutReset(t *testing.T) {
	f := setup(t)
	if _, _, err := f.s.RecordReading(f.ctx, f.counter, ReadingInput{Value: 12300, ReadingAt: clock, Source: "MANUAL"}, "r-1"); err != nil {
		t.Fatal(err)
	}
	_, _, err := f.s.RecordReading(f.ctx, f.counter, ReadingInput{Value: 12250, ReadingAt: clock.Add(time.Hour), Source: "MANUAL"}, "r-2")
	if code := problemCode(err); code != domain.CodeNonMonotonic {
		t.Fatalf("code = %q, want %s (err %v)", code, domain.CodeNonMonotonic, err)
	}
	if _, replayed, err := f.s.RecordReading(f.ctx, f.counter, ReadingInput{Value: 12300, ReadingAt: clock, Source: "MANUAL"}, "r-1"); err != nil || !replayed {
		t.Fatalf("replay: replayed=%v err=%v", replayed, err)
	}
}

// TestLabor_CostRollsToWorkOrder covers the labor part of AC2: 8 h at 45.00
// books 360.00 on the work order.
func TestLabor_CostRollsToWorkOrder(t *testing.T) {
	f := setup(t)
	wo, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: f.asset, Type: "CORRECTIVE", Status: "CREATED",
		Priority: 3, Currency: "USD"}, "wo-1")
	if err != nil {
		t.Fatal(err)
	}
	wo, err = f.s.RecordLabor(f.ctx, wo.ID, decimal.NewFromInt(8), decimal.NewFromInt(45), nil, "labor-1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := decimal.NewFromString(wo.LaborCost)
	if err != nil || !got.Equal(decimal.NewFromInt(360)) {
		t.Fatalf("labor cost = %s (%v), want 360", wo.LaborCost, err)
	}
}

// TestScheduler_BR441Example covers AC-44.1 end to end: a TIME_OR_COUNTER line
// of 90 days or 500 h, last completed 2026-08-01 at 12,000 h, with the counter
// at 12,300 h on 2026-10-21 after 30 days at 6.5 h/day. Scheduling on
// 2026-10-21 creates a work order due 2026-10-30 with next due reading 12,500.
func TestScheduler_BR441Example(t *testing.T) {
	f := setup(t)
	for i, r := range []struct {
		at time.Time
		v  float64
	}{{clock.AddDate(0, 0, -30), 12105}, {clock.AddDate(0, 0, -10), 12235}, {clock, 12300}} {
		if _, _, err := f.s.RecordReading(f.ctx, f.counter, ReadingInput{Value: r.v, ReadingAt: r.at, Source: "MANUAL"},
			"read-"+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	var usage float64
	f.inTenant(t, func(tx pgx.Tx) {
		if err := tx.QueryRow(f.ctx, `select avg_daily_usage::float8 from counter where id = $1`, f.counter).Scan(&usage); err != nil {
			t.Fatal(err)
		}
	})
	if usage != 6.5 {
		t.Fatalf("rolling 30-day usage = %v, want 6.5", usage)
	}
	completed := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	plan, err := f.s.CreatePlan(f.ctx, PlanInput{Code: "PM-BR441", Name: "Pump 90 d / 500 h", Currency: "USD",
		Lines: []PlanLineInput{{AssetID: f.asset, Kind: "TIME_OR_COUNTER", IntervalValue: ptr(90), IntervalUnit: ptr("DAYS"),
			CounterID: &f.counter, CounterInterval: ptr(500.0), JobType: "OVERHAUL", StartAt: &completed,
			LastCompletedAt: &completed, LastCompletedReading: ptr(12000.0)}}}, "plan-br441")
	if err != nil {
		t.Fatal(err)
	}
	var due time.Time
	var reading float64
	f.inTenant(t, func(tx pgx.Tx) {
		if err := tx.QueryRow(f.ctx, `select w.due_at, pl.next_due_reading::float8 from work_order w
		  join plan_line pl on pl.id = w.plan_line_id where w.plan_line_id = $1`, plan.LineIDs[0]).Scan(&due, &reading); err != nil {
			t.Fatal(err)
		}
	})
	if want := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC); !due.Equal(want) || reading != 12500 {
		t.Fatalf("due = %v reading = %v, want %v and 12500", due.UTC(), reading, want)
	}
	page, err := f.s.ListPlans(f.ctx, ListOptions{Limit: 10, AssetID: f.asset})
	if err != nil || len(page.Items) != 1 || page.Items[0].Lines[0].NextDueForecastAt == nil {
		t.Fatalf("plan list = %+v, %v; want one plan with a forecast", page, err)
	}
	if got := page.Items[0].Lines[0].NextDueForecastAt.UTC().Format("2006-01-02"); got != "2026-11-21" {
		t.Fatalf("forecast = %s, want 2026-11-21 (BR-44.1 example)", got)
	}
}

// A reading arriving late is checked against its neighbours in time and does
// not move the counter's last value.
func TestCounter_OutOfOrderReadings(t *testing.T) {
	f := setup(t)
	for _, r := range []ReadingInput{
		{Value: 100, ReadingAt: clock.AddDate(0, 0, -2), Source: "IOT", MessageID: "a"},
		{Value: 200, ReadingAt: clock, Source: "IOT", MessageID: "c"},
	} {
		if _, _, err := f.s.RecordReading(f.ctx, f.counter, r, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := f.s.RecordReading(f.ctx, f.counter, ReadingInput{Value: 250, ReadingAt: clock.AddDate(0, 0, -1),
		Source: "IOT", MessageID: "b-high"}, ""); problemCode(err) != domain.CodeNonMonotonic {
		t.Fatalf("a late reading above the later one: err %v, want %s", err, domain.CodeNonMonotonic)
	}
	if _, _, err := f.s.RecordReading(f.ctx, f.counter, ReadingInput{Value: 90, ReadingAt: clock.AddDate(0, 0, -1),
		Source: "IOT", MessageID: "b-low"}, ""); problemCode(err) != domain.CodeNonMonotonic {
		t.Fatalf("a late reading below the earlier one: err %v, want %s", err, domain.CodeNonMonotonic)
	}
	if _, _, err := f.s.RecordReading(f.ctx, f.counter, ReadingInput{Value: 150, ReadingAt: clock.AddDate(0, 0, -1),
		Source: "IOT", MessageID: "b"}, ""); err != nil {
		t.Fatal(err)
	}
	_, replayed, err := f.s.RecordReading(f.ctx, f.counter, ReadingInput{Value: 150, ReadingAt: clock.AddDate(0, 0, -1),
		Source: "IOT", MessageID: "b"}, "")
	if err != nil || !replayed {
		t.Fatalf("same message id must replay: replayed=%v err=%v", replayed, err)
	}
	detail, err := f.s.GetAsset(f.ctx, f.asset)
	if err != nil {
		t.Fatal(err)
	}
	c := detail.Counters[0]
	if c.LastReading != 200 || c.LastReadingAt == nil || !c.LastReadingAt.Equal(clock) || c.AvgDailyUsage != 50 {
		t.Fatalf("counter = %+v, want last 200 at %v and 50/day", c, clock)
	}
	readings, err := f.s.ListReadings(f.ctx, f.counter, 10)
	if err != nil || len(readings) != 3 || readings[0].Value != 200 {
		t.Fatalf("readings = %+v, %v; want 3, newest first", readings, err)
	}
}

// TestCondition_CooldownAndConversion covers BR-44.4 beyond AC-44.4: while
// the request is open no new one is raised even after the cooldown; once it is
// converted, the next breach after the cooldown raises a new request.
func TestCondition_CooldownAndConversion(t *testing.T) {
	f := setup(t)
	temp, err := f.s.CreateCounter(f.ctx, f.asset, Counter{Name: "bearing temp", Unit: "C", Cumulative: false, Source: "IOT"}, "temp-1")
	if err != nil {
		t.Fatal(err)
	}
	rule, err := f.s.CreateRule(f.ctx, temp.ID, Rule{Name: "bearing hot", Operator: ">", Threshold: 85, Severity: 1}, "rule-1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.s.CreateRule(f.ctx, temp.ID, Rule{Name: "bearing hot", Operator: ">", Threshold: 85, Severity: 1}, "rule-1")
	if err != nil || again.ID != rule.ID {
		t.Fatalf("repeated key must replay the rule: %+v %v", again, err)
	}
	read := func(v float64, at time.Time, msg string) {
		t.Helper()
		if _, _, err := f.s.RecordReading(f.ctx, temp.ID, ReadingInput{Value: v, ReadingAt: at, Source: "IOT", MessageID: msg}, ""); err != nil {
			t.Fatal(err)
		}
	}
	read(87, clock, "m1")
	read(88, clock.Add(2*time.Hour), "m2") // past the cooldown, but the first request is still NEW
	const iot = `select count(*) from maintenance_request where source = 'IOT'`
	if n := f.count(t, iot); n != 1 {
		t.Fatalf("requests = %d, want 1 while the first is open", n)
	}
	reqs, err := f.s.ListRequests(f.ctx, ListOptions{Limit: 10, Status: "NEW"})
	if err != nil || len(reqs.Items) != 1 {
		t.Fatalf("open requests = %+v %v", reqs, err)
	}
	wo, err := f.s.ConvertRequest(f.ctx, reqs.Items[0].ID, "USD", "convert-1")
	if err != nil {
		t.Fatal(err)
	}
	if wo.Type != "EMERGENCY" || wo.Priority != 1 || wo.RequestID == nil {
		t.Fatalf("severity-1 request converted to %+v, want EMERGENCY priority 1", wo)
	}
	read(89, clock.Add(2*time.Hour+30*time.Minute), "m3") // the rule last fired at m1, outside the cooldown
	if n := f.count(t, iot); n != 2 {
		t.Fatalf("requests = %d, want 2 after the first was converted", n)
	}
	read(90, clock.Add(2*time.Hour+40*time.Minute), "m4")
	if n := f.count(t, iot); n != 2 {
		t.Fatalf("requests = %d, want still 2 (the second is open)", n)
	}
	if n := f.count(t, `select count(*) from outbox where type = 'dev.passage.eam.request.created.v1'`); n != 2 {
		t.Fatalf("request.created events = %d, want 2", n)
	}
}

// Conversion: CORRECTIVE at 6 − criticality, one action, replay by key, and a
// converted request cannot convert again (EAM-REQ-001).
func TestConvertRequest(t *testing.T) {
	f := setup(t)
	req, err := f.s.CreateRequest(f.ctx, Request{AssetID: f.asset, Source: "USER", Severity: 3, Description: "Leaking seal"}, "req-1")
	if err != nil {
		t.Fatal(err)
	}
	wo, err := f.s.ConvertRequest(f.ctx, req.ID, "EUR", "conv-1")
	if err != nil {
		t.Fatal(err)
	}
	if wo.Type != "CORRECTIVE" || wo.Priority != 2 || wo.Currency != "EUR" || wo.Status != "CREATED" {
		t.Fatalf("work order = %+v, want CORRECTIVE priority 2 (criticality 4) in EUR", wo)
	}
	replay, err := f.s.ConvertRequest(f.ctx, req.ID, "EUR", "conv-1")
	if err != nil || replay.ID != wo.ID {
		t.Fatalf("replay = %+v %v, want the same order", replay, err)
	}
	if _, err := f.s.ConvertRequest(f.ctx, req.ID, "EUR", "conv-2"); problemCode(err) != CodeRequestState {
		t.Fatalf("second conversion: %v, want %s", err, CodeRequestState)
	}
	page, err := f.s.ListRequests(f.ctx, ListOptions{Limit: 10, Status: "CONVERTED"})
	if err != nil || len(page.Items) != 1 || page.Items[0].WorkOrderID == nil || *page.Items[0].WorkOrderID != wo.ID {
		t.Fatalf("converted requests = %+v %v", page, err)
	}
}

// Work order creation rules: ACTIVE asset only (EAM-AST-001), CAPITAL needs a
// fixed asset (EAM-WO-005), priority defaults from type and criticality.
func TestCreateWorkOrder_Rules(t *testing.T) {
	f := setup(t)
	idle, err := f.s.CreateAsset(f.ctx, Asset{Name: "Spare motor", AssetType: "MOTOR", SiteID: testkit.TenantID(9), Criticality: 2}, "asset-idle")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: idle.ID, Type: "CORRECTIVE", Currency: "USD"}, "wo-idle"); problemCode(err) != CodeAssetState {
		t.Fatalf("work order on a CREATED asset: %v, want %s", err, CodeAssetState)
	}
	if _, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: f.asset, Type: "CAPITAL", Currency: "USD"}, "wo-cap"); problemCode(err) != CodeCapital {
		t.Fatalf("CAPITAL work order: %v, want %s", err, CodeCapital)
	}
	for typ, want := range map[string]int{"CORRECTIVE": 2, "PREVENTIVE": 3, "EMERGENCY": 1} {
		wo, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: f.asset, Type: typ, Currency: "USD"}, "wo-"+typ)
		if err != nil || wo.Priority != want {
			t.Fatalf("%s: %+v %v, want priority %d", typ, wo, err, want)
		}
	}
	wo, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: f.asset, Type: "EMERGENCY", Priority: 4, Currency: "USD"}, "wo-em2")
	if err != nil || wo.Priority != 1 {
		t.Fatalf("EMERGENCY with priority 4: %+v %v, want priority 1", wo, err)
	}
}

// Transitions: optimistic version (412), state machine (EAM-WO-001), replay by
// key, emergency start before release (BR-44.7), history and events.
func TestTransitionWorkOrder(t *testing.T) {
	f := setup(t)
	wo, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: f.asset, Type: "CORRECTIVE", Currency: "USD"}, "wo-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, Transition{To: domain.StatusInProgress, Version: 1}, "t-0"); problemCode(err) != CodeWOTransition {
		t.Fatalf("CREATED to IN_PROGRESS: %v, want %s", err, CodeWOTransition)
	}
	first, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, Transition{To: domain.StatusScheduled, Version: 1, Actor: "planner-1"}, "t-1")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, Transition{To: domain.StatusScheduled, Version: 1}, "t-1")
	if err != nil || replay.Version != first.Version || replay.Status != "SCHEDULED" {
		t.Fatalf("replay = %+v %v, want version %d", replay, err, first.Version)
	}
	if _, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, Transition{To: domain.StatusReleased, Version: 1}, "t-2"); problemCode(err) != CodeVersion {
		t.Fatalf("stale version: %v, want %s", err, CodeVersion)
	}
	end := clock.Add(-time.Hour)
	start := clock
	v := f.advance(t, wo.ID, first.Version, domain.StatusReleased, domain.StatusInProgress)
	if _, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, Transition{To: domain.StatusCompleted, Version: v, ActualStart: &start, ActualEnd: &end}, "t-bad"); problemCode(err) != CodeWOTimes {
		t.Fatalf("end before start: %v, want %s", err, CodeWOTimes)
	}
	f.fault(t, wo.ID)
	done, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, Transition{To: domain.StatusCompleted, Version: v, DowntimeMinutes: ptr(90)}, "t-done")
	if err != nil || done.DowntimeMinutes != 90 || done.ActualEnd == nil {
		t.Fatalf("completion = %+v %v", done, err)
	}
	detail, err := f.s.GetWorkOrder(f.ctx, wo.ID)
	if err != nil || len(detail.History) != 4 || detail.History[0].To != "SCHEDULED" || deref(detail.History[0].Actor, "") != "planner-1" {
		t.Fatalf("history = %+v %v", detail.History, err)
	}
	if n := f.count(t, `select count(*) from outbox where aggregate_id = $1 and type = 'dev.passage.eam.workorderstatus.changed.v1'`, wo.ID); n != 4 {
		t.Fatalf("status events = %d, want 4", n)
	}
	if n := f.count(t, `select count(*) from outbox where aggregate_id = $1 and type = 'dev.passage.eam.workorder.completed.v1'
	    and payload->'data'->>'downtimeMinutes' = '90'`, wo.ID); n != 1 {
		t.Fatalf("completed events with downtime = %d, want 1", n)
	}

	em, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: f.asset, Type: "EMERGENCY", Currency: "USD"}, "wo-em")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.TransitionWorkOrder(f.ctx, em.ID, Transition{To: domain.StatusInProgress, Version: em.Version}, "em-1"); err != nil {
		t.Fatalf("emergency start before release: %v", err)
	}
}

// TestLabor_ReplayAndClosedOrder: a repeated key books once; a completed order takes no labor.
func TestLabor_ReplayAndClosedOrder(t *testing.T) {
	f := setup(t)
	wo, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: f.asset, Type: "CORRECTIVE", Currency: "USD"}, "wo-1")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := f.s.RecordLabor(f.ctx, wo.ID, decimal.RequireFromString("2.5"), decimal.RequireFromString("40.10"), nil, "lab-1"); err != nil {
			t.Fatal(err)
		}
	}
	detail, err := f.s.GetWorkOrder(f.ctx, wo.ID)
	if err != nil || len(detail.Labor) != 1 || !decimal.RequireFromString(detail.LaborCost).Equal(decimal.RequireFromString("100.25")) {
		t.Fatalf("labor = %+v cost %s %v, want one line of 100.25", detail.Labor, detail.LaborCost, err)
	}
	f.fault(t, wo.ID)
	v := f.advance(t, wo.ID, detail.Version, domain.StatusScheduled, domain.StatusReleased, domain.StatusInProgress, domain.StatusCompleted)
	_ = v
	if _, err := f.s.RecordLabor(f.ctx, wo.ID, decimal.NewFromInt(1), decimal.NewFromInt(1), nil, "lab-2"); problemCode(err) != CodeWOTransition {
		t.Fatalf("labor on a completed order: %v, want %s", err, CodeWOTransition)
	}
	a, err := f.s.GetAsset(f.ctx, f.asset)
	if err != nil || !decimal.RequireFromString(a.LaborCost).Equal(decimal.RequireFromString("100.25")) {
		t.Fatalf("asset labor rollup = %s %v, want 100.25", a.LaborCost, err)
	}
}

// TestAssetKPI_AC448: three corrective repairs and 2,700 h of uptime give MTBF 900 h.
func TestAssetKPI_AC448(t *testing.T) {
	f := setup(t)
	from := clock.Add(-2710 * time.Hour)
	for i, hours := range []int{2, 3, 5} {
		wo, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: f.asset, Type: "CORRECTIVE", Currency: "USD"}, "kpi-"+string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		f.fault(t, wo.ID)
		v := f.advance(t, wo.ID, wo.Version, domain.StatusScheduled, domain.StatusReleased)
		start := from.Add(time.Duration(100*(i+1)) * time.Hour)
		end := start.Add(time.Duration(hours) * time.Hour)
		if _, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, Transition{To: domain.StatusInProgress, Version: v, ActualStart: &start}, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, Transition{To: domain.StatusCompleted, Version: v + 1, ActualEnd: &end,
			DowntimeMinutes: ptr(hours * 60)}, ""); err != nil {
			t.Fatal(err)
		}
	}
	k, err := f.s.AssetKPI(f.ctx, f.asset, from, clock)
	if err != nil {
		t.Fatal(err)
	}
	if k.Failures != 3 || k.MTBFHours != 900 || k.MTTRHours < 3.33 || k.MTTRHours > 3.34 {
		t.Fatalf("kpi = %+v, want 3 failures, MTBF 900, MTTR 3.33", k)
	}
}

// Lists page newest first with a cursor; the workspace counts open work.
func TestListsAndWorkspace(t *testing.T) {
	f := setup(t)
	due := clock.Add(48 * time.Hour)
	late := clock.Add(-48 * time.Hour)
	for i, d := range []*time.Time{&due, &late, nil} {
		if _, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: f.asset, Type: "INSPECTION", Currency: "USD", DueAt: d},
			"ws-"+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.CreateRequest(f.ctx, Request{AssetID: f.asset, Source: "MOBILE", Severity: 4, Description: "Noise"}, "ws-req"); err != nil {
		t.Fatal(err)
	}
	ws, err := f.s.Workspace(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ws != (Workspace{RequestsToTriage: 1, DueIn7Days: 1, Overdue: 1, OpenWorkOrders: 3}) {
		t.Fatalf("workspace = %+v", ws)
	}
	p1, err := f.s.ListWorkOrders(f.ctx, ListOptions{Limit: 2, AssetID: f.asset})
	if err != nil || len(p1.Items) != 2 || p1.NextCursor == "" {
		t.Fatalf("page 1 = %+v %v", p1, err)
	}
	p2, err := f.s.ListWorkOrders(f.ctx, ListOptions{Limit: 2, AssetID: f.asset, Cursor: p1.NextCursor})
	if err != nil || len(p2.Items) != 1 || p2.NextCursor != "" || p2.Items[0].ID == p1.Items[1].ID {
		t.Fatalf("page 2 = %+v %v", p2, err)
	}
	assets, err := f.s.ListAssets(f.ctx, ListOptions{Limit: 10, Status: "ACTIVE"})
	if err != nil || len(assets.Items) != 1 || assets.Items[0].ID != f.asset {
		t.Fatalf("active assets = %+v %v", assets, err)
	}
	if _, err := f.s.CreatePlan(f.ctx, PlanInput{Code: "PM-1", Name: "x", Currency: "USD", Lines: []PlanLineInput{{AssetID: f.asset,
		Kind: "TIME", IntervalValue: ptr(1), IntervalUnit: ptr("YEARS"), JobType: "CHECK"}}}, "p-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CreatePlan(f.ctx, PlanInput{Code: "PM-1", Name: "x", Currency: "USD", Lines: []PlanLineInput{{AssetID: f.asset,
		Kind: "TIME", IntervalValue: ptr(1), IntervalUnit: ptr("YEARS"), JobType: "CHECK"}}}, "p-2"); problemCode(err) != CodeDuplicate {
		t.Fatalf("duplicate plan code: %v, want %s", err, CodeDuplicate)
	}
	if _, err := f.s.CreateCounter(f.ctx, f.asset, Counter{Name: "run hours", Unit: "h", Cumulative: true, Source: "MANUAL"}, "counter-2"); problemCode(err) != CodeDuplicate {
		t.Fatalf("duplicate counter name: %v, want %s", err, CodeDuplicate)
	}
}

// TestRLS_TenantIsolation runs the store as the application role (RLS in
// force): another tenant sees none of tenant 1's rows. It needs
// PASSAGE_TEST_APP_DATABASE_URL for a non-owner role named passage_app.
func TestRLS_TenantIsolation(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skip("needs PASSAGE_TEST_DATABASE_URL and PASSAGE_TEST_APP_DATABASE_URL")
	}
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := admin.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"asset", "counter", "counter_reading", "maintenance_plan", "plan_line", "work_order",
		"work_order_transition", "work_order_labor", "work_order_part", "maintenance_request", "condition_rule", "outbox", "idempotency_keys",
		"criticality_assessment", "fault_catalog", "fault_code", "fault_combination", "work_order_fault", "downtime_window",
		"maintenance_request_triage", "asset_warranty"} {
		var enabled, forced bool
		if err := admin.QueryRow(ctx, `select relrowsecurity, relforcerowsecurity from pg_class
		  where relname = $1 and relnamespace = $2::regnamespace`, table, schema).Scan(&enabled, &forced); err != nil {
			t.Fatal(table, err)
		}
		if !enabled || !forced {
			t.Errorf("%s: rls enabled=%v forced=%v", table, enabled, forced)
		}
	}
	s := NewWithClock(app, func() time.Time { return clock })
	one := testkit.Ctx(testkit.TenantID(1))
	two := testkit.Ctx(testkit.TenantID(2))
	a, err := s.CreateAsset(one, Asset{Name: "Pump", AssetType: "PUMP", SiteID: testkit.TenantID(9), Criticality: 3}, "k")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAsset(two, a.ID); problemCode(err) != CodeNotFound {
		t.Fatalf("tenant 2 reading tenant 1's asset: %v, want %s", err, CodeNotFound)
	}
	page, err := s.ListAssets(two, ListOptions{Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 asset list = %+v %v, want empty", page, err)
	}
	// The same idempotency key in another tenant creates a separate asset.
	b, err := s.CreateAsset(two, Asset{Name: "Pump", AssetType: "PUMP", SiteID: testkit.TenantID(9), Criticality: 3}, "k")
	if err != nil || b.ID == a.ID {
		t.Fatalf("tenant 2 asset = %+v %v", b, err)
	}
	// Fault catalogs and warranties are tenant data too.
	if _, err := s.PutFaultCatalog(one, "PUMP", pumpCatalog(), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetFaultCatalog(two, "PUMP"); problemCode(err) != CodeNotFound {
		t.Fatalf("tenant 2 reading tenant 1's catalog: %v, want %s", err, CodeNotFound)
	}
	if c, err := s.PutFaultCatalog(two, "PUMP", pumpCatalog(), 0); err != nil || c.Version != 1 {
		t.Fatalf("tenant 2 own catalog = %+v %v, want version 1", c, err)
	}
	if _, err := s.CreateWarranty(one, a.ID, Warranty{WarrantyType: "OEM", VendorPartyID: testkit.TenantID(8), StartBasis: "PURCHASE",
		StartsOn: "2026-01-01", Months: 12, PartsCovered: true}, "w"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListWarranties(two, a.ID); problemCode(err) != CodeNotFound {
		t.Fatalf("tenant 2 listing tenant 1's warranties: %v, want %s", err, CodeNotFound)
	}
}
