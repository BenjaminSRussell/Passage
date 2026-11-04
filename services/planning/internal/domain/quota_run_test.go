package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func dq(s string) decimal.Decimal { return decimal.RequireFromString(s) }

var quotaDay0 = time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)

func quotaSpec(split bool, sources ...QuotaSourceSpec) RunQuota {
	for i := range sources {
		if sources[i].ID == "" {
			sources[i].ID = string(rune('a' + i))
		}
	}
	return RunQuota{ArrangementID: "arr", Spec: QuotaArrangementSpec{ItemID: "item", SiteID: "site",
		ValidFrom: quotaDay0, ValidTo: quotaDay0.AddDate(0, 0, 90), Split: split, MinSplitQty: decimal.Zero, Sources: sources}}
}

func vendorSrc(id, vendor, quota, allocated string) QuotaSourceSpec {
	return QuotaSourceSpec{ID: id, Type: SourceVendor, VendorID: vendor, Quota: dq(quota), Allocated: dq(allocated), BaseQty: decimal.Zero}
}

func po(number string, delivery int, qty string) PlannedOrder {
	return PlannedOrder{Number: number, ItemID: "item", OrderType: OrderPurchase, Qty: dq(qty), DeliveryDate: delivery, OrderDate: delivery - 5}
}

// AC-22.13 with the BR-22.12 arrangement: A quota 60 allocated 300 (rating
// 5.00), B quota 40 allocated 100 (rating 2.50); a requirement of 100 goes to
// vendor B, and a second one of 100 then goes to A (B is at 5.00, A wins the
// tie on its id).
func TestApplyQuota_AC2213LowestRatingGetsTheOrder(t *testing.T) {
	q := quotaSpec(false, vendorSrc("A", "va", "60", "300"), vendorSrc("B", "vb", "40", "100"))
	res, out, err := ApplyQuota(quotaDay0, Result{Orders: []PlannedOrder{po("PLN-1", 10, "100"), po("PLN-2", 11, "100")}}, []RunQuota{q})
	if err != nil {
		t.Fatal(err)
	}
	o := res.Orders[0]
	if o.QuotaSourceID != "B" || o.VendorID != "vb" || o.QuotaArrangementID != "arr" || out.Allocated != 2 {
		t.Fatalf("order %+v outcome %+v", o, out)
	}
	if res.Orders[1].QuotaSourceID != "A" {
		t.Fatalf("second order went to %q; B is at rating 5.00 after the first", res.Orders[1].QuotaSourceID)
	}
}

func TestApplyQuota_RatingsMoveAcrossOrdersOfTheRun(t *testing.T) {
	q := quotaSpec(false, vendorSrc("A", "va", "50", "0"), vendorSrc("B", "vb", "50", "0"))
	res, _, err := ApplyQuota(quotaDay0, Result{Orders: []PlannedOrder{po("PLN-2", 20, "100"), po("PLN-1", 10, "100"), po("PLN-3", 30, "10")}}, []RunQuota{q})
	if err != nil {
		t.Fatal(err)
	}
	// Delivery order: PLN-1 -> A (tie, id order), PLN-2 -> B, PLN-3 -> A (tie again).
	got := map[string]string{}
	for _, o := range res.Orders {
		got[o.Number] = o.QuotaSourceID
	}
	if got["PLN-1"] != "A" || got["PLN-2"] != "B" || got["PLN-3"] != "A" {
		t.Fatalf("got %v", got)
	}
	if res.Orders[0].Number != "PLN-2" {
		t.Fatalf("order of the result changed: %v", res.Orders[0].Number)
	}
}

