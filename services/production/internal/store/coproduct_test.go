package store

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

// coProductOrder is the sample order (planned cost 5000 for 100 units) that
// also yields 0.2 of a co-product per unit: 20 more output units, so a cost
// base of 120 and 41.666667 per unit.
func coProductOrder(t *testing.T, key string) NewOrder {
	in := sampleOrder(t, key, ids.New())
	cos, err := domain.PlanCoProducts([]domain.CoProduct{{LineNo: "C1", ItemID: ids.New(), QtyPer: d("0.2")}}, in.Quantity)
	if err != nil {
		t.Fatal(err)
	}
	in.CoProducts = cos
	return in
}

// FR-21.14: co-product receipts take their share of planned cost by quantity,
// the order's RAF and the co-product's reversal, End leaves no WIP.
func TestCoProducts_ReceiptsAllocateByQuantityAndSettle(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(91))
	st := startedOrder(t, s, ctx, coProductOrder(t, "key-co-1"))

	o := st.refresh()
	if len(o.CoProducts) != 1 || !o.CoProducts[0].QtyPlanned.Equal(d("20")) {
		t.Fatalf("declared co-products: %+v", o.CoProducts)
	}
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList}).ID)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalRouteCard}).ID)

	// The command reports the co-product in proportion: 60 of 100 → 12 of 20.
	st.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("60") })
	o = st.refresh()
	if !o.CoProducts[0].QtyReported.Equal(d("12")) {
		t.Fatalf("proportional co-product: %+v", o.CoProducts)
	}
	if c := st.costs(); !c.WIPBalance.Equal(d("2000")) {
		t.Fatalf("WIP after RAF 60 + co 12: %s, want 5000 − 2500 − 500", c.WIPBalance)
	}

	// A reset reverses the receipts and the co-product's reported quantity.
	st.must(domain.CmdResetStatus, nil)
	if o = st.refresh(); !o.CoProducts[0].QtyReported.IsZero() || !st.costs().WIPBalance.Equal(d("5000")) {
		t.Fatalf("after reset: %+v wip %s", o.CoProducts, st.costs().WIPBalance)
	}

	// A manual RAF with an explicit co-product line.
	raf := st.mustJournal(NewJournal{Type: domain.JournalReportFinished, QtyGood: d("100"),
		CoProducts: []CoProductInput{{LineNo: "C1", Qty: d("20")}}})
	var co JournalLine
	for _, l := range raf.Lines {
		if l.Kind == KindCoProduct {
			co = l
		}
	}
	if co.CoProductLineNo == nil || *co.CoProductLineNo != "C1" || !co.Qty.Equal(d("20")) {
		t.Fatalf("draft lines: %+v", raf.Lines)
	}
	posted := st.mustPost(raf.ID)
	amounts := map[string]string{}
	for _, l := range posted.Lines {
		if l.Kind == KindReceipt || l.Kind == KindCoProduct {
			amounts[l.Kind] = l.Amount.String()
		}
	}
	if amounts[KindReceipt] != "4166.67" || amounts[KindCoProduct] != "833.33" {
		t.Fatalf("receipt values by quantity: %v", amounts)
	}
	if !domain.Balanced(posted.Postings) {
		t.Fatalf("postings: %+v", posted.Postings)
	}
	st.must(domain.CmdEnd, nil)
	c := st.costs()
	total := decimal.Zero
	for _, v := range c.Variances {
		total = total.Add(v.Amount)
	}
	if !c.WIPBalance.IsZero() || !total.IsZero() {
		t.Fatalf("End: WIP %s variances %s %+v", c.WIPBalance, total, c.Variances)
	}
	if net := ledgerNet(t, pool, st.orderID); !net.IsZero() {
		t.Fatalf("subledger net %s", net)
	}
}

