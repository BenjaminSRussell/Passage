package store

import (
	"context"
	"net/http"
	"reflect"
	"testing"

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
