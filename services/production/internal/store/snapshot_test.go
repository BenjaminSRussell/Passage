package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
	"github.com/BenjaminSRussell/passage/services/production/migrations"
)

// order-cost-snapshot: one row per open order and day with planned cost, actual cost and WIP; a
// second run of the day replaces the rows; Created, Ended and Cancelled orders are left out; a
// run for an earlier day counts only the postings dated up to that day.
func TestCostSnapshot_RunsPerDayAndIsIdempotent(t *testing.T) {
	pool, s := migrated(t)
	day := today()
	before := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	ctx := tenantCtx(testkit.TenantID(9601))
	le := ids.New()
	started := startedOrder(t, s, ctx, routedOrder(t, "key-snap-started", le, 10))
	started.mustPost(started.mustJournal(NewJournal{Type: domain.JournalPickingList, Proposal: domain.ProposeRemaining}).ID)
	created, _, err := s.CreateOrder(ctx, sampleOrder(t, "key-snap-created", le))
	if err != nil {
		t.Fatal(err)
	}
	ended := startedAgain(t, s, ctx, routedOrder(t, "key-snap-ended", le, 10))
	ended.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("100") })
	ended.must(domain.CmdEnd, nil)

	res, err := s.RunCostSnapshot(ctx, CostSnapshotRun{LegalEntityID: le, Date: day, Actor: "scheduler"})
	if err != nil || res.Orders != 1 || res.SnapshotDate != day {
		t.Fatalf("run: %+v %v", res, err)
	}
	page, err := s.CostSnapshots(ctx, CostSnapshotFilter{LegalEntityID: le})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("list: %+v %v", page, err)
	}
	got := page.Items[0]
	c := started.costs()
	planned := decimalSum(c.Planned)
	if got.OrderID != started.orderID || got.Status != "Started" || got.Currency != "USD" || !got.QtyOrdered.Equal(d("100")) ||
		!got.PlannedCost.Equal(planned) || !got.WIPBalance.Equal(c.WIPBalance) || !got.ActualCost.Equal(decimalSum(c.Actual)) || !got.ActualCost.IsPositive() {
		t.Fatalf("snapshot %+v, planned %s, actual %s, WIP %s", got, planned, decimalSum(c.Actual), c.WIPBalance)
	}
	for _, id := range []string{created.ID, ended.orderID} {
		if rows, err := s.CostSnapshots(ctx, CostSnapshotFilter{OrderID: id}); err != nil || len(rows.Items) != 0 {
			t.Fatalf("order %s must not be snapshotted: %+v %v", id, rows, err)
		}
	}

	// More work, then the same day again: the row is replaced, not added.
	started.mustPost(started.mustJournal(NewJournal{Type: domain.JournalRouteCard, Proposal: domain.ProposeRemaining}).ID)
	if res, err = s.RunCostSnapshot(ctx, CostSnapshotRun{LegalEntityID: le, Date: day, Actor: "scheduler"}); err != nil || res.Orders != 1 {
		t.Fatalf("second run: %+v %v", res, err)
	}
	page, _ = s.CostSnapshots(ctx, CostSnapshotFilter{LegalEntityID: le})
	if len(page.Items) != 1 || !page.Items[0].ActualCost.GreaterThan(got.ActualCost) {
		t.Fatalf("the day's row must be replaced: %+v", page.Items)
	}
	// An earlier day adds a row with the postings dated up to it (none: they are all dated today);
	// the list is newest first and filters by date and order, with a cursor.
	if _, err = s.RunCostSnapshot(ctx, CostSnapshotRun{LegalEntityID: le, Date: before, Actor: "scheduler"}); err != nil {
		t.Fatal(err)
	}
	first, err := s.CostSnapshots(ctx, CostSnapshotFilter{LegalEntityID: le, Limit: 1})
	if err != nil || len(first.Items) != 1 || first.Items[0].SnapshotDate != day || first.NextCursor == "" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	next, err := s.CostSnapshots(ctx, CostSnapshotFilter{LegalEntityID: le, Limit: 1, Cursor: first.NextCursor})
	if err != nil || len(next.Items) != 1 || next.Items[0].SnapshotDate != before || next.NextCursor != "" {
		t.Fatalf("second page: %+v %v", next, err)
	}
	if old := next.Items[0]; !old.ActualCost.IsZero() || !old.WIPBalance.IsZero() || !old.PlannedCost.Equal(planned) {
		t.Fatalf("the earlier day counts no posting dated after it: %+v", old)
	}
	if rows, err := s.CostSnapshots(ctx, CostSnapshotFilter{OrderID: started.orderID, From: day, To: day}); err != nil || len(rows.Items) != 1 {
		t.Fatalf("date filter: %+v %v", rows, err)
	}
	// Each run enqueued one costsnapshot.taken event on the legal entity.
	got3 := 0
	for _, typ := range outboxTypes(t, pool, le) {
		if typ == prodevents.CostSnapshotTaken {
			got3++
		}
	}
	if got3 != 3 {
		t.Fatalf("events: %d", got3)
	}
}

