package store_test

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// TestRunPlan_QuotaAllocationAndBooking covers FR-22.17 and AC-22.13 on the
// database: a run reads the item's arrangement, the planned order is stored
// with its source and the source's vendor, and firming books the quantity on
// the source (allocated_qty) and names the source in plannedorder.firmed.v1.
// Another tenant sees no arrangement.
func TestRunPlan_QuotaAllocationAndBooking(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read", "planning.setup.maintain")
	res := oneItemPlan(t) // a purchase of 40
	item := res.Orders[0].ItemID
	req := runRequest(res, "run-q", "in-q")
	asOf := req.AsOf

	vendorA, vendorB := ids.New(), ids.New()
	spec := domain.QuotaArrangementSpec{ItemID: item, SiteID: req.SiteID, ValidFrom: day(2026, 1, 1), ValidTo: day(2027, 12, 31), MinSplitQty: decimal.Zero,
		Sources: []domain.QuotaSourceSpec{
			{Type: domain.SourceVendor, VendorID: vendorA, Quota: decimal.NewFromInt(50), Allocated: decimal.NewFromInt(160)},
			{Type: domain.SourceVendor, VendorID: vendorB, Quota: decimal.NewFromInt(50), Allocated: decimal.NewFromInt(160)},
		}}
	arr, _, err := st.CreateQuotaArrangement(ctx, store.QuotaCreate{Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	// Vendor B has the lower id order only when ratings tie, so lower B's rating.
	createdB := arr.Spec.Sources[1].ID
	spec.Sources[1].Allocated = decimal.NewFromInt(100)
	if arr, err = st.UpdateQuotaArrangement(ctx, arr.ID, arr.Version, spec); err != nil {
		t.Fatal(err)
	}
	// The update keeps the id of a source naming the same vendor, so planned
	// orders allocated to it before still book on it.
	if arr.Spec.Sources[1].ID != createdB {
		t.Fatalf("source of vendor B changed id on update: %s, was %s", arr.Spec.Sources[1].ID, createdB)
	}

	quotas, err := st.RunQuotas(ctx, req.SiteID, "", []string{item}, asOf, asOf.AddDate(0, 0, 30))
	if err != nil || len(quotas) != 1 || len(quotas[0].Spec.Sources) != 2 {
		t.Fatalf("RunQuotas = %+v, %v", quotas, err)
	}
	other := testkit.Ctx(testkit.TenantID(2), "planning.run")
	if got, err := st.RunQuotas(other, req.SiteID, "", []string{item}, asOf, asOf.AddDate(0, 0, 30)); err != nil || len(got) != 0 {
		t.Fatalf("other tenant sees %+v, %v", got, err)
	}
	if got, err := st.RunQuotas(ctx, req.SiteID, "", []string{item}, day(2028, 1, 1), day(2028, 2, 1)); err != nil || len(got) != 0 {
		t.Fatalf("outside validity: %+v, %v", got, err)
	}

	applied, out, err := domain.ApplyQuota(asOf, req.Result, quotas)
	if err != nil || out.Allocated != 1 {
		t.Fatalf("apply: %+v, %v", out, err)
	}
	req.Result = applied
	if _, _, err := st.RunPlan(ctx, req); err != nil {
		t.Fatal(err)
	}
	orders, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 10)
	if err != nil || len(orders) != 1 {
		t.Fatalf("orders %+v, %v", orders, err)
	}
	o := orders[0]
	wantSource := arr.Spec.Sources[1].ID
	if o.QuotaSourceID == nil || *o.QuotaSourceID != wantSource || o.QuotaArrangementID == nil || *o.QuotaArrangementID != arr.ID ||
		o.VendorID == nil || *o.VendorID != vendorB {
		t.Fatalf("order %+v; want source %s vendor %s", o, wantSource, vendorB)
	}

	allocated := func() string {
		var s string
		if err := admin.QueryRow(context.Background(), `select allocated_qty::text from quota_source where id = $1`, wantSource).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if got := allocated(); got != "100.000000" {
		t.Fatalf("allocated before firming = %s", got)
	}
	batch, _, err := st.FirmPlannedOrders(ctx, store.FirmRequest{IdempotencyKey: "firm-q", InputHash: "h", Grouping: domain.DefaultGrouping, OrderIDs: []string{o.ID}})
	if err != nil || batch.Firmed != 1 || batch.Groups[0].VendorID == nil || *batch.Groups[0].VendorID != vendorB {
		t.Fatalf("firm: %+v, %v", batch, err)
	}
	if got := allocated(); got != "140.000000" { // 100 + the order's 40
		t.Fatalf("allocated after firming = %s", got)
	}
	// Booking bumps the arrangement's version: an update with the ETag read
	// before firming would write B's old allocated quantity back, so it is 412.
	if _, err := st.UpdateQuotaArrangement(ctx, arr.ID, arr.Version, spec); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("update with the ETag from before firming: %v", err)
	}
	if now, err := st.GetQuotaArrangement(ctx, arr.ID); err != nil || now.Version != arr.Version+1 || !now.Spec.Sources[1].Allocated.Equal(decimal.NewFromInt(140)) {
		t.Fatalf("arrangement after firming = %+v, %v", now, err)
	}
	// A replay of the firming books nothing more.
	if _, replayed, err := st.FirmPlannedOrders(ctx, store.FirmRequest{IdempotencyKey: "firm-q", InputHash: "h", Grouping: domain.DefaultGrouping, OrderIDs: []string{o.ID}}); err != nil || !replayed {
		t.Fatalf("replay: %v %v", replayed, err)
	}
	if got := allocated(); got != "140.000000" {
		t.Fatalf("allocated after replay = %s", got)
	}
	var tagged int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = 'dev.passage.planning.plannedorder.firmed.v1'
		and payload::text like '%' || $1 || '%'`, wantSource).Scan(&tagged); err != nil || tagged != 1 {
		t.Fatalf("firmed events naming the source = %d, %v", tagged, err)
	}
}

// TestRunPlan_QuotaAutoFirmBookingAndVendorChange covers the firming-fence path
// and planned-order maintenance with quota on the database: an auto-firmed
// order books on its source in the run, is read back as a booking against
// the source's per-period maximum, and a planner's vendor change moves an open
// order to that vendor's source or out of the arrangement; an approved order
// firmed by the next run books on its source too.
func TestRunPlan_QuotaAutoFirmBookingAndVendorChange(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read", "planning.setup.maintain", "planning.maintain")
	res := oneItemPlan(t) // a purchase of 40
	item := res.Orders[0].ItemID
	req := runRequest(res, "run-af", "in-af")
	max, period := decimal.NewFromInt(1000), 30
	vendorA, vendorB := ids.New(), ids.New()
	arr, _, err := st.CreateQuotaArrangement(ctx, store.QuotaCreate{Spec: domain.QuotaArrangementSpec{
		ItemID: item, SiteID: req.SiteID, ValidFrom: day(2026, 11, 1), ValidTo: day(2027, 10, 31), MinSplitQty: decimal.Zero,
		Sources: []domain.QuotaSourceSpec{
			{Type: domain.SourceVendor, VendorID: vendorA, Quota: decimal.NewFromInt(50), Allocated: decimal.NewFromInt(500)},
			{Type: domain.SourceVendor, VendorID: vendorB, Quota: decimal.NewFromInt(50), MaxQtyPerPeriod: &max, PeriodDays: &period},
		}}})
	if err != nil {
		t.Fatal(err)
	}
	srcA, srcB := arr.Spec.Sources[0].ID, arr.Spec.Sources[1].ID
	quotas, err := st.RunQuotas(ctx, req.SiteID, "", []string{item}, req.AsOf, req.AsOf.AddDate(0, 0, 30))
	if err != nil || len(quotas) != 1 || len(quotas[0].Booked) != 0 {
		t.Fatalf("RunQuotas before = %+v, %v", quotas, err)
	}
	firmed, _, err := domain.ApplyQuota(req.AsOf, req.Result, quotas)
	if err != nil || firmed.Orders[0].QuotaSourceID != srcB {
		t.Fatalf("apply: %+v, %v", firmed.Orders, err)
	}
	firmed.Orders[0].AutoFirmed = true
	req.Result = firmed
	if _, _, err := st.RunPlan(ctx, req); err != nil {
		t.Fatal(err)
	}
	var allocated string
	if err := admin.QueryRow(context.Background(), `select allocated_qty::text from quota_source where id = $1`, srcB).Scan(&allocated); err != nil || allocated != "40.000000" {
		t.Fatalf("allocated on B after the auto-firming run = %s, %v", allocated, err)
	}
	quotas, err = st.RunQuotas(ctx, req.SiteID, "", []string{item}, req.AsOf, req.AsOf.AddDate(0, 0, 30))
	if err != nil || len(quotas) != 1 || len(quotas[0].Booked) != 1 || quotas[0].Booked[0].SourceID != srcB ||
		!quotas[0].Booked[0].Qty.Equal(decimal.NewFromInt(40)) || !quotas[0].Booked[0].Day.Equal(req.AsOf.AddDate(0, 0, res.Orders[0].DeliveryDate)) {
		t.Fatalf("bookings after firming = %+v, %v", quotas, err)
	}

	// A second run leaves the order open; the planner then changes its vendor.
	open := runRequest(res, "run-open", "in-open")
	open.SiteID = req.SiteID
	open.Result, _, err = domain.ApplyQuota(open.AsOf, res, quotas)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RunPlan(ctx, open); err != nil {
		t.Fatal(err)
	}
	orders, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 10)
	if err != nil || len(orders) != 1 || orders[0].Status != "unprocessed" || orders[0].QuotaSourceID == nil {
		t.Fatalf("open orders %+v, %v", orders, err)
	}
	o := orders[0]
	toA, err := st.UpdatePlannedOrder(ctx, o.ID, o.Version, store.OrderPatch{VendorID: &vendorA})
	if err != nil || toA.QuotaSourceID == nil || *toA.QuotaSourceID != srcA || toA.QuotaArrangementID == nil || *toA.QuotaArrangementID != arr.ID {
		t.Fatalf("vendor A: %+v, %v", toA, err)
	}
	// Approved, it is firmed by the next run, which books it on A.
	if _, err := st.UpdatePlannedOrder(ctx, o.ID, toA.Version, store.OrderPatch{Approve: true}); err != nil {
		t.Fatal(err)
	}
	next := runRequest(res, "run-next", "in-next")
	next.SiteID = req.SiteID
	next.Result, _, err = domain.ApplyQuota(next.AsOf, res, quotas)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RunPlan(ctx, next); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(context.Background(), `select allocated_qty::text from quota_source where id = $1`, srcA).Scan(&allocated); err != nil || allocated != "540.000000" {
		t.Fatalf("allocated on A after the approved order was firmed = %s, %v", allocated, err)
	}
	orders, err = st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 10)
	if err != nil || len(orders) != 1 || orders[0].Status != "unprocessed" {
		t.Fatalf("orders of the third run %+v, %v", orders, err)
	}
	stranger := ids.New()
	away, err := st.UpdatePlannedOrder(ctx, orders[0].ID, orders[0].Version, store.OrderPatch{VendorID: &stranger})
	if err != nil || away.QuotaSourceID != nil || away.QuotaArrangementID != nil || away.VendorID == nil || *away.VendorID != stranger {
		t.Fatalf("vendor outside the arrangement: %+v, %v", away, err)
	}
}
