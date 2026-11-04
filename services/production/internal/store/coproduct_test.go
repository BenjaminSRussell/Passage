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

}

// FR-21.14 with FR-21.15: a split divides the declared outputs, the original
// keeping 60 units and 12 of the co-product, the new order 40 and 8.
func TestCoProducts_SplitDividesTheOutputs(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(95))
	o, _, err := s.CreateOrder(ctx, coProductOrder(t, "key-co-3"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.SplitOrder(ctx, Split{OrderID: o.ID, IfMatch: o.Version, SplitQty: d("40"), Actor: "planner", IdempotencyKey: "split-co", RequestHash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		o       Order
		planned string
	}{{res.Original, "12"}, {res.New, "8"}} {
		if len(c.o.CoProducts) != 1 || c.o.CoProducts[0].LineNo != "C1" || !c.o.CoProducts[0].QtyPlanned.Equal(d(c.planned)) || !c.o.CoProducts[0].QtyReported.IsZero() {
			t.Fatalf("co-products of %s after the split: %+v", c.o.OrderNumber, c.o.CoProducts)
		}
	}
	if res.New.CoProducts[0].ItemID != o.CoProducts[0].ItemID || !res.New.CoProducts[0].QtyPer.Equal(d("0.2")) {
		t.Fatalf("the new order's co-product: %+v", res.New.CoProducts)
	}
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
// the planned co-products; a merge needs the same declared outputs.
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
	expectProblem(t, err, 422, domain.CodeMergeRefused)

	// Orders that declare the same outputs merge, and the survivor's co-product scales.
	twin := sameShape(in, "key-co-7")
	twinOrder, _, err := s.CreateOrder(ctx, twin)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.MergeOrders(ctx, Merge{Orders: []MergeRef{{u.ID, u.Version}, {twinOrder.ID, twinOrder.Version}}, Actor: "planner",
		IdempotencyKey: "merge-co-2", RequestHash: "h2"})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Survivor.Quantity.Equal(d("140")) || !m.Survivor.CoProducts[0].QtyPlanned.Equal(d("28")) {
		t.Fatalf("merged survivor: %s %+v", m.Survivor.Quantity, m.Survivor.CoProducts)
	}
}

// FR-21.14 with FR-21.15 and BR-21.11 on a Started order: received co-products
// stay with the original (a reported order is not split until its report as
// finished is reset), then a 40-of-100 split moves 40 % of the WIP and plans 8 of the 20
// co-product units on the new order with nothing received. The WIP of the two
// orders adds up to what the original held, and each order's receipts, the
// co-product's share of planned cost included, clear its own WIP at End.
func TestCoProducts_StartedSplitKeepsReceiptsAndWIP(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(96))
	st := startedOrder(t, s, ctx, coProductOrder(t, "key-co-split-st"))
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList}).ID)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalRouteCard}).ID)

	// A report as finished of 60 receives 12 of the co-product and leaves the order
	// ReportedAsFinished, which no split takes: the receipts stay on the original.
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalReportFinished, QtyGood: d("60"), CoProductsProportional: true}).ID)
	o := st.refresh()
	_, err := s.SplitOrder(ctx, Split{OrderID: o.ID, IfMatch: o.Version, SplitQty: d("40"), Actor: "planner", IdempotencyKey: "split-co-st-0", RequestHash: "h0"})
	expectProblem(t, err, 409, domain.CodeIllegalTransition)
	if o = st.refresh(); !o.Quantity.Equal(d("100")) || !o.CoProducts[0].QtyPlanned.Equal(d("20")) || !o.CoProducts[0].QtyReported.Equal(d("12")) {
		t.Fatalf("a refused split changed the order: %s %+v", o.Quantity, o.CoProducts)
	}

	// The reset to Started reverses the report and the co-product receipt.
	st.must(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Started })
	o = st.refresh()
	before := st.costs().WIPBalance
	if o.Status != domain.Started || !o.CoProducts[0].QtyReported.IsZero() || !before.Equal(d("5000")) {
		t.Fatalf("after the reset: %s %+v WIP %s", o.Status, o.CoProducts, before)
	}
	res, err := s.SplitOrder(ctx, Split{OrderID: o.ID, IfMatch: o.Version, SplitQty: d("40"), Actor: "planner", IdempotencyKey: "split-co-st-1", RequestHash: "h1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		o            Order
		planned, wip string
	}{{res.Original, "12", "3000"}, {res.New, "8", "2000"}} {
		if len(c.o.CoProducts) != 1 || !c.o.CoProducts[0].QtyPlanned.Equal(d(c.planned)) || !c.o.CoProducts[0].QtyReported.IsZero() {
			t.Fatalf("co-products of %s: %+v", c.o.OrderNumber, c.o.CoProducts)
		}
		cs, err := s.Costs(ctx, c.o.ID)
		if err != nil || !cs.WIPBalance.Equal(d(c.wip)) || !ledgerNet(t, pool, c.o.ID).IsZero() {
			t.Fatalf("WIP of %s: %s (want %s), ledger net %s, %v", c.o.OrderNumber, cs.WIPBalance, c.wip, ledgerNet(t, pool, c.o.ID), err)
		}
	}
	oc, _ := s.Costs(ctx, res.Original.ID)
	nc, _ := s.Costs(ctx, res.New.ID)
	if !oc.WIPBalance.Add(nc.WIPBalance).Equal(before) {
		t.Fatalf("WIP before %s, after %s + %s", before, oc.WIPBalance, nc.WIPBalance)
	}

	// Each order reports its full quantity (co-products in proportion) and ends with no WIP and no variance.
	for _, ord := range []Order{res.Original, res.New} {
		cur, _ := s.GetOrder(ctx, ord.ID)
		e := &stepper{t: t, s: s, ctx: ctx, orderID: ord.ID, version: cur.Version, n: 20}
		e.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = cur.Quantity })
		if got := e.refresh(); !got.CoProducts[0].QtyReported.Equal(got.CoProducts[0].QtyPlanned) {
			t.Fatalf("co-product of %s after the full report: %+v", got.OrderNumber, got.CoProducts)
		}
		e.must(domain.CmdEnd, nil)
		c := e.costs()
		total := decimal.Zero
		for _, v := range c.Variances {
			total = total.Add(v.Amount)
		}
		if !c.WIPBalance.IsZero() || !total.IsZero() || !ledgerNet(t, pool, ord.ID).IsZero() {
			t.Fatalf("End of %s: WIP %s, variances %s %+v", ord.OrderNumber, c.WIPBalance, total, c.Variances)
		}
	}
}