func TestCostSnapshot_TenantIsolation(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9602))
	le := ids.New()
	st := startedOrder(t, s, ctx, routedOrder(t, "key-snap-iso", le, 10))
	if _, err := s.RunCostSnapshot(ctx, CostSnapshotRun{LegalEntityID: le, Actor: "scheduler"}); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.CostSnapshots(tenantCtx(testkit.TenantID(9603)), CostSnapshotFilter{OrderID: st.orderID}); err != nil || len(rows.Items) != 0 {
		t.Fatalf("another tenant reads %+v %v", rows, err)
	}
	if res, err := s.RunCostSnapshot(tenantCtx(testkit.TenantID(9603)), CostSnapshotRun{LegalEntityID: le, Actor: "scheduler"}); err != nil || res.Orders != 0 {
		t.Fatalf("another tenant snapshots %+v %v", res, err)
	}
}

// Under the application role (RLS, and the grants of migration 0020: the upsert needs UPDATE), a
// caller scoped to one legal entity takes and reads only its own snapshots.
func TestCostSnapshot_LegalEntityScopeUnderRLS(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s and %s are needed for the RLS test", testkit.EnvURL, testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(app)
	tid := testkit.TenantID(9605)
	leA, leB := ids.New(), ids.New()
	all := tenantCtx(tid)
	startedOrder(t, s, all, routedOrder(t, "key-snap-rls-a", leA, 10))
	startedOrder(t, s, all, routedOrder(t, "key-snap-rls-b", leB, 10))
	scoped := scopedCtx(tid, leA)
	for range 2 { // the second run of the day goes through the upsert's UPDATE
		if res, err := s.RunCostSnapshot(scoped, CostSnapshotRun{LegalEntityID: leA, Actor: "scheduler"}); err != nil || res.Orders != 1 {
			t.Fatalf("own entity: %+v %v", res, err)
		}
	}
	if res, err := s.RunCostSnapshot(scoped, CostSnapshotRun{LegalEntityID: leB, Actor: "scheduler"}); err == nil && res.Orders != 0 {
		t.Fatalf("another entity's orders were snapshotted: %+v", res)
	}
	if _, err := s.RunCostSnapshot(all, CostSnapshotRun{LegalEntityID: leB, Actor: "scheduler"}); err != nil {
		t.Fatal(err)
	}
	if page, err := s.CostSnapshots(scoped, CostSnapshotFilter{}); err != nil || len(page.Items) != 1 || page.Items[0].LegalEntityID != leA {
		t.Fatalf("scoped list: %+v %v", page, err)
	}
	if page, err := s.CostSnapshots(scoped, CostSnapshotFilter{LegalEntityID: leB}); err != nil || len(page.Items) != 0 {
		t.Fatalf("a filter on another entity widens the scope: %+v %v", page, err)
	}
	if page, err := s.CostSnapshots(all, CostSnapshotFilter{}); err != nil || len(page.Items) != 2 {
		t.Fatalf("all-scope list: %+v %v", page, err)
	}
}

// Two runs of the same day at once queue on the advisory lock: both succeed and the day keeps one
// row per order.
func TestCostSnapshot_ConcurrentRunsOfADay(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(9604))
	le := ids.New()
	st := startedOrder(t, s, ctx, routedOrder(t, "key-snap-c1", le, 10))
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList, Proposal: domain.ProposeRemaining}).ID)
	startedAgain(t, s, ctx, routedOrder(t, "key-snap-c2", le, 10))
	startedAgain(t, s, ctx, routedOrder(t, "key-snap-c3", le, 10))
	errs := make(chan error, 4)
	for range 4 {
		go func() {
			_, err := s.RunCostSnapshot(ctx, CostSnapshotRun{LegalEntityID: le, Actor: "scheduler"})
			errs <- err
		}()
	}
	for range 4 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent run: %v", err)
		}
	}
	if page, err := s.CostSnapshots(ctx, CostSnapshotFilter{LegalEntityID: le}); err != nil || len(page.Items) != 3 {
		t.Fatalf("one row per order and day: %+v %v", page, err)
	}
}

func decimalSum(rows []domain.CostRow) decimal.Decimal {
	sum := decimal.Zero
	for _, r := range rows {
		sum = sum.Add(r.Amount)
	}
	return sum
}
