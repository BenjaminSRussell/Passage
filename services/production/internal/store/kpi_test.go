package store

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/migrations"
)

// endedAt runs an order to End (reporting `good` finished and `bad` in error on
// its route card) and dates its Started, ReportedAsFinished and Ended log entries.
func endedAt(t *testing.T, pool *pgxpool.Pool, s *Postgres, ctx context.Context, in NewOrder, good, bad, started, finished, ended string) string {
	t.Helper()
	st := startedOrder(t, s, ctx, in)
	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good, tr.Error = d(good), d(bad) })
	st.must(domain.CmdEnd, nil)
	for to, at := range map[string]string{"Started": started, "ReportedAsFinished": finished, "Ended": ended} {
		if _, err := pool.Exec(context.Background(), `update production_status_log set occurred_at = $1::timestamptz where order_id = $2 and to_status = $3`, at, st.orderID, to); err != nil {
			t.Fatal(err)
		}
	}
	return st.orderID
}

// The order KPIs (4.21.14): ended orders in the period, finished on time against
// the delivery date, mean cycle days from Started to the report as finished,
// scrap share, and the open and overdue orders as of a date; grouping by site and
// product, filters, legal-entity scope and tenant isolation.
func TestKPIs(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(78))
	site1, site2 := ids.New(), ids.New()
	order := func(key, site, due string) NewOrder {
		in := sampleOrder(t, key, ids.New())
		in.SiteID = site
		in.DeliveryDate = &due
		return in
	}

	// 3.5 days, finished the day before its delivery date and ended (cost close)
	// two weeks after it: on time all the same; 100 finished.
	inA := order("kpi-a", site1, "2026-10-06")
	endedAt(t, pool, s, ctx, inA, "100", "0", "2026-10-01T12:00:00Z", "2026-10-05T00:00:00Z", "2026-10-20T00:00:00Z")
	// 2 days, finished two days after its delivery date; 98 finished and 2 in error.
	endedAt(t, pool, s, ctx, order("kpi-b", site1, "2026-10-10"), "98", "2", "2026-10-10T00:00:00Z", "2026-10-12T00:00:00Z", "2026-10-12T00:00:00Z")
	// Ended in September: outside the period.
	endedAt(t, pool, s, ctx, order("kpi-c", site2, "2026-09-30"), "100", "0", "2026-09-01T00:00:00Z", "2026-09-30T23:00:00Z", "2026-09-30T23:00:00Z")
	// Open: Created and overdue on 20 October, and one not yet due.
	if _, _, err := s.CreateOrder(ctx, order("kpi-d", site2, "2026-10-01")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateOrder(ctx, order("kpi-e", site1, "2026-12-01")); err != nil {
		t.Fatal(err)
	}

	rep, err := s.KPIs(ctx, KPIFilter{From: "2026-10-01", To: "2026-10-31", AsOf: "2026-10-20"})
	if err != nil {
		t.Fatal(err)
	}
	tot := rep.Totals
	if len(rep.Groups) != 1 || rep.Groups[0].Key != "" || tot.Ended != 2 || tot.Dated != 2 || tot.OnTime != 1 || tot.Cycled != 2 ||
		!tot.CycleDaysSum.Equal(d("5.5")) || !tot.QtyFinished.Equal(d("198")) || !tot.QtyError.Equal(d("2")) || tot.Open != 2 || tot.Overdue != 1 {
		t.Fatalf("totals = %+v groups %+v", tot, rep.Groups)
	}
	if p := tot.OnTimePct(); p == nil || !p.Equal(d("50")) {
		t.Fatalf("on time = %v", p)
	}
	if p := tot.AvgCycleDays(); p == nil || !p.Equal(d("2.75")) {
		t.Fatalf("cycle = %v", p)
	}
	if p := tot.ScrapPct(); p == nil || !p.Equal(d("1")) {
		t.Fatalf("scrap = %v", p)
	}

	bySite, err := s.KPIs(ctx, KPIFilter{GroupBy: KPIGroupSite, AsOf: "2026-10-20"})
	if err != nil || len(bySite.Groups) != 2 {
		t.Fatalf("by site: %+v %v", bySite, err)
	}
	// Without a period every ended order counts: site 1 has two, site 2 one; site 1 ranks first.
	g1, g2 := bySite.Groups[0], bySite.Groups[1]
	if g1.Key != site1 || g1.Ended != 2 || g1.Open != 1 || g1.Overdue != 0 || g2.Key != site2 || g2.Ended != 1 || g2.Open != 1 || g2.Overdue != 1 || bySite.Totals.Ended != 3 {
		t.Fatalf("groups = %+v", bySite.Groups)
	}

	one, err := s.KPIs(ctx, KPIFilter{SiteID: site2, AsOf: "2026-10-20"})
	if err != nil || one.Totals.Ended != 1 || one.Totals.Open != 1 || one.Totals.OnTime != 1 {
		t.Fatalf("site filter: %+v %v", one.Totals, err)
	}
	none, err := s.KPIs(ctx, KPIFilter{From: "2026-11-01"})
	if err != nil || none.Totals.Ended != 0 || none.Totals.OnTimePct() != nil {
		t.Fatalf("later period: %+v %v", none.Totals, err)
	}
	// Finished in October but ended (closed) on 1 November: in November's period, not October's.
	late := endedAt(t, pool, s, ctx, order("kpi-f", site2, "2026-10-30"), "50", "0", "2026-10-28T00:00:00Z", "2026-10-29T00:00:00Z", "2026-11-01T00:00:00Z")
	nov, err := s.KPIs(ctx, KPIFilter{From: "2026-11-01", To: "2026-11-30", AsOf: "2026-10-20"})
	if err != nil || nov.Totals.Ended != 1 || nov.Totals.OnTime != 1 {
		t.Fatalf("November: %+v %v", nov.Totals, err)
	}
	if oct, err := s.KPIs(ctx, KPIFilter{From: "2026-10-01", To: "2026-10-31", AsOf: "2026-10-20"}); err != nil || oct.Totals.Ended != 2 {
		t.Fatalf("October after a November close: %+v %v", oct.Totals, err)
	}

	byProduct, err := s.KPIs(ctx, KPIFilter{GroupBy: KPIGroupProduct, From: "2026-11-01", AsOf: "2026-10-20"})
	if err != nil {
		t.Fatal(err)
	}
	// One group per product: the ended order's, then one per open order (no ended orders, keyed by id).
	var lateProduct string
	if err := pool.QueryRow(context.Background(), `select product_id::text from production_order where id = $1`, late).Scan(&lateProduct); err != nil {
		t.Fatal(err)
	}
	if len(byProduct.Groups) != 3 || byProduct.Groups[0].Key != lateProduct || byProduct.Groups[0].Ended != 1 || byProduct.Totals.Open != 2 {
		t.Fatalf("by product: %+v", byProduct.Groups)
	}
	onlyA, err := s.KPIs(ctx, KPIFilter{ProductID: inA.ProductID})
	if err != nil || onlyA.Totals.Ended != 1 || onlyA.Totals.Open != 0 {
		t.Fatalf("product filter: %+v %v", onlyA.Totals, err)
	}

	inLE, err := s.KPIs(ctx, KPIFilter{LegalEntityID: inA.LegalEntityID})
	if err != nil || inLE.Totals.Ended != 1 || inLE.Totals.Open != 0 || inLE.Totals.Cycled != 1 {
		t.Fatalf("legal-entity filter: %+v %v", inLE.Totals, err)
	}

	foreign, err := s.KPIs(tenantCtx(testkit.TenantID(79)), KPIFilter{})
	if err != nil || len(foreign.Groups) != 0 || foreign.Totals.Ended != 0 || foreign.Totals.Open != 0 {
		t.Fatalf("another tenant: %+v %v", foreign, err)
	}
}

