package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

func plannedGroup(o Order, g domain.CostGroup) decimal.Decimal {
	return groupSum(o.Costs, g)
}

func logCommands(t *testing.T, s *Postgres, ctx context.Context, orderID string) []string {
	t.Helper()
	entries, err := s.StatusLog(ctx, orderID)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, e := range entries {
		out = append(out, string(e.Command)+":"+string(e.From)+">"+string(e.To))
	}
	return out
}

// sameShape is another order of the same product, site, warehouse and BOM/route snapshot.
func sameShape(in NewOrder, key string) NewOrder {
	in.IdempotencyKey, in.RequestHash = key, "hash-"+key
	return in
}

// FR-21.15 split of an Estimated order: the sample order of 100 (2 + 1 kg per
// unit at 10.00, 0.5 h per unit) split 40 off.
func TestSplit_EstimatedOrder(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(61))
	o, _, err := s.CreateOrder(ctx, sampleOrder(t, "split-create", ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	st.must(domain.CmdEstimate, nil)

	in := Split{OrderID: o.ID, IfMatch: 2, SplitQty: d("40"), Actor: "planner-1", TraceID: "trace-s", IdempotencyKey: "split-1", RequestHash: "split-40"}
	res, err := s.SplitOrder(ctx, in)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	orig, split := res.Original, res.New
	if res.Replayed || !orig.Quantity.Equal(d("60")) || orig.Version != 3 || orig.Status != domain.Estimated {
		t.Fatalf("original: %+v", orig)
	}
	if !split.Quantity.Equal(d("40")) || split.Status != domain.Estimated || split.Version != 1 || split.OrderNumber != "PO-000002" ||
		split.OriginType != domain.OriginSplit || split.OriginID == nil || *split.OriginID != o.ID ||
		split.SplitFromOrderID == nil || *split.SplitFromOrderID != o.ID || split.DeliveryDate == nil || *split.DeliveryDate != "2026-11-30" {
		t.Fatalf("new order: %+v", split)
	}
	if !orig.Lines[0].QtyRequired.Equal(d("120")) || !orig.Lines[1].QtyRequired.Equal(d("60")) ||
		!split.Lines[0].QtyRequired.Equal(d("80")) || !split.Lines[1].QtyRequired.Equal(d("40")) {
		t.Fatalf("lines: original %+v, new %+v", orig.Lines, split.Lines)
	}
	if !orig.Operations[0].ProcessQty.Equal(d("60")) || !split.Operations[0].ProcessQty.Equal(d("40")) ||
		split.Operations[0].ResourceID != orig.Operations[0].ResourceID {
		t.Fatalf("operations: original %+v, new %+v", orig.Operations, split.Operations)
	}
	// Both orders are re-estimated: material 180 × 10 and 120 × 10, labor 30 h and 20 h at 30.00.
	if !plannedGroup(orig, domain.GroupMaterial).Equal(d("1800")) || !plannedGroup(split, domain.GroupMaterial).Equal(d("1200")) ||
		!plannedGroup(orig, domain.GroupLabor).Equal(d("900")) || !plannedGroup(split, domain.GroupLabor).Equal(d("600")) {
		t.Fatalf("planned cost: original %+v, new %+v", orig.Costs, split.Costs)
	}
	if got := logCommands(t, s, ctx, split.ID); !reflect.DeepEqual(got, []string{"split:Estimated>Estimated"}) {
		t.Fatalf("new order status log: %v", got)
	}
	if got := outboxTypes(t, pool, split.ID); !reflect.DeepEqual(got, []string{prodevents.OrderCreated}) {
		t.Fatalf("new order events: %v", got)
	}
	if got := outboxTypes(t, pool, o.ID); !reflect.DeepEqual(got, []string{prodevents.OrderCreated, prodevents.OrderStatusChanged, prodevents.OrderUpdated}) {
		t.Fatalf("original events: %v", got)
	}

	replay, err := s.SplitOrder(ctx, in)
	if err != nil || !replay.Replayed || replay.New.ID != split.ID || !replay.Original.Quantity.Equal(d("60")) {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	other := in
	other.RequestHash = "split-30"
	_, err = s.SplitOrder(ctx, other)
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeIdempotencyReuse)
	usedByCommand := in
	usedByCommand.IdempotencyKey = o.ID + "-estimate-" + string(rune('a'+1))
	_, err = s.SplitOrder(ctx, usedByCommand)
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeIdempotencyReuse)

	stale := Split{OrderID: o.ID, IfMatch: 2, SplitQty: d("10"), IdempotencyKey: "split-stale", RequestHash: "x"}
	_, err = s.SplitOrder(ctx, stale)
	expectProblem(t, err, http.StatusPreconditionFailed, CodeVersionConflict)
	whole := Split{OrderID: o.ID, IfMatch: 3, SplitQty: d("60"), IdempotencyKey: "split-whole", RequestHash: "x"}
	_, err = s.SplitOrder(ctx, whole)
	expectProblem(t, err, http.StatusUnprocessableEntity, domain.CodeSplitQuantity)
	multiple := Split{OrderID: o.ID, IfMatch: 3, SplitQty: d("10"), Multiple: d("4"), IdempotencyKey: "split-multiple", RequestHash: "x"}
	_, err = s.SplitOrder(ctx, multiple)
	expectProblem(t, err, http.StatusUnprocessableEntity, domain.CodeSplitQuantity)
	missing := Split{OrderID: ids.New(), IfMatch: 1, SplitQty: d("10"), IdempotencyKey: "split-missing", RequestHash: "x"}
	_, err = s.SplitOrder(ctx, missing)
	expectProblem(t, err, http.StatusNotFound, CodeOrderNotFound)

	// A cancelled order cannot be split (only Created to Released).
	nst := &stepper{t: t, s: s, ctx: ctx, orderID: split.ID, version: split.Version}
	nst.must(domain.CmdCancel, nil)
	_, err = s.SplitOrder(ctx, Split{OrderID: split.ID, IfMatch: nst.version, SplitQty: d("10"), IdempotencyKey: "split-cancelled", RequestHash: "x"})
	expectProblem(t, err, http.StatusConflict, domain.CodeIllegalTransition)
}

