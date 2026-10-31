package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

func runOrder(le, customer, product, site, wh string, qty int64) NewOrder {
	in := orderOf(le, customer, product, "2027-03-01", qty)
	in.Lines[0].SiteID, in.Lines[0].WarehouseID = site, wh
	return in
}

// TestPostgres_BackorderRuns covers FR-27.17, BR-27.11 and AC-27.12: a run
// reads the open lines from the confirmed orders, previews without changing
// anything, a REDISTRIBUTE commit serves the priority-1 customer first and
// deconfirms the rest, a later WIN run promises the freed quantity on the
// supply date, atp.promised is written only for changed lines, and a commit
// refuses an order that changed since the preview.
func TestPostgres_BackorderRuns(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, product, site, wh := ids.New(), ids.New(), ids.New(), ids.New()
	a, b, c := ids.New(), ids.New(), ids.New()
	oa := confirmed(t, s, ctx, runOrder(le, a, product, site, wh, 20))
	confirmed(t, s, ctx, runOrder(le, b, product, site, wh, 20))
	oc := confirmed(t, s, ctx, runOrder(le, c, product, site, wh, 20))
	other := confirmed(t, s, ctx, runOrder(le, a, product, ids.New(), wh, 7)) // another site: not in the run
	journalIn := runOrder(le, a, product, site, wh, 9)
	journalIn.OrderType = domain.OrderTypeJournal
	confirmed(t, s, ctx, journalIn) // JOURNAL never takes part

	now := time.Now().UTC().Truncate(time.Microsecond)
	in := NewBackorderRun{LegalEntityID: le, ProductID: product, SiteID: site, WarehouseID: wh, Strategy: domain.StrategyRedistribute, Supply: d(40),
		SupplyDate: "2027-03-10", LoseThreshold: 3, DefaultPriority: 5, Priorities: map[string]int{a: 1}, IdempotencyKey: "run-1", RequestHash: "h1", Actor: "planner"}

	if _, _, err := s.CreateBackorderRun(ctx, NewBackorderRun{LegalEntityID: le, ProductID: ids.New(), SiteID: site, WarehouseID: wh, Strategy: domain.StrategyFill,
		Supply: d(1), SupplyDate: "2027-03-10", LoseThreshold: 3, DefaultPriority: 5, IdempotencyKey: "none", RequestHash: "x", Actor: "p"}, now); problemCode(t, err) != CodeBackorderRunRefused {
		t.Fatalf("no open line: %v", err)
	}
	winShort := in
	winShort.Strategy, winShort.IdempotencyKey, winShort.RequestHash = domain.StrategyWin, "win-short", "w"
	if _, _, err := s.CreateBackorderRun(ctx, winShort, now); problemCode(t, err) != CodeBackorderRunRefused {
		t.Fatalf("WIN cannot keep 60 confirmed with 40 supply: %v", err)
	}

	run, created, err := s.CreateBackorderRun(ctx, in, now)
	if err != nil || !created {
		t.Fatalf("create: %v %v", err, created)
	}
	if run.Status != "PREVIEW" || run.Version != 1 || len(run.Lines) != 3 || run.ChangedCount != 1 || run.DeconfirmedCount != 1 || !run.Allocated.Equal(d(40)) {
		t.Fatalf("preview: %+v", run)
	}
	if run.Lines[0].OrderID != oa.ID || run.Lines[0].PriorityClass != 1 || !run.Lines[0].After.Equal(d(20)) || run.Lines[0].Changed {
		t.Fatalf("priority 1 first: %+v", run.Lines[0])
	}
	last := run.Lines[2]
	if last.OrderID != oc.ID || !last.Before.Equal(d(20)) || !last.After.IsZero() || !last.Deconfirmed || last.GainDate != nil {
		t.Fatalf("last line loses: %+v", last)
	}
	if sched, _ := s.ListScheduleLines(ctx, oc.ID); len(sched) != 1 || sched[0].DeliveryDate == nil {
		t.Fatalf("a preview changes nothing: %+v", sched)
	}
	replay, created, err := s.CreateBackorderRun(ctx, in, now)
	if err != nil || created || replay.ID != run.ID {
		t.Fatalf("replay: %v %v %v", err, created, replay.ID)
	}
	in2 := in
	in2.RequestHash = "different"
	if _, _, err := s.CreateBackorderRun(ctx, in2, now); problemCode(t, err) != CodeIdempotencyReuse {
		t.Fatalf("same key, other body: %v", err)
	}
	if list, err := s.ListBackorderRuns(ctx, BackorderRunFilter{LegalEntityID: le, Status: "PREVIEW", Limit: 10}); err != nil || len(list) != 1 || len(list[0].Lines) != 0 {
		t.Fatalf("list: %v %+v", err, list)
	}

	// An order whose line the run would change, changed after the preview, refuses the commit and nothing is applied.
	price := decimal.NewFromInt(9)
	if _, err := s.ChangeOrder(ctx, oc.ID, oc.Version, ChangeRequest{Lines: []LineChange{{LineNo: 1, UnitPrice: &price}}}, "rep", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitBackorderRun(ctx, run.ID, run.Version-1, "planner", now); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("stale run version: %v", err)
	}
	if _, err := s.CommitBackorderRun(ctx, run.ID, run.Version, "planner", now); problemCode(t, err) != CodeRunStale {
		t.Fatalf("order changed since the preview: %v", err)
	}
	if got, _ := s.GetBackorderRun(ctx, run.ID); got.Status != "PREVIEW" {
		t.Fatalf("run after refused commit: %+v", got)
	}
	// A fresh preview on the current orders commits.
	in.IdempotencyKey, in.RequestHash = "run-2", "h2"
	run, _, err = s.CreateBackorderRun(ctx, in, now)
	if err != nil {
		t.Fatal(err)
	}
	done, err := s.CommitBackorderRun(ctx, run.ID, run.Version, "planner", now)
	if err != nil || done.Status != "COMMITTED" || done.Version != 2 || done.CommittedBy == nil || *done.CommittedBy != "planner" {
		t.Fatalf("commit: %v %+v", err, done)
	}
	if _, err := s.CommitBackorderRun(ctx, run.ID, done.Version, "planner", now); problemCode(t, err) != CodeRunCommitted {
		t.Fatalf("second commit: %v", err)
	}
	if _, err := s.CommitBackorderRun(ctx, run.ID, run.Version, "planner", now); problemCode(t, err) != CodeRunCommitted {
		t.Fatalf("a retried commit at the old ETag is told the run is committed: %v", err)
	}
	sched, _ := s.ListScheduleLines(ctx, oc.ID)
	if len(sched) != 1 || sched[0].Status != domain.SchedUnpromised || sched[0].DeliveryDate != nil || !sched[0].Quantity.Equal(d(20)) {
		t.Fatalf("deconfirmed line: %+v", sched)
	}
	got, err := s.GetOrder(ctx, oc.ID)
	if err != nil || got.Lines[0].ConfirmedShipDate != nil || got.Version != oc.Version+2 {
		t.Fatalf("order after commit: %v %+v", err, got)
	}
	if n := countWhere(t, pool, `select count(*) from outbox where type = $1 and aggregate_id = any($2)`, salesevents.ATPPromised, []string{oa.ID, oc.ID, other.ID}); n != 1 {
		t.Fatalf("atp.promised only for the changed line: %d", n)
	}
	var data salesevents.ATPPromisedData
	if err := json.Unmarshal(outboxData(t, pool, oc.ID, salesevents.ATPPromised), &data); err != nil {
		t.Fatal(err)
	}
	if data.Source != "BACKORDER_RUN" || data.Complete || data.BackorderQty != "20" || data.PromisedDate != "" {
		t.Fatalf("atp.promised %+v", data)
	}
	var ev salesevents.BackorderRunCommittedData
	if err := json.Unmarshal(outboxData(t, pool, run.ID, salesevents.BackorderRunCommitted), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Strategy != "REDISTRIBUTE" || len(ev.Lines) != 1 || ev.Lines[0].After != "0" || !ev.Lines[0].Deconfirmed || ev.Version != 2 {
		t.Fatalf("backorderrun.committed %+v", ev)
	}
	if back, _ := s.ListBackorders(ctx, BackorderFilter{LegalEntityID: le, Limit: 50}); len(back) != 1 || back[0].OrderID != oc.ID || back[0].Status != domain.SchedUnpromised {
		t.Fatalf("backorder list: %+v", back)
	}

	// New supply of 60 on 03-10 under WIN keeps A and B and promises C on the supply date.
	in.Strategy, in.Supply, in.IdempotencyKey, in.RequestHash = domain.StrategyWin, d(60), "run-3", "h3"
	run, _, err = s.CreateBackorderRun(ctx, in, now)
	if err != nil || run.ChangedCount != 1 || run.Lines[2].GainDate == nil || *run.Lines[2].GainDate != "2027-03-10" || run.DeconfirmedCount != 0 {
		t.Fatalf("win preview: %v %+v", err, run)
	}
	if _, err := s.CommitBackorderRun(ctx, run.ID, run.Version, "planner", now); err != nil {
		t.Fatal(err)
	}
	sched, _ = s.ListScheduleLines(ctx, oc.ID)
	if len(sched) != 1 || sched[0].Status != domain.SchedBackorder || sched[0].DeliveryDate == nil || *sched[0].DeliveryDate != "2027-03-10" ||
		sched[0].Source != domain.SchedSourceATP || sched[0].BucketDate == nil || *sched[0].BucketDate != "2027-03-10" {
		t.Fatalf("promised on the supply date: %+v", sched)
	}
	got, _ = s.GetOrder(ctx, oc.ID)
	if got.Lines[0].ConfirmedShipDate == nil || *got.Lines[0].ConfirmedShipDate != "2027-03-10" {
		t.Fatalf("confirmed date follows: %+v", got.Lines[0])
	}
	var versions int
	if err := pool.QueryRow(ctx, `select count(*) from sales_order_version where order_id = $1 and reason like 'Backorder run %'`, oc.ID).Scan(&versions); err != nil || versions != 2 {
		t.Fatalf("version rows: %d %v", versions, err)
	}
	if o, _ := s.GetOrder(ctx, other.ID); o.Version != other.Version {
		t.Fatalf("another site is untouched: %+v", o)
	}
}

// TestPostgres_BackorderRunCommitSeesOtherPromises: two previews planned on
// the same lines, each giving the same 20 units of supply to a different line,
// cannot both commit (AC-27.12: no promise beyond the supply), even though the
// second run's own orders did not move; nor can a run commit after another
// order of the product was confirmed with a promised date since its preview.
func TestPostgres_BackorderRunCommitSeesOtherPromises(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, product, site, wh := ids.New(), ids.New(), ids.New(), ids.New()
	x, y := ids.New(), ids.New()
	ox := confirmed(t, s, ctx, runOrder(le, x, product, site, wh, 20))
	oy := confirmed(t, s, ctx, runOrder(le, y, product, site, wh, 20))
	now := time.Now().UTC().Truncate(time.Microsecond)
	base := NewBackorderRun{LegalEntityID: le, ProductID: product, SiteID: site, WarehouseID: wh, Strategy: domain.StrategyRedistribute,
		SupplyDate: "2027-03-10", LoseThreshold: 3, DefaultPriority: 5, Actor: "planner"}
	plan := func(key string, supply int64, priorities map[string]int) BackorderRun {
		t.Helper()
		in := base
		in.Supply, in.Priorities, in.IdempotencyKey, in.RequestHash = d(supply), priorities, key, key
		run, _, err := s.CreateBackorderRun(ctx, in, now)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		return run
	}
	// No supply: both lines lose their promise.
	empty := plan("empty", 0, nil)
	if _, err := s.CommitBackorderRun(ctx, empty.ID, empty.Version, "planner", now); err != nil {
		t.Fatal(err)
	}
	forX := plan("for-x", 20, map[string]int{x: 1})
	forY := plan("for-y", 20, map[string]int{y: 1})
	if forX.ChangedCount != 1 || forY.ChangedCount != 1 {
		t.Fatalf("each run gains one line: %+v %+v", forX, forY)
	}
	if _, err := s.CommitBackorderRun(ctx, forX.ID, forX.Version, "planner", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitBackorderRun(ctx, forY.ID, forY.Version, "planner", now); problemCode(t, err) != CodeRunStale {
		t.Fatalf("the second run would promise 40 against 20: %v", err)
	}
	if sched, _ := s.ListScheduleLines(ctx, oy.ID); len(sched) != 1 || sched[0].DeliveryDate != nil {
		t.Fatalf("nothing of the refused run is applied: %+v", sched)
	}
	if sched, _ := s.ListScheduleLines(ctx, ox.ID); len(sched) != 1 || sched[0].DeliveryDate == nil || !sched[0].Quantity.Equal(d(20)) {
		t.Fatalf("the first run stands: %+v", sched)
	}

	// A line confirmed after the preview, with a promised date, refuses the commit too.
	late := plan("late", 40, map[string]int{y: 1})
	confirmed(t, s, ctx, runOrder(le, ids.New(), product, site, wh, 5))
	if _, err := s.CommitBackorderRun(ctx, late.ID, late.Version, "planner", now); problemCode(t, err) != CodeRunStale {
		t.Fatalf("a line promised since the preview: %v", err)
	}
	if n := countWhere(t, pool, `select count(*) from outbox where type = $1 and aggregate_id = $2`, salesevents.ATPPromised, oy.ID); n != 1 {
		t.Fatalf("only the empty run promised for Y: %d", n)
	}
}

// TestPostgres_BackorderRunScope: a run is invisible to a caller scoped to
// another legal entity or tenant, who can neither read, list nor commit it.
func TestPostgres_BackorderRunScope(t *testing.T) {
	s, _, ctx := migrated(t)
	le, product, site, wh := ids.New(), ids.New(), ids.New(), ids.New()
	confirmed(t, s, ctx, runOrder(le, ids.New(), product, site, wh, 20))
	now := time.Now().UTC().Truncate(time.Microsecond)
	run, _, err := s.CreateBackorderRun(ctx, NewBackorderRun{LegalEntityID: le, ProductID: product, SiteID: site, WarehouseID: wh, Strategy: domain.StrategyFill,
		Supply: d(0), SupplyDate: "2027-03-10", LoseThreshold: 3, DefaultPriority: 5, IdempotencyKey: "scope", RequestHash: "scope", Actor: "planner"}, now)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]context.Context{
		"other legal entity": tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: ids.New(), UserID: "x", Duties: testkit.ScopeDuties("x")}),
		"other tenant":       tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(2), UserID: "x", Duties: testkit.ScopeDuties("")}),
	} {
		if _, err := s.GetBackorderRun(c, run.ID); problemCode(t, err) != CodeRunNotFound {
			t.Fatalf("%s: get %v", name, err)
		}
		if _, err := s.CommitBackorderRun(c, run.ID, run.Version, "x", now); problemCode(t, err) != CodeRunNotFound {
			t.Fatalf("%s: commit %v", name, err)
		}
		if list, err := s.ListBackorderRuns(c, BackorderRunFilter{Limit: 50}); err != nil || len(list) != 0 {
			t.Fatalf("%s: list %v %+v", name, err, list)
		}
		in := NewBackorderRun{LegalEntityID: le, ProductID: product, SiteID: site, WarehouseID: wh, Strategy: domain.StrategyFill, Supply: d(20),
			SupplyDate: "2027-03-10", LoseThreshold: 3, DefaultPriority: 5, IdempotencyKey: name, RequestHash: name, Actor: "x"}
		if _, _, err := s.CreateBackorderRun(c, in, now); problemCode(t, err) != CodeBackorderRunRefused {
			t.Fatalf("%s: the lines are not visible: %v", name, err)
		}
	}
	own := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "x", Duties: testkit.ScopeDuties(le)})
	if got, err := s.GetBackorderRun(own, run.ID); err != nil || got.ID != run.ID {
		t.Fatalf("own legal entity: %v", err)
	}
}