// Under the application role, row-level security keeps a principal scoped to
// one legal entity to its own orders, ended and open, and another tenant sees none.
func TestKPIs_RowLevelSecurity(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s and %s are needed for the RLS test", testkit.EnvURL, testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(app)
	tenantA := testkit.TenantID(81)
	leA, leB := ids.New(), ids.New()
	ctxA := tenantCtx(tenantA)
	for key, le := range map[string]string{"kpi-rls-a": leA, "kpi-rls-b": leB} {
		endedAt(t, admin, s, ctxA, sampleOrder(t, key, le), "100", "0", "2026-10-01T00:00:00Z", "2026-10-03T00:00:00Z", "2026-10-03T00:00:00Z")
		if _, _, err := s.CreateOrder(ctxA, sampleOrder(t, key+"-open", le)); err != nil {
			t.Fatal(err)
		}
	}
	if all, err := s.KPIs(ctxA, KPIFilter{}); err != nil || all.Totals.Ended != 2 || all.Totals.Open != 2 {
		t.Fatalf("tenant-wide principal: %+v %v", all.Totals, err)
	}
	scoped := scopedCtx(tenantA, leA)
	in, err := s.KPIs(scoped, KPIFilter{GroupBy: KPIGroupSite})
	if err != nil || in.Totals.Ended != 1 || in.Totals.Open != 1 || len(in.Groups) != 2 {
		t.Fatalf("under a legal-entity scope: %+v %v", in, err)
	}
	// An explicit filter on another legal entity does not widen the scope.
	if leak, err := s.KPIs(scoped, KPIFilter{LegalEntityID: leB}); err != nil || leak.Totals.Ended != 0 || leak.Totals.Open != 0 || len(leak.Groups) != 0 {
		t.Fatalf("another legal entity: %+v %v", leak, err)
	}
	if leak, err := s.KPIs(tenantCtx(testkit.TenantID(82)), KPIFilter{}); err != nil || leak.Totals.Ended != 0 || leak.Totals.Open != 0 {
		t.Fatalf("another tenant: %+v %v", leak, err)
	}
}