// A split of a rework order stays a rework order of the same reference, so it
// settles as one.
func TestSplit_ReworkOrderKeepsItsReference(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(62))
	in := sampleOrder(t, "split-rework", ids.New())
	in.OriginType, in.ReworkReference = domain.OriginRework, &domain.ReworkRef{Type: domain.ReworkRefBatch, ID: "B-7"}
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.SplitOrder(ctx, Split{OrderID: o.ID, IfMatch: 1, SplitQty: d("25"), IdempotencyKey: "split-rework-1", RequestHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	n := res.New
	if n.OriginType != domain.OriginRework || n.ReworkReference == nil || *n.ReworkReference != *in.ReworkReference ||
		n.SplitFromOrderID == nil || *n.SplitFromOrderID != o.ID || n.Status != domain.Created || len(n.Costs) != 0 {
		t.Fatalf("split of a rework order: %+v", n)
	}
}

func TestMerge_IntoTheFirstOrder(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(63))
	base := sampleOrder(t, "merge-a", ids.New())
	a, _, err := s.CreateOrder(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := s.CreateOrder(ctx, sameShape(base, "merge-b"))
	if err != nil {
		t.Fatal(err)
	}
	ast := &stepper{t: t, s: s, ctx: ctx, orderID: a.ID, version: a.Version}
	ast.must(domain.CmdEstimate, nil)

	in := Merge{Orders: []MergeRef{{a.ID, 2}, {b.ID, 1}}, Actor: "planner-1", TraceID: "trace-m", IdempotencyKey: "merge-1", RequestHash: "m1"}
	res, err := s.MergeOrders(ctx, in)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	sv := res.Survivor
	if res.Replayed || sv.ID != a.ID || !sv.Quantity.Equal(d("200")) || sv.Version != 3 || sv.Status != domain.Estimated ||
		!reflect.DeepEqual(res.Merged, []string{b.ID}) {
		t.Fatalf("merge result: %+v", res)
	}
	if !sv.Lines[0].QtyRequired.Equal(d("400")) || !sv.Lines[1].QtyRequired.Equal(d("200")) || !sv.Operations[0].ProcessQty.Equal(d("200")) ||
		!plannedGroup(sv, domain.GroupMaterial).Equal(d("6000")) {
		t.Fatalf("survivor not re-exploded and re-estimated: %+v %+v %+v", sv.Lines, sv.Operations, sv.Costs)
	}
	gone, err := s.GetOrder(ctx, b.ID)
	if err != nil || gone.Status != domain.Cancelled || gone.MergedIntoOrderID == nil || *gone.MergedIntoOrderID != a.ID || gone.Version != 2 {
		t.Fatalf("merged order: %+v %v", gone, err)
	}
	if got := logCommands(t, s, ctx, b.ID); !reflect.DeepEqual(got, []string{"merge:Created>Cancelled"}) {
		t.Fatalf("merged order status log: %v", got)
	}
	if got := outboxTypes(t, pool, b.ID); !reflect.DeepEqual(got, []string{prodevents.OrderCreated, prodevents.OrderStatusChanged}) {
		t.Fatalf("merged order events: %v", got)
	}
	if got := outboxTypes(t, pool, a.ID); got[len(got)-1] != prodevents.OrderUpdated {
		t.Fatalf("survivor events: %v", got)
	}

	replay, err := s.MergeOrders(ctx, in)
	if err != nil || !replay.Replayed || replay.Survivor.Version != 3 {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	other := in
	other.RequestHash = "m2"
	_, err = s.MergeOrders(ctx, other)
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeIdempotencyReuse)

	// A cancelled order, another product and a stale version are refused.
	c, _, err := s.CreateOrder(ctx, sameShape(base, "merge-c"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.MergeOrders(ctx, Merge{Orders: []MergeRef{{a.ID, 3}, {b.ID, 2}}, IdempotencyKey: "merge-cancelled", RequestHash: "x"})
	expectProblem(t, err, http.StatusUnprocessableEntity, domain.CodeMergeRefused)
	otherProduct, _, err := s.CreateOrder(ctx, sampleOrder(t, "merge-other", base.LegalEntityID))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.MergeOrders(ctx, Merge{Orders: []MergeRef{{a.ID, 3}, {otherProduct.ID, 1}}, IdempotencyKey: "merge-product", RequestHash: "x"})
	expectProblem(t, err, http.StatusUnprocessableEntity, domain.CodeMergeRefused)
	_, err = s.MergeOrders(ctx, Merge{Orders: []MergeRef{{a.ID, 2}, {c.ID, 1}}, IdempotencyKey: "merge-stale", RequestHash: "x"})
	expectProblem(t, err, http.StatusPreconditionFailed, CodeVersionConflict)
	if again, _ := s.GetOrder(ctx, c.ID); again.Status != domain.Created || again.Version != 1 {
		t.Fatalf("a refused merge changed order c: %+v", again)
	}
}

// PROD-0016: an order whose journals were posted (and reversed by a reset back
// to Released) cannot be merged.
func TestMerge_RefusesPostedJournals(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(64))
	base := sampleOrder(t, "merge-posted", ids.New())
	st := startedOrder(t, s, ctx, base)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalPickingList}).ID)
	back := st.must(domain.CmdResetStatus, nil)
	if back.Status != domain.Released {
		t.Fatalf("reset: %s", back.Status)
	}
	fresh, _, err := s.CreateOrder(ctx, sameShape(base, "merge-fresh"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.MergeOrders(ctx, Merge{Orders: []MergeRef{{fresh.ID, 1}, {st.orderID, st.version}}, IdempotencyKey: "merge-posted-1", RequestHash: "x"})
	expectProblem(t, err, http.StatusUnprocessableEntity, domain.CodeMergeRefused)
	// The posted order cannot survive a merge either.
	_, err = s.MergeOrders(ctx, Merge{Orders: []MergeRef{{st.orderID, st.version}, {fresh.ID, 1}}, IdempotencyKey: "merge-posted-2", RequestHash: "x"})
	expectProblem(t, err, http.StatusUnprocessableEntity, domain.CodeMergeRefused)
	// Draft journals on an order that would be cancelled are refused as well.
	other, _, err := s.CreateOrder(ctx, sameShape(base, "merge-draft"))
	if err != nil {
		t.Fatal(err)
	}
	ost := &stepper{t: t, s: s, ctx: ctx, orderID: other.ID, version: other.Version}
	for _, cmd := range []domain.Command{domain.CmdEstimate, domain.CmdSchedule, domain.CmdRelease} {
		ost.must(cmd, nil)
	}
	ost.mustJournal(NewJournal{Type: domain.JournalRouteCard, AutoStart: true})
	_, err = s.MergeOrders(ctx, Merge{Orders: []MergeRef{{fresh.ID, 1}, {other.ID, ost.version}}, IdempotencyKey: "merge-draft-1", RequestHash: "x"})
	expectProblem(t, err, http.StatusUnprocessableEntity, domain.CodeMergeRefused)
}

// FR-21.12: per-order results, continue on error, and a retried batch replays
// the orders it already moved.
func TestBulkTransition(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(65))
	le := ids.New()
	x, _, err := s.CreateOrder(ctx, sampleOrder(t, "bulk-x", le))
	if err != nil {
		t.Fatal(err)
	}
	y, _, err := s.CreateOrder(ctx, sampleOrder(t, "bulk-y", le))
	if err != nil {
		t.Fatal(err)
	}
	unknown := ids.New()
	batch := Bulk{OrderIDs: []string{x.ID, unknown, y.ID}, Command: domain.CmdEstimate, Actor: "planner-1", IdempotencyKey: "bulk-1", RequestHash: "estimate"}
	items := s.BulkTransition(ctx, batch)
	if len(items) != 3 || !items[0].OK || items[0].Status != domain.Estimated || items[0].Version != 2 || items[1].OK ||
		items[1].Code != CodeOrderNotFound || items[1].OrderID != unknown || !items[2].OK || items[2].Replayed {
		t.Fatalf("bulk estimate: %+v", items)
	}
	again := s.BulkTransition(ctx, batch)
	if !again[0].OK || !again[0].Replayed || again[0].Version != 2 || again[1].OK || !again[2].Replayed {
		t.Fatalf("retried batch: %+v", again)
	}
	if o, _ := s.GetOrder(ctx, x.ID); o.Version != 2 || len(logCommands(t, s, ctx, x.ID)) != 1 {
		t.Fatalf("a replay changed the order: %+v", o)
	}
	s.BulkTransition(ctx, Bulk{OrderIDs: []string{x.ID}, Command: domain.CmdSchedule, IdempotencyKey: "bulk-2", RequestHash: "schedule"})
	items = s.BulkTransition(ctx, Bulk{OrderIDs: []string{x.ID, y.ID}, Command: domain.CmdRelease, IdempotencyKey: "bulk-3", RequestHash: "release"})
	if !items[0].OK || items[0].Status != domain.Released || items[1].OK || items[1].Code != domain.CodeIllegalTransition || items[1].Detail == "" {
		t.Fatalf("bulk release: %+v", items)
	}
}