func TestCoProducts_Rules(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(92))
	st := startedOrder(t, s, ctx, coProductOrder(t, "key-co-2"))
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList}).ID)

	_, err := st.journal(NewJournal{Type: domain.JournalReportFinished, QtyGood: d("10"), CoProducts: []CoProductInput{{LineNo: "X9", Qty: d("1")}}})
	expectProblem(t, err, 422, CodeInvalidJournalLine)
	_, err = st.journal(NewJournal{Type: domain.JournalPickingList, CoProducts: []CoProductInput{{LineNo: "C1", Qty: d("1")}}})
	expectProblem(t, err, 422, CodeInvalidJournalLine)

	over := st.mustJournal(NewJournal{Type: domain.JournalReportFinished, QtyGood: d("10"), CoProducts: []CoProductInput{{LineNo: "C1", Qty: d("21")}}})
	_, err = st.post(over.ID, nil)
	expectProblem(t, err, 422, domain.CodeCoProductExceeds)

	// An order with co-products cannot be split.
	o, _, err := s.CreateOrder(ctx, coProductOrder(t, "key-co-3"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SplitOrder(ctx, Split{OrderID: o.ID, IfMatch: o.Version, SplitQty: d("40"), Actor: "planner", IdempotencyKey: "split-co", RequestHash: "h"})
	expectProblem(t, err, 409, CodeCoProductUnsupported)
}

// BR-9.12: a by-product carries no cost. It is received at zero, the product and
// the co-product share the planned cost by quantity alone, and End clears WIP.
func TestCoProducts_ByProductReceivedAtZero(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(93))
	in := sampleOrder(t, "key-by-1", ids.New())
	cos, err := domain.PlanCoProducts([]domain.CoProduct{
		{LineNo: "B1", Type: domain.CoProductTypeBy, ItemID: ids.New(), QtyPer: d("0.5")},
		{LineNo: "C1", ItemID: ids.New(), QtyPer: d("0.2")},
	}, in.Quantity)
	if err != nil {
		t.Fatal(err)
	}
	in.CoProducts = cos
	st := startedOrder(t, s, ctx, in)
	if o := st.refresh(); o.CoProducts[0].Type != domain.CoProductTypeBy || o.CoProducts[1].Type != domain.CoProductTypeCo {
		t.Fatalf("stored types: %+v", o.CoProducts)
	}
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList}).ID)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalRouteCard}).ID)

	posted := st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalReportFinished, QtyGood: d("100"),
		CoProducts: []CoProductInput{{LineNo: "B1", Qty: d("50")}, {LineNo: "C1", Qty: d("20")}}}).ID)
	amounts := map[string]string{}
	for _, l := range posted.Lines {
		switch {
		case l.Kind == KindReceipt:
			amounts["product"] = l.Amount.String()
		case l.Kind == KindCoProduct:
			amounts[*l.CoProductLineNo] = l.Amount.String()
		}
	}
	if amounts["product"] != "4166.67" || amounts["C1"] != "833.33" || amounts["B1"] != "0" {
		t.Fatalf("receipt values: %v", amounts)
	}
	if o := st.refresh(); !o.CoProducts[0].QtyReported.Equal(d("50")) {
		t.Fatalf("by-product reported: %+v", o.CoProducts)
	}
	st.must(domain.CmdEnd, nil)
	c := st.costs()
	total := decimal.Zero
	for _, v := range c.Variances {
		total = total.Add(v.Amount)
	}
	if !c.WIPBalance.IsZero() || !total.IsZero() {
		t.Fatalf("End: WIP %s variances %s %+v", c.WIPBalance, total, c.Variances)
	}
	if net := ledgerNet(t, pool, st.orderID); !net.IsZero() {
		t.Fatalf("subledger net %s", net)
	}
}

// The tolerance counts what earlier journals received; a quantity PATCH rescales
// the planned co-products; a merge is refused like a split.
func TestCoProducts_CumulativeLimitReplanAndMerge(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(94))
	st := startedOrder(t, s, ctx, coProductOrder(t, "key-co-4"))
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList}).ID)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalReportFinished, QtyGood: d("50"), CoProducts: []CoProductInput{{LineNo: "C1", Qty: d("15")}}}).ID)
	second := st.mustJournal(NewJournal{Type: domain.JournalReportFinished, QtyGood: d("10"), CoProducts: []CoProductInput{{LineNo: "C1", Qty: d("6")}}})
	_, err := st.post(second.ID, nil)
	expectProblem(t, err, 422, domain.CodeCoProductExceeds)

	in := coProductOrder(t, "key-co-5")
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	forty := d("40")
	u, err := s.UpdateOrder(ctx, OrderUpdate{OrderID: o.ID, IfMatch: o.Version, Quantity: &forty, Actor: "planner"})
	if err != nil {
		t.Fatal(err)
	}
	if !u.CoProducts[0].QtyPlanned.Equal(d("8")) {
		t.Fatalf("replanned co-product: %+v", u.CoProducts)
	}

	plain := sameShape(in, "key-co-6")
	plain.CoProducts = nil
	other, _, err := s.CreateOrder(ctx, plain)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.MergeOrders(ctx, Merge{Orders: []MergeRef{{other.ID, other.Version}, {u.ID, u.Version}}, Actor: "planner",
		IdempotencyKey: "merge-co", RequestHash: "h"})
	expectProblem(t, err, 409, CodeCoProductUnsupported)
}