func TestApplyQuota_SplitDividesOrderAndPegging(t *testing.T) {
	q := quotaSpec(true, vendorSrc("A", "va", "60", "0"), vendorSrc("B", "vb", "40", "0"))
	in := Result{
		Orders: []PlannedOrder{po("PLN-1", 10, "100")},
		Pegging: []Peg{
			{ItemID: "item", SupplyRef: "PLN-1", DemandRef: "so1", Qty: dq("70")},
			{ItemID: "item", SupplyRef: "PLN-1", DemandRef: "so2", Qty: dq("30")},
			{ItemID: "item", SupplyRef: "R-9", DemandRef: "so3", Qty: dq("5")},
		},
	}
	res, out, err := ApplyQuota(quotaDay0, in, []RunQuota{q})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Orders) != 2 || out.Split != 1 || res.Orders[0].Number != "PLN-1-1" || !res.Orders[0].Qty.Equal(dq("60")) || !res.Orders[1].Qty.Equal(dq("40")) {
		t.Fatalf("orders %+v outcome %+v", res.Orders, out)
	}
	pegged := map[string]decimal.Decimal{}
	for _, p := range res.Pegging {
		pegged[p.SupplyRef] = pegged[p.SupplyRef].Add(p.Qty)
	}
	if !pegged["PLN-1-1"].Equal(dq("60")) || !pegged["PLN-1-2"].Equal(dq("40")) || !pegged["R-9"].Equal(dq("5")) || len(pegged) != 3 {
		t.Fatalf("pegging %v", pegged)
	}
}

func TestApplyQuota_PeriodMaximumBlocksTheSource(t *testing.T) {
	a := vendorSrc("A", "va", "50", "0")
	max, days := dq("100"), 30
	a.MaxQtyPerPeriod, a.PeriodDays = &max, &days
	q := quotaSpec(false, a, vendorSrc("B", "vb", "50", "500"))
	// A has the lowest rating but takes only 100 per 30 days.
	in := Result{Orders: []PlannedOrder{po("PLN-1", 5, "80"), po("PLN-2", 10, "80"), po("PLN-3", 40, "80")}}
	res, _, err := ApplyQuota(quotaDay0, in, []RunQuota{q})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{res.Orders[0].QuotaSourceID, res.Orders[1].QuotaSourceID, res.Orders[2].QuotaSourceID}
	if got[0] != "A" || got[1] != "B" || got[2] != "A" {
		t.Fatalf("got %v (second period of A starts on day 30)", got)
	}
}

func TestApplyQuota_SourceTypeMustMatchOrderType(t *testing.T) {
	q := quotaSpec(false, vendorSrc("A", "va", "50", "0"))
	o := po("PLN-1", 5, "10")
	o.OrderType = OrderProduction
	res, out, err := ApplyQuota(quotaDay0, Result{Orders: []PlannedOrder{o}}, []RunQuota{q})
	if err != nil {
		t.Fatal(err)
	}
	if res.Orders[0].QuotaSourceID != "" || out.Unassigned != 1 {
		t.Fatalf("%+v %+v", res.Orders[0], out)
	}
}

func TestApplyQuota_ValidityAndOtherItems(t *testing.T) {
	q := quotaSpec(false, vendorSrc("A", "va", "50", "0"))
	late := po("PLN-1", 200, "10")
	other := po("PLN-2", 5, "10")
	other.ItemID = "other"
	res, out, err := ApplyQuota(quotaDay0, Result{Orders: []PlannedOrder{late, other}}, []RunQuota{q})
	if err != nil {
		t.Fatal(err)
	}
	if res.Orders[0].QuotaSourceID != "" || res.Orders[1].QuotaSourceID != "" || out.Allocated != 0 {
		t.Fatalf("%+v", res.Orders)
	}
}

func TestApplyQuota_SplitSumsToQuantity(t *testing.T) {
	q := quotaSpec(true, vendorSrc("A", "va", "3", "10"), vendorSrc("B", "vb", "7", "0"), vendorSrc("C", "vc", "1", "2"))
	res, _, err := ApplyQuota(quotaDay0, Result{Orders: []PlannedOrder{po("PLN-1", 3, "101.333333")}}, []RunQuota{q})
	if err != nil {
		t.Fatal(err)
	}
	sum := decimal.Zero
	for _, o := range res.Orders {
		sum = sum.Add(o.Qty)
	}
	if !sum.Equal(dq("101.333333")) {
		t.Fatalf("sum %s", sum)
	}
}

