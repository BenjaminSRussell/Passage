package store_test

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
)

// TestReadProjectionSource reads the stored net requirements of the current
// version for chosen items inside a window, and the projection built from them
// closes on the engine's own PAB (FR-22.21, FR-22.22).
func TestReadProjectionSource(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")

	if _, err := st.ReadProjectionSource(ctx, "MAIN", []string{ids.New()}, 90); problemCode(err) != "PLAN-VERSION-NOT-FOUND" {
		t.Fatalf("plan without a version: %v", err)
	}

	res, item := latePlan(t)
	if _, _, err := st.RunPlan(ctx, runRequest(res, "p-1", "in-p1")); err != nil {
		t.Fatal(err)
	}
	unplanned := ids.New()
	src, err := st.ReadProjectionSource(ctx, "MAIN", []string{item, unplanned}, 90)
	if err != nil {
		t.Fatal(err)
	}
	if src.Version != 1 || src.AsOf != "2026-11-02" || !ids.Valid(src.SiteID) {
		t.Fatalf("version %d as-of %s site %q", src.Version, src.AsOf, src.SiteID)
	}
	rows := src.Rows[item]
	if len(rows) != len(res.Net) || len(src.Rows[unplanned]) != 0 {
		t.Fatalf("rows %+v; engine has %d, unplanned item has %d", rows, len(res.Net), len(src.Rows[unplanned]))
	}

	var days []domain.NetDay
	for _, n := range rows {
		days = append(days, domain.NetDay{
			Day: dayOffset(t, n.Date), Gross: dec(t, n.GrossRequirements), Scheduled: dec(t, n.ScheduledReceipts),
			Planned: dec(t, n.PlannedReceipts), PAB: dec(t, n.PAB),
		})
	}
	_, p, err := domain.ProjectPlanned(days, domain.ProjectionInput{UnitCost: money.Amount{Currency: "XXX"}, HorizonDays: 30, Bucket: domain.BucketDay})
	if err != nil {
		t.Fatal(err)
	}
	last := res.Net[len(res.Net)-1]
	if !p.ClosingOnHand.Equal(last.PAB) {
		t.Fatalf("projection closes at %s, the engine's last PAB is %s", p.ClosingOnHand, last.PAB)
	}
	if !p.TotalDemand.Equal(sumOf(res, func(n domain.NetRow) decimal.Decimal { return n.Gross })) {
		t.Fatalf("projected demand %s", p.TotalDemand)
	}

	// The window cuts later days but keeps the first stored day (day 1 here),
	// which carries the opening on-hand; another tenant reads nothing of this plan.
	short, err := st.ReadProjectionSource(ctx, "MAIN", []string{item}, 0)
	if err != nil || len(short.Rows[item]) != 1 || short.Rows[item][0] != rows[0] || rows[0].Date == src.AsOf {
		t.Fatalf("window 0 kept %+v of %d rows (first %+v), %v", short.Rows[item], len(rows), rows[0], err)
	}
	other := testkit.Ctx(testkit.TenantID(2), "planning.read")
	if _, err := st.ReadProjectionSource(other, "MAIN", []string{item}, 90); problemCode(err) != "PLAN-VERSION-NOT-FOUND" {
		t.Fatalf("tenant 2: %v", err)
	}

	// A new run supersedes the view.
	if _, _, err := st.RunPlan(ctx, runRequest(oneItemPlan(t), "p-2", "in-p2")); err != nil {
		t.Fatal(err)
	}
	next, err := st.ReadProjectionSource(ctx, "MAIN", []string{item}, 90)
	if err != nil || next.Version != 2 || len(next.Rows[item]) != 0 {
		t.Fatalf("after the second run: %+v, %v", next, err)
	}
}

func sumOf(res domain.Result, f func(domain.NetRow) decimal.Decimal) decimal.Decimal {
	var s decimal.Decimal
	for _, n := range res.Net {
		s = s.Add(f(n))
	}
	return s
}

func dayOffset(t *testing.T, date string) int {
	t.Helper()
	for d := 0; d < 400; d++ {
		if dayOf(d) == date {
			return d
		}
	}
	t.Fatalf("date %s is more than 400 days out", date)
	return 0
}