// FR-21.14 with FR-21.18: co-products are added, changed and removed on the order, planned
// quantities follow, the edits are audited and published; a line a journal names keeps its item
// and type and cannot be removed.
func TestCoProducts_EditedOnTheOrder(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(93))
	in := coProductOrder(t, "key-co-edit")
	allProfile(t, s, ctx, in.LegalEntityID)
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}

	scrap := ids.New()
	edited, err := st.edit(domain.OrderEdits{CoProducts: []domain.CoProductEdit{
		{Op: domain.EditAdd, LineNo: "C2", Type: ptrTo(domain.CoProductTypeBy), ItemID: &scrap, QtyPer: qty("0.05")},
		{Op: domain.EditChange, LineNo: "C1", QtyPer: qty("0.3")},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(edited.CoProducts) != 2 || !edited.CoProducts[0].QtyPlanned.Equal(d("30")) || edited.CoProducts[1].Type != domain.CoProductTypeBy ||
		!edited.CoProducts[1].QtyPlanned.Equal(d("5")) || !edited.OrderBOMModified || edited.Version != o.Version+1 {
		t.Fatalf("after the edit: %+v", edited)
	}
	entries, err := s.OrderEdits(ctx, o.ID)
	if err != nil || len(entries) != 2 || entries[0].Target != domain.TargetCoProduct || string(entries[0].Before) != "null" || string(entries[1].Before) == "null" {
		t.Fatalf("audit trail: %v %+v", err, entries)
	}
	if evts := bomEditedEvents(t, pool, o.ID); len(evts) != 1 || len(evts[0].Edits) != 2 || evts[0].Edits[0].Target != "coproduct" {
		t.Fatalf("event: %+v", evts)
	}

	if _, err = st.edit(domain.OrderEdits{CoProducts: []domain.CoProductEdit{{Op: domain.EditRemove, LineNo: "C2"}}}, false); err != nil {
		t.Fatal(err)
	}
	if got := st.refresh(); len(got.CoProducts) != 1 || got.CoProducts[0].LineNo != "C1" {
		t.Fatalf("after the removal: %+v", got.CoProducts)
	}
	_, err = st.edit(domain.OrderEdits{CoProducts: []domain.CoProductEdit{{Op: domain.EditRemove, LineNo: "C2"}}}, false)
	expectProblem(t, err, 422, domain.CodeEditTargetMissing)
	// As at creation, a line never yields the order's own product.
	_, err = st.edit(domain.OrderEdits{CoProducts: []domain.CoProductEdit{{Op: domain.EditChange, LineNo: "C1", ItemID: &in.ProductID}}}, false)
	expectProblem(t, err, 422, domain.CodeInvalidInput)

	// A line a posted report names: the item and type stay, the quantity still follows.
	st.must(domain.CmdEstimate, nil)
	st.must(domain.CmdSchedule, nil)
	st.must(domain.CmdRelease, nil)
	st.must(domain.CmdStart, nil)
	_, err = st.edit(domain.OrderEdits{CoProducts: []domain.CoProductEdit{{Op: domain.EditChange, LineNo: "C1", QtyPer: qty("0.1")}}}, true)
	expectProblem(t, err, 409, domain.CodeEditNotAllowed)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList}).ID)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalRouteCard}).ID)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalReportFinished, QtyGood: d("100"),
		CoProducts: []CoProductInput{{LineNo: "C1", Qty: d("30")}}}).ID)
	st.must(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Released })
	if got := st.refresh(); !got.CoProducts[0].QtyReported.IsZero() {
		t.Fatalf("reported after the reset: %+v", got.CoProducts)
	}
	_, err = st.edit(domain.OrderEdits{CoProducts: []domain.CoProductEdit{{Op: domain.EditRemove, LineNo: "C1"}}}, true)
	expectProblem(t, err, 409, domain.CodeEditTargetInUse)
	_, err = st.edit(domain.OrderEdits{CoProducts: []domain.CoProductEdit{{Op: domain.EditChange, LineNo: "C1", ItemID: &scrap}}}, true)
	expectProblem(t, err, 409, domain.CodeEditTargetInUse)
	_, err = st.edit(domain.OrderEdits{CoProducts: []domain.CoProductEdit{{Op: domain.EditChange, LineNo: "C1", QtyPer: qty("0.1")}}}, false)
	expectProblem(t, err, 403, CodeEditAfterRelease)
	dated := len(scheduledEvents(t, pool, o.ID))
	got, err := st.edit(domain.OrderEdits{CoProducts: []domain.CoProductEdit{{Op: domain.EditChange, LineNo: "C1", QtyPer: qty("0.1")}}}, true)
	if err != nil || !got.CoProducts[0].QtyPlanned.Equal(d("10")) {
		t.Fatalf("quantity change on a journaled line: %v %+v", err, got.CoProducts)
	}
	// Co-product edits alone leave the route: the dated order is not re-dated.
	if n := len(scheduledEvents(t, pool, o.ID)); dated == 0 || n != dated {
		t.Fatalf("a co-product edit re-dated the order: %d scheduled events before, %d after", dated, n)
	}
}

// A rework order yields no co-products (FR-21.20), so an edit cannot add one.
func TestCoProducts_ReworkOrderTakesNone(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(94))
	in := sampleOrder(t, "key-co-rework", ids.New())
	in.OriginType, in.ReworkReference = domain.OriginRework, &domain.ReworkRef{Type: domain.ReworkRefBatch, ID: "B-2026-11"}
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	scrap := ids.New()
	_, err = s.EditOrder(ctx, OrderEdit{OrderID: o.ID, IfMatch: o.Version, Actor: "planner", Edits: domain.OrderEdits{CoProducts: []domain.CoProductEdit{
		{Op: domain.EditAdd, LineNo: "C1", ItemID: &scrap, QtyPer: qty("0.1")}}}})
	expectProblem(t, err, 422, domain.CodeInvalidInput)
}