// AC-21.11 (FR-21.15, BR-21.11): a Started order with posted consumption and
// hours is split 40 of 100 with proportional allocation. The worked example of
// BR-21.7 (203 + 102 kg at 10.20, 46 h at 30.00 + 10.00) gives the new order
// 40 % of everything and both WIP balances reconcile.
func TestSplit_StartedOrderMovesConsumptionHoursAndWIP(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(63))
	st := startedOrder(t, s, ctx, sampleOrder(t, "split-started", ids.New()))
	pick := st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{
		{LineNo: "10", Qty: d("203"), UnitCost: qty("10.20")}, {LineNo: "20.1", Qty: d("102"), UnitCost: qty("10.20")}}})
	st.mustPost(pick.ID)
	route := st.mustJournal(NewJournal{Type: domain.JournalRouteCard, Hours: []HoursInput{{OperNumber: 10, Hours: d("46"), QtyGood: d("100")}}})
	st.mustPost(route.ID)
	before := st.costs()
	if !before.WIPBalance.Equal(d("4951")) { // 3111 + 1380 + 460
		t.Fatalf("WIP before the split: %s", before.WIPBalance)
	}

	in := Split{OrderID: st.orderID, IfMatch: st.version, SplitQty: d("40"), Actor: "planner-1", TraceID: "t", IdempotencyKey: "split-st-1", RequestHash: "h1"}
	res, err := s.SplitOrder(ctx, in)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	orig, split := res.Original, res.New
	if orig.Status != domain.Started || split.Status != domain.Started || !orig.Quantity.Equal(d("60")) || !split.Quantity.Equal(d("40")) {
		t.Fatalf("orders: %s %s / %s %s", orig.Status, orig.Quantity, split.Status, split.Quantity)
	}
	// Consumption: 40 % of 203 and 102 moves; the original keeps the rest.
	if !split.Lines[0].QtyConsumed.Equal(d("81.2")) || !orig.Lines[0].QtyConsumed.Equal(d("121.8")) ||
		!split.Lines[1].QtyConsumed.Equal(d("40.8")) || !orig.Lines[1].QtyConsumed.Equal(d("61.2")) {
		t.Fatalf("consumed: original %+v, new %+v", orig.Lines, split.Lines)
	}
	if !split.Operations[0].HoursReported.Equal(d("18.4")) || !orig.Operations[0].HoursReported.Equal(d("27.6")) ||
		!split.Operations[0].QtyGood.Equal(d("40")) || !orig.Operations[0].QtyGood.Equal(d("60")) ||
		split.Operations[0].Status != domain.OperStarted || orig.Operations[0].Status != domain.OperStarted {
		t.Fatalf("hours: original %+v, new %+v", orig.Operations, split.Operations)
	}
	// WIP: 40 % of 4951.00 moves (material 1244.40, labor 552.00, overhead 184.00).
	oc, err := s.Costs(ctx, orig.ID)
	if err != nil {
		t.Fatal(err)
	}
	nc, err := s.Costs(ctx, split.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !nc.WIPBalance.Equal(d("1980.4")) || !oc.WIPBalance.Equal(d("2970.6")) || !nc.WIPBalance.Add(oc.WIPBalance).Equal(before.WIPBalance) {
		t.Fatalf("WIP balances: original %s, new %s", oc.WIPBalance, nc.WIPBalance)
	}
	if !groupSum(nc.Actual, domain.GroupMaterial).Equal(d("1244.4")) || !groupSum(oc.Actual, domain.GroupMaterial).Equal(d("1866.6")) ||
		!groupSum(nc.Actual, domain.GroupLabor).Equal(d("552")) || !groupSum(oc.Actual, domain.GroupLabor).Equal(d("828")) {
		t.Fatalf("actual cost: original %+v, new %+v", oc.Actual, nc.Actual)
	}
	if !ledgerNet(t, pool, orig.ID).IsZero() || !ledgerNet(t, pool, split.ID).IsZero() {
		t.Fatalf("a subledger does not balance: %s %s", ledgerNet(t, pool, orig.ID), ledgerNet(t, pool, split.ID))
	}

	// One posted split-transfer journal on each order, pointing at the other.
	oj, err := s.ListJournals(ctx, orig.ID)
	if err != nil {
		t.Fatal(err)
	}
	nj, err := s.ListJournals(ctx, split.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := oj[len(oj)-1]
	if len(nj) != 1 || nj[0].Type != domain.JournalSplitTransfer || last.Type != domain.JournalSplitTransfer || !last.Posted || !nj[0].Posted ||
		last.CounterpartOrderID == nil || *last.CounterpartOrderID != split.ID || nj[0].CounterpartOrderID == nil || *nj[0].CounterpartOrderID != orig.ID ||
		!last.Lines[0].Qty.Equal(d("-81.2")) || !nj[0].Lines[0].Qty.Equal(d("81.2")) {
		t.Fatalf("transfer journals: %+v / %+v", last, nj)
	}
	// The new order of a dated original is dated too (slice 18), after it is known and its transfer posted.
	if got := outboxTypes(t, pool, split.ID); !reflect.DeepEqual(got, []string{prodevents.OrderCreated, prodevents.JournalPosted, prodevents.OrderScheduled}) {
		t.Fatalf("new order events: %v", got)
	}
	var payload json.RawMessage
	if err := pool.QueryRow(context.Background(), `select payload from outbox where aggregate_id = $1 and type = $2`, split.ID, prodevents.JournalPosted).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Data prodevents.JournalPostedData `json:"data"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil || evt.Data.Type != string(domain.JournalSplitTransfer) ||
		evt.Data.CounterpartOrderID == nil || *evt.Data.CounterpartOrderID != orig.ID {
		t.Fatalf("journal.posted of the transfer: %s", payload)
	}

	replay, err := s.SplitOrder(ctx, in)
	if err != nil || !replay.Replayed || replay.New.ID != split.ID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if again, _ := s.ListJournals(ctx, split.ID); len(again) != 1 {
		t.Fatalf("a replay posted more transfers: %d", len(again))
	}

	// A reset would reverse the original's journals past what it now holds.
	ost := &stepper{t: t, s: s, ctx: ctx, orderID: orig.ID, version: orig.Version}
	_, _, err = ost.s.Transition(ctx, Transition{OrderID: orig.ID, IfMatch: ost.version, Command: domain.CmdResetStatus, ResetTo: domain.Released, Actor: "p", IdempotencyKey: "reset-1", RequestHash: "r"})
	expectProblem(t, err, http.StatusConflict, domain.CodeIllegalTransition)

	// A second split of the original shares what it holds after the first
	// transfer, the earlier transfer journal included: r = 30 / 60.
	o2, err := s.GetOrder(ctx, orig.ID)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := s.SplitOrder(ctx, Split{OrderID: orig.ID, IfMatch: o2.Version, SplitQty: d("30"), Actor: "planner-1", IdempotencyKey: "split-st-1b", RequestHash: "h1b"})
	if err != nil {
		t.Fatalf("second split: %v", err)
	}
	if !res2.New.Lines[0].QtyConsumed.Equal(d("60.9")) || !res2.Original.Lines[0].QtyConsumed.Equal(d("60.9")) ||
		!res2.New.Lines[1].QtyConsumed.Equal(d("30.6")) || !res2.New.Operations[0].HoursReported.Equal(d("13.8")) ||
		!res2.Original.Operations[0].HoursReported.Equal(d("13.8")) {
		t.Fatalf("second split: original %+v %+v, new %+v %+v", res2.Original.Lines, res2.Original.Operations, res2.New.Lines, res2.New.Operations)
	}
	for _, id := range []string{orig.ID, res2.New.ID} {
		c, err := s.Costs(ctx, id)
		if err != nil || !c.WIPBalance.Equal(d("1485.3")) || !groupSum(c.Actual, domain.GroupMaterial).Equal(d("933.3")) {
			t.Fatalf("second split %s: WIP %s, material %s, %v", id, c.WIPBalance, groupSum(c.Actual, domain.GroupMaterial), err)
		}
	}

	// Leaving ReportedAsFinished still works after a transfer: it reverses only
	// the reports as finished, which a split never shares.
	o3, _ := s.GetOrder(ctx, orig.ID)
	rst := &stepper{t: t, s: s, ctx: ctx, orderID: orig.ID, version: o3.Version}
	rst.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("10") })
	if back := rst.must(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Started }); back.Status != domain.Started || !back.QtyRAF.IsZero() {
		t.Fatalf("reset from ReportedAsFinished: %s %s", back.Status, back.QtyRAF)
	}
	if c, _ := s.Costs(ctx, orig.ID); !c.WIPBalance.Equal(d("1485.3")) {
		t.Fatalf("WIP after the RAF reset: %s", c.WIPBalance)
	}

	// The three orders still run to End on their own: the planned cost is split
	// too, so each order's actual cost clears its own WIP.
	for _, id := range []string{orig.ID, split.ID, res2.New.ID} {
		o, _ := s.GetOrder(ctx, id)
		e := &stepper{t: t, s: s, ctx: ctx, orderID: id, version: o.Version, n: 10} // past rst's keys
		e.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = o.Quantity })
		e.must(domain.CmdEnd, nil)
		c, err := s.Costs(ctx, id)
		if err != nil || !c.WIPBalance.IsZero() {
			t.Fatalf("End of %s: WIP %s %v", id, c.WIPBalance, err)
		}
	}
}

func TestSplit_StartedOrderRefusalsAndKeepOnOriginal(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(64))
	st := startedOrder(t, s, ctx, sampleOrder(t, "split-started-2", ids.New()))
	pick := st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "10", Qty: d("100"), UnitCost: qty("10")}}})
	st.mustPost(pick.ID)
	draft := st.mustJournal(NewJournal{Type: domain.JournalRouteCard, Hours: []HoursInput{{OperNumber: 10, Hours: d("5"), QtyGood: d("10")}}})

	in := Split{OrderID: st.orderID, IfMatch: st.version, SplitQty: d("40"), IdempotencyKey: "split-st-2", RequestHash: "h"}
	_, err := s.SplitOrder(ctx, in)
	expectProblem(t, err, http.StatusConflict, domain.CodeSplitRefused)
	if err := s.DeleteJournal(ctx, draft.ID); err != nil {
		t.Fatal(err)
	}

	in.Allocation = domain.AllocKeepOnOriginal
	res, err := s.SplitOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Original.Lines[0].QtyConsumed.Equal(d("100")) || !res.New.Lines[0].QtyConsumed.IsZero() {
		t.Fatalf("keep-on-original moved consumption: %+v / %+v", res.Original.Lines, res.New.Lines)
	}
	if nj, _ := s.ListJournals(ctx, res.New.ID); len(nj) != 0 {
		t.Fatalf("keep-on-original posted %d journals on the new order", len(nj))
	}
	nc, _ := s.Costs(ctx, res.New.ID)
	if !nc.WIPBalance.IsZero() {
		t.Fatalf("new order WIP: %s", nc.WIPBalance)
	}

	// An as-built record names a finished serial of the original and is held
	// against its consumption, so it blocks a split until it is detached.
	o := res.Original
	ab, _, err := s.AssignAsBuilt(ctx, NewAsBuilt{OrderID: o.ID, Actor: "operator-1", IdempotencyKey: "split-ab", RequestHash: "split-ab",
		AsBuilt: domain.AsBuilt{FinishedSerial: "S-1", ComponentItemID: o.Lines[0].ItemID, Tracking: domain.TrackBatch, ComponentSerial: "B-1", Qty: d("50")}})
	if err != nil {
		t.Fatal(err)
	}
	o, _ = s.GetOrder(ctx, o.ID)
	_, err = s.SplitOrder(ctx, Split{OrderID: o.ID, IfMatch: o.Version, SplitQty: d("5"), IdempotencyKey: "split-st-ab", RequestHash: "hab"})
	expectProblem(t, err, http.StatusConflict, domain.CodeSplitRefused)
	if _, err := s.DetachAsBuilt(ctx, ab.ID, "operator-1"); err != nil {
		t.Fatal(err)
	}

	// Reported output blocks a further split.
	o, _ = s.GetOrder(ctx, o.ID)
	st2 := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	st2.must(domain.CmdReportFinished, func(tr *Transition) { tr.Good = d("10") })
	_, err = s.SplitOrder(ctx, Split{OrderID: o.ID, IfMatch: st2.version, SplitQty: d("5"), IdempotencyKey: "split-st-3", RequestHash: "h3"})
	expectProblem(t, err, http.StatusConflict, domain.CodeIllegalTransition)
}

// AC-21.10 (FR-21.12): 500 Released orders bulk-started finish in under 5 minutes and the answer lists a
// result per order. The batch runs synchronously in the request here (the Temporal job is a wave 4 handoff),
// so this is the budget the request has to meet.
//
// The set-up alone (500 creates and three bulk commands) takes minutes on a loaded machine, so the test runs
// with PASSAGE_PERF_TEST=1 like the journal post budget (the bulk start itself took 22 s on a loaded laptop).
func TestBulkTransition_Start500ReleasedOrders(t *testing.T) {
	if os.Getenv("PASSAGE_PERF_TEST") != "1" {
		t.Skip("set PASSAGE_PERF_TEST=1 to run the 500-order bulk start budget")
	}
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(66))
	le := ids.New()
	allProfile(t, s, ctx, le)
	orderIDs := make([]string, 0, domain.MaxBulkOrders)
	for i := 0; i < domain.MaxBulkOrders; i++ {
		o, _, err := s.CreateOrder(ctx, sampleOrder(t, fmt.Sprintf("bulk500-%d", i), le))
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		orderIDs = append(orderIDs, o.ID)
	}
	for _, cmd := range []domain.Command{domain.CmdEstimate, domain.CmdSchedule, domain.CmdRelease} {
		for i, item := range s.BulkTransition(ctx, Bulk{OrderIDs: orderIDs, Command: cmd, Actor: "planner-1", IdempotencyKey: "prep-" + string(cmd), RequestHash: string(cmd)}) {
			if !item.OK {
				t.Fatalf("%s order %d: %+v", cmd, i, item)
			}
		}
	}
	began := time.Now()
	items := s.BulkTransition(ctx, Bulk{OrderIDs: orderIDs, Command: domain.CmdStart, Actor: "planner-1", IdempotencyKey: "bulk500-start", RequestHash: "start"})
	took := time.Since(began)
	if took > 5*time.Minute {
		t.Fatalf("bulk start of %d orders took %s, the budget is 5 minutes", len(orderIDs), took)
	}
	t.Logf("bulk start of %d Released orders took %s", len(orderIDs), took)
	if len(items) != len(orderIDs) {
		t.Fatalf("%d results for %d orders", len(items), len(orderIDs))
	}
	for i, item := range items {
		if item.OrderID != orderIDs[i] || !item.OK || item.Status != domain.Started || item.Replayed {
			t.Fatalf("order %d: %+v", i, item)
		}
	}
	// A retry of the whole batch (the same key) replays every order and changes none.
	for i, item := range s.BulkTransition(ctx, Bulk{OrderIDs: orderIDs, Command: domain.CmdStart, Actor: "planner-1", IdempotencyKey: "bulk500-start", RequestHash: "start"}) {
		if !item.OK || !item.Replayed || item.Status != domain.Started {
			t.Fatalf("replay of order %d: %+v", i, item)
		}
	}
}

// FR-21.1: the released product an order was created with stays with its quantity. A split's new order
// carries it, a merge's survivor takes it from an order merged into it, orders of different released
// products are refused under the locks, and a key that created an order cannot start a merge (the API skips
// the product's rules for a key it has seen).
func TestSplitAndMerge_KeepTheReleasedProduct(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(69))
	released, other := ids.New(), ids.New()
	base := sampleOrder(t, "rpk-a", ids.New())
	plain, _, err := s.CreateOrder(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	withProduct := sameShape(base, "rpk-b")
	withProduct.ReleasedProductID = released
	held, _, err := s.CreateOrder(ctx, withProduct)
	if err != nil {
		t.Fatal(err)
	}

	res, err := s.SplitOrder(ctx, Split{OrderID: held.ID, IfMatch: held.Version, SplitQty: d("40"), IdempotencyKey: "rpk-split", RequestHash: "s"})
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if res.New.ReleasedProductID == nil || *res.New.ReleasedProductID != released {
		t.Fatalf("split order's released product: %v", res.New.ReleasedProductID)
	}

	_, err = s.MergeOrders(ctx, Merge{Orders: []MergeRef{{plain.ID, plain.Version}, {held.ID, res.Original.Version}}, IdempotencyKey: "rpk-a", RequestHash: "m"})
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeIdempotencyReuse)

	foreign := sameShape(base, "rpk-c")
	foreign.ReleasedProductID = other
	fo, _, err := s.CreateOrder(ctx, foreign)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.MergeOrders(ctx, Merge{Orders: []MergeRef{{held.ID, res.Original.Version}, {fo.ID, fo.Version}}, IdempotencyKey: "rpk-merge-x", RequestHash: "x"})
	expectProblem(t, err, http.StatusUnprocessableEntity, domain.CodeMergeRefused)

	merged, err := s.MergeOrders(ctx, Merge{Orders: []MergeRef{{plain.ID, plain.Version}, {held.ID, res.Original.Version}}, IdempotencyKey: "rpk-merge", RequestHash: "m"})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if merged.Survivor.ReleasedProductID == nil || *merged.Survivor.ReleasedProductID != released || !merged.Survivor.Quantity.Equal(d("160")) {
		t.Fatalf("survivor: product %v quantity %s", merged.Survivor.ReleasedProductID, merged.Survivor.Quantity)
	}
}