func capped(src QuotaSourceSpec, max string, days int) QuotaSourceSpec {
	m := dq(max)
	src.MaxQtyPerPeriod, src.PeriodDays = &m, &days
	return src
}

// A split share above a source's remainder in the period is cut to the
// remainder and the rest goes to the other sources, instead of dropping the
// capped source altogether.
func TestApplyQuota_SplitCutsAShareToThePeriodRemainder(t *testing.T) {
	q := quotaSpec(true, capped(vendorSrc("A", "va", "50", "0"), "10", 30), vendorSrc("B", "vb", "50", "0"))
	res, _, err := ApplyQuota(quotaDay0, Result{Orders: []PlannedOrder{po("PLN-1", 5, "100")}}, []RunQuota{q})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Orders) != 2 || res.Orders[0].QuotaSourceID != "A" || !res.Orders[0].Qty.Equal(dq("10")) ||
		res.Orders[1].QuotaSourceID != "B" || !res.Orders[1].Qty.Equal(dq("90")) {
		t.Fatalf("orders %+v", res.Orders)
	}
}

// When every source is at its period maximum, the order keeps no source and is
// reported (PLAN-0009) rather than given to a source beyond its maximum: one
// dismiss-only no-quota-source message per order, kept in message order, and a
// plan hash that differs from the run without it.
func TestApplyQuota_NoSourceLeftReportsTheOrder(t *testing.T) {
	q := quotaSpec(true, capped(vendorSrc("A", "va", "50", "0"), "10", 30), capped(vendorSrc("B", "vb", "50", "0"), "20", 30))
	delay := Message{Action: ActionDelay, ItemID: "item", DemandRef: "SO-1", SuggestedDate: 9, DelayDays: 2}
	in := Result{Orders: []PlannedOrder{po("PLN-1", 5, "100")}, Messages: []Message{delay}, Hash: "engine"}
	res, out, err := ApplyQuota(quotaDay0, in, []RunQuota{q})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Orders) != 1 || res.Orders[0].QuotaSourceID != "" || out.Unassigned != 1 || len(out.UnassignedOrders) != 1 || out.UnassignedOrders[0] != "PLN-1" {
		t.Fatalf("orders %+v outcome %+v", res.Orders, out)
	}
	want := Message{Action: ActionNoQuotaSource, ItemID: "item", SupplyRef: "PLN-1", DemandRef: "arr", SuggestedDate: 5, Qty: dq("100")}
	if len(res.Messages) != 2 || res.Messages[0] != delay || res.Messages[1].SupplyRef != want.SupplyRef || res.Messages[1].DemandRef != "arr" ||
		res.Messages[1].Action != want.Action || res.Messages[1].SuggestedDate != 5 || !res.Messages[1].Qty.Equal(want.Qty) {
		t.Fatalf("messages %+v; want the delay then %+v", res.Messages, want)
	}
	if len(in.Messages) != 1 {
		t.Fatal("ApplyQuota must not change the caller's messages")
	}
	if res.Hash == "engine" || res.Hash == "" {
		t.Fatalf("hash %q; want it recomputed over the new message", res.Hash)
	}
	if CatalogCode(ActionNoQuotaSource) != "PLAN-0009" || MessageActionable(ActionNoQuotaSource) {
		t.Fatal("no-quota-source is PLAN-0009 and can only be dismissed")
	}
	if text := CatalogText(MessageView{Action: ActionNoQuotaSource, DemandRef: "arr", Date: "2026-03-07"}); text != "Quota arrangement arr has no usable source in 2026-03-07." {
		t.Fatalf("text %q", text)
	}
}

// Firmed and approved orders already booked on a source count against its
// maximum in their period, so a rerun does not get a fresh period budget.
func TestApplyQuota_BookedOrdersCountAgainstThePeriodMaximum(t *testing.T) {
	q := quotaSpec(false, capped(vendorSrc("A", "va", "50", "0"), "100", 30), vendorSrc("B", "vb", "50", "500"))
	q.Booked = []QuotaBooking{
		{SourceID: "A", Day: quotaDay0.AddDate(0, 0, 2), Qty: dq("90")},  // period 0
		{SourceID: "A", Day: quotaDay0.AddDate(0, 0, -5), Qty: dq("90")}, // before validity: ignored
	}
	res, _, err := ApplyQuota(quotaDay0, Result{Orders: []PlannedOrder{po("PLN-1", 5, "20"), po("PLN-2", 35, "20")}}, []RunQuota{q})
	if err != nil {
		t.Fatal(err)
	}
	if res.Orders[0].QuotaSourceID != "B" || res.Orders[1].QuotaSourceID != "A" {
		t.Fatalf("got %q, %q; A has 10 left in period 0 and 100 in period 1", res.Orders[0].QuotaSourceID, res.Orders[1].QuotaSourceID)
	}
}

// The plan hash changes with the sources orders went to, so the version diff
// does not report two runs with other allocations as equal plans; with no
// allocation it is the engine's hash.
func TestApplyQuota_HashCoversTheAllocation(t *testing.T) {
	in := Result{Orders: []PlannedOrder{po("PLN-1", 5, "20")}}
	in.Hash = hashResult(in)
	toA, _, _ := ApplyQuota(quotaDay0, in, []RunQuota{quotaSpec(false, vendorSrc("A", "va", "50", "0"), vendorSrc("B", "vb", "50", "10"))})
	toB, _, _ := ApplyQuota(quotaDay0, in, []RunQuota{quotaSpec(false, vendorSrc("A", "va", "50", "10"), vendorSrc("B", "vb", "50", "0"))})
	if toA.Hash == toB.Hash || toA.Hash == in.Hash {
		t.Fatalf("hashes A %s B %s engine %s", toA.Hash, toB.Hash, in.Hash)
	}
	other := po("PLN-1", 5, "20")
	other.ItemID = "other"
	none, _, _ := ApplyQuota(quotaDay0, Result{Orders: []PlannedOrder{other}, Hash: "engine"}, []RunQuota{quotaSpec(false, vendorSrc("A", "va", "50", "0"))})
	if none.Hash != "engine" {
		t.Fatalf("hash without allocation = %s", none.Hash)
	}
}

// A split production order divides the bom-line demand it drives: the
// component's peg to "<order>/<component>" is shared by the parts in
// proportion, naming each part's bom line.
func TestApplyQuota_SplitProductionDividesComponentPegs(t *testing.T) {
	prod := func(id string) QuotaSourceSpec {
		return QuotaSourceSpec{ID: id, Type: SourceProduction, ProductionVersionID: "pv-" + id, Quota: dq("50")}
	}
	parent := PlannedOrder{Number: "PLN-000001", ItemID: "item", OrderType: OrderProduction, Qty: dq("100"), DeliveryDate: 10, OrderDate: 5}
	comp := PlannedOrder{Number: "PLN-000002", ItemID: "comp", OrderType: OrderPurchase, Qty: dq("300"), DeliveryDate: 5, OrderDate: 1}
	in := Result{
		Orders: []PlannedOrder{parent, comp},
		Pegging: []Peg{
			{ItemID: "item", SupplyRef: "PLN-000001", DemandRef: "so1", DemandRefType: RefSales, Qty: dq("100")},
			{ItemID: "comp", SupplyRef: "PLN-000002", DemandRef: "PLN-000001/comp", DemandRefType: RefBOM, Qty: dq("300")},
		},
	}
	res, _, err := ApplyQuota(quotaDay0, in, []RunQuota{quotaSpec(true, prod("P1"), prod("P2"))})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]decimal.Decimal{}
	for _, p := range res.Pegging {
		if p.DemandRefType == RefBOM {
			got[p.DemandRef] = got[p.DemandRef].Add(p.Qty)
		}
	}
	if len(got) != 2 || !got["PLN-000001-1/comp"].Equal(dq("150")) || !got["PLN-000001-2/comp"].Equal(dq("150")) {
		t.Fatalf("component pegs %v", got)
	}
	if d := driverOf("PLN-000001-2/comp"); d != "PLN-000001-2" {
		t.Fatalf("driver of the part's bom line = %s", d)
	}
}
