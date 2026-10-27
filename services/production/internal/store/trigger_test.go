package store

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

// routedOrder is sampleOrder with the route numbers given, 0.1 h per unit at 30.00 each.
func routedOrder(t *testing.T, key, legalEntity string, numbers ...int) NewOrder {
	t.Helper()
	in := sampleOrder(t, key, legalEntity)
	ops := make([]domain.Operation, len(numbers))
	for i, n := range numbers {
		ops[i] = domain.Operation{OperNumber: n, ResourceID: ids.New(), RunHPer: d("0.1"), LaborRate: d("30")}
	}
	planned, err := domain.PlanOperations(ops, in.Quantity)
	if err != nil {
		t.Fatal(err)
	}
	in.Operations = planned
	return in
}

func (st *stepper) trigger(in NewTriggerPoint) (TriggerPoint, bool, error) {
	in.OrderID, in.Actor = st.orderID, "engineer-1"
	if in.IdempotencyKey == "" {
		st.n++
		in.IdempotencyKey = st.orderID + "-trigger-" + string(rune('a'+st.n))
	}
	if in.RequestHash == "" {
		in.RequestHash = in.IdempotencyKey
	}
	return st.s.CreateTriggerPoint(st.ctx, in)
}

func (st *stepper) mustTrigger(in NewTriggerPoint) TriggerPoint {
	st.t.Helper()
	tp, _, err := st.trigger(in)
	if err != nil {
		st.t.Fatalf("define trigger point: %v", err)
	}
	return tp
}

// routeCard creates and posts a route card with one hours line per input.
func (st *stepper) routeCard(hours ...HoursInput) Journal {
	st.t.Helper()
	j := st.mustJournal(NewJournal{Type: domain.JournalRouteCard, Hours: hours})
	return st.mustPost(j.ID)
}

func firedEvents(t *testing.T, pool *pgxpool.Pool, orderID string) []prodevents.TriggerPointFiredData {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select payload from outbox where aggregate_id = $1 and type = $2 order by created_at, id`,
		orderID, prodevents.TriggerPointFired)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []prodevents.TriggerPointFiredData
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var evt struct {
			Data prodevents.TriggerPointFiredData `json:"data"`
		}
		if err := json.Unmarshal(raw, &evt); err != nil {
			t.Fatal(err)
		}
		out = append(out, evt.Data)
	}
	return out
}

func operNumbers(ops []domain.Operation) []int {
	out := make([]int, len(ops))
	for i, op := range ops {
		out[i] = op.OperNumber
	}
	return out
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// AC-21.13 (FR-21.17, BR-21.13): "qty_error > 0 → insert rework operations R10, R20" on
// operation 20; a route card posting 3 error units inserts operations 25 and 26 after it.
// The operation's other due triggers act in ascending sequence in the same post: a
// notification and a rework order for the 3 units. A non-repeatable trigger fires once.
func TestTriggers_AC2113(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(61))
	st := startedOrder(t, s, ctx, routedOrder(t, "key-trigger", ids.New(), 10, 20, 30))
	r10, r20, rw := ids.New(), ids.New(), ids.New()

	// Defined out of sequence order on purpose: they still act 10, 20, 30.
	rework := st.mustTrigger(NewTriggerPoint{OperNumber: 20, Sequence: 30, Condition: "qty_error > 0", Function: domain.FnCreateReworkOrder,
		Operations: []domain.TriggerOperation{{ResourceID: rw, RunHPer: d("0.2"), LaborRate: d("30")}}})
	insert := st.mustTrigger(NewTriggerPoint{OperNumber: 20, Sequence: 10, Condition: "qty_error > 0", Function: domain.FnInsertOperations,
		Operations: []domain.TriggerOperation{{ResourceID: r10, RunHPer: d("0.5"), LaborRate: d("40")}, {ResourceID: r20, SetupH: d("1")}}})
	notify := st.mustTrigger(NewTriggerPoint{OperNumber: 20, Sequence: 20, Condition: "hours >= 1", Function: domain.FnNotify, Repeatable: true})
	quiet := st.mustTrigger(NewTriggerPoint{OperNumber: 10, Sequence: 10, Condition: "qty_error > 0", Function: domain.FnNotify})
	if insert.FiredCount != 0 || len(insert.Operations) != 2 || insert.Firings == nil || insert.CreatedBy != "engineer-1" {
		t.Fatalf("stored trigger point: %+v", insert)
	}

	versionBefore := st.version
	draft := st.mustJournal(NewJournal{Type: domain.JournalRouteCard, Hours: []HoursInput{{OperNumber: 10, Hours: d("2"), QtyGood: d("100")},
		{OperNumber: 20, Hours: d("2.5"), QtyGood: d("97"), QtyError: d("3")}}})
	post := PostRequest{JournalID: draft.ID, OverConsumptionPct: d("10"), AutoStart: true, Actor: "supervisor-1", IdempotencyKey: "post-rc-1"}
	j, _, err := s.PostJournal(ctx, post)
	if err != nil {
		t.Fatal(err)
	}
	if len(j.TriggerFirings) != 3 {
		t.Fatalf("firings: %+v", j.TriggerFirings)
	}
	ins, note, rew := j.TriggerFirings[0], j.TriggerFirings[1], j.TriggerFirings[2]
	if ins.TriggerPointID != insert.ID || ins.Status != FiringFired || !sameInts(ins.InsertedOperations, []int{25, 26}) || ins.Reason != nil ||
		!ins.Feedback.QtyError.Equal(d("3")) || !ins.Feedback.Hours.Equal(d("2.5")) {
		t.Fatalf("insert firing: %+v", ins)
	}
	if note.TriggerPointID != notify.ID || note.Status != FiringFired || len(note.InsertedOperations) != 0 || note.ReworkOrderID != nil {
		t.Fatalf("notify firing: %+v", note)
	}
	if rew.TriggerPointID != rework.ID || rew.Status != FiringFired || rew.ReworkOrderID == nil {
		t.Fatalf("rework firing: %+v", rew)
	}

	o := st.refresh()
	if !sameInts(operNumbers(o.Operations), []int{10, 20, 25, 26, 30}) || o.Version != versionBefore+1 {
		t.Fatalf("route after the insertion: %v (version %d)", operNumbers(o.Operations), o.Version)
	}
	r25, r26 := o.Operations[2], o.Operations[3]
	if r25.ResourceID != r10 || r26.ResourceID != r20 || !r25.ProcessQty.Equal(d("3")) || !r26.SetupH.Equal(d("1")) ||
		!r25.LaborRate.Equal(d("40")) || r25.Status != domain.OperOpen || !r25.HoursReported.IsZero() {
		t.Fatalf("inserted operations: %+v %+v", r25, r26)
	}
	// The inserted operation takes route-card feedback like any other.
	st.routeCard(HoursInput{OperNumber: 25, Hours: d("1.5"), QtyGood: d("3"), Finished: true})

	// FR-21.20: the rework order covers the 3 error units of the same product, against this order.
	st.refresh()
	ro, err := s.GetOrder(ctx, *rew.ReworkOrderID)
	if err != nil {
		t.Fatal(err)
	}
	if ro.OriginType != domain.OriginRework || ro.OriginID == nil || *ro.OriginID != o.ID || ro.ReworkReference == nil ||
		ro.ReworkReference.ID != o.ID || ro.ProductID != o.ProductID || !ro.Quantity.Equal(d("3")) || ro.Status != domain.Created ||
		len(ro.Lines) != 0 || len(ro.Operations) != 1 || ro.Operations[0].ResourceID != rw || !ro.Operations[0].ProcessQty.Equal(d("3")) ||
		ro.WarehouseID != o.WarehouseID || ro.Currency != "USD" {
		t.Fatalf("rework order: %+v", ro)
	}
	if types := outboxTypes(t, pool, ro.ID); len(types) != 1 || types[0] != prodevents.OrderCreated {
		t.Fatalf("rework order events: %v", types)
	}

	evts := firedEvents(t, pool, o.ID)
	if len(evts) != 3 || evts[0].Function != string(domain.FnInsertOperations) || !sameInts(evts[0].InsertedOperations, []int{25, 26}) ||
		evts[0].Feedback.QtyError != "3" || evts[0].JournalID != j.ID || evts[0].Sequence != 10 || evts[0].Condition != "qty_error > 0" ||
		evts[1].Function != string(domain.FnNotify) || evts[2].ReworkOrderID == nil || *evts[2].ReworkOrderID != ro.ID || evts[2].Actor != "supervisor-1" {
		t.Fatalf("triggerpoint.fired: %+v", evts)
	}

	// Feedback again on 20: the fired once-only triggers stay quiet, the repeatable one fires again.
	again := st.routeCard(HoursInput{OperNumber: 20, Hours: d("1"), QtyError: d("1")})
	if len(again.TriggerFirings) != 1 || again.TriggerFirings[0].TriggerPointID != notify.ID {
		t.Fatalf("second post firings: %+v", again.TriggerFirings)
	}
	tps, err := s.ListTriggerPoints(ctx, o.ID)
	if err != nil || len(tps) != 4 {
		t.Fatalf("list: %d %v", len(tps), err)
	}
	counts := map[string]int{}
	for _, tp := range tps {
		counts[tp.ID] = tp.FiredCount
		if tp.FiredCount > 0 && (tp.LastFiredAt == nil || len(tp.Firings) != tp.FiredCount) {
			t.Fatalf("trigger point %s: %+v", tp.ID, tp)
		}
	}
	if counts[insert.ID] != 1 || counts[notify.ID] != 2 || counts[rework.ID] != 1 || counts[quiet.ID] != 0 {
		t.Fatalf("fired counts: %v", counts)
	}
	if tps[0].ID != quiet.ID || tps[1].ID != insert.ID || tps[3].ID != rework.ID {
		t.Fatal("trigger points are not listed by operation and sequence")
	}

	// A replayed post reads the firings back; reading the journal does too.
	replay, replayed, err := s.PostJournal(ctx, post)
	if err != nil || !replayed || len(replay.TriggerFirings) != 3 || replay.TriggerFirings[2].ReworkOrderID == nil {
		t.Fatalf("replayed firings: %v %+v %v", replayed, replay.TriggerFirings, err)
	}
	if read, err := s.GetJournal(ctx, j.ID); err != nil || len(read.TriggerFirings) != 3 || read.TriggerFirings[0].ID != ins.ID {
		t.Fatalf("journal read: %+v %v", read.TriggerFirings, err)
	}
	if len(firedEvents(t, pool, o.ID)) != 4 {
		t.Fatal("a read or replay published firings")
	}

	// A fired trigger point is kept; one that never fired can be removed.
	expectProblem(t, s.DeleteTriggerPoint(ctx, insert.ID), http.StatusConflict, CodeTriggerPointFired)
	if err := s.DeleteTriggerPoint(ctx, quiet.ID); err != nil {
		t.Fatal(err)
	}
	expectProblem(t, s.DeleteTriggerPoint(ctx, quiet.ID), http.StatusNotFound, CodeTriggerPointNotFound)
}

// BR-21.13: a function that fails changes nothing, is recorded with its reason
// (PROD-0019) and does not block the post; the trigger stays due.
func TestTriggers_FailureDoesNotBlockThePost(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(62))
	st := startedOrder(t, s, ctx, routedOrder(t, "key-trigger-fail", ids.New(), 10, 11))
	crowded := st.mustTrigger(NewTriggerPoint{OperNumber: 10, Sequence: 1, Condition: "qty_error >= 1", Function: domain.FnInsertOperations,
		Operations: []domain.TriggerOperation{{ResourceID: ids.New()}, {ResourceID: ids.New()}}})
	noErrors := st.mustTrigger(NewTriggerPoint{OperNumber: 11, Sequence: 1, Condition: "hours > 0", Function: domain.FnCreateReworkOrder,
		Operations: []domain.TriggerOperation{{ResourceID: ids.New()}}})

	j := st.routeCard(HoursInput{OperNumber: 10, Hours: d("1"), QtyError: d("2")}, HoursInput{OperNumber: 11, Hours: d("1"), QtyGood: d("98")})
	if !j.Posted || len(j.Postings) == 0 || len(j.TriggerFirings) != 2 {
		t.Fatalf("journal: posted %v, firings %+v", j.Posted, j.TriggerFirings)
	}
	for _, f := range j.TriggerFirings {
		if f.Status != FiringFailed || f.Reason == nil || *f.Reason == "" || len(f.InsertedOperations) != 0 || f.ReworkOrderID != nil {
			t.Fatalf("failed firing: %+v", f)
		}
	}
	o := st.refresh()
	if !sameInts(operNumbers(o.Operations), []int{10, 11}) || !o.Operations[0].HoursReported.Equal(d("1")) {
		t.Fatalf("route after failed firings: %+v", o.Operations)
	}
	if page, err := s.ListOrders(ctx, ListFilter{OriginType: domain.OriginRework}); err != nil || len(page.Items) != 0 {
		t.Fatalf("a failed create-rework-order left an order: %+v %v", page.Items, err)
	}
	evts := firedEvents(t, pool, o.ID)
	if len(evts) != 2 || evts[0].Status != FiringFailed || evts[0].Reason == nil || evts[1].TriggerPointID != noErrors.ID {
		t.Fatalf("failed events: %+v", evts)
	}
	tps, _ := s.ListTriggerPoints(ctx, o.ID)
	if tps[0].ID != crowded.ID || tps[0].FiredCount != 0 || tps[0].LastFiredAt != nil || len(tps[0].Firings) != 1 {
		t.Fatalf("failed trigger point: %+v", tps[0])
	}
	// Still due: the next feedback evaluates it again.
	if next := st.routeCard(HoursInput{OperNumber: 10, Hours: d("1"), QtyError: d("1")}); len(next.TriggerFirings) != 1 {
		t.Fatalf("next firings: %+v", next.TriggerFirings)
	}
}

// Setup rules: the order, the operation and a free sequence; Idempotency-Key replays;
// closed orders; tenant isolation.
func TestTriggers_Setup(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(63))
	in := routedOrder(t, "key-trigger-setup", ids.New(), 10, 20)
	allProfile(t, s, ctx, in.LegalEntityID)
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	def := NewTriggerPoint{OperNumber: 20, Sequence: 1, Condition: "qty_error > 0", Function: domain.FnNotify, IdempotencyKey: "tp-1", RequestHash: "h1"}

	first, created, err := st.trigger(def)
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	again, created, err := st.trigger(def)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("replay: %+v %v %v", again, created, err)
	}
	reuse := def
	reuse.RequestHash = "h2"
	_, _, err = st.trigger(reuse)
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeIdempotencyReuse)

	dup := def
	dup.IdempotencyKey, dup.RequestHash = "tp-2", "h3"
	_, _, err = st.trigger(dup)
	expectProblem(t, err, http.StatusConflict, CodeTriggerPointExists)

	missing := def
	missing.IdempotencyKey, missing.RequestHash, missing.OperNumber = "tp-3", "h4", 15
	_, _, err = st.trigger(missing)
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeTriggerOperationMissing)

	_, _, err = s.CreateTriggerPoint(ctx, NewTriggerPoint{OrderID: ids.New(), OperNumber: 10, Sequence: 1, Condition: "hours > 0",
		Function: domain.FnNotify, Actor: "x", IdempotencyKey: "tp-4", RequestHash: "h5"})
	expectProblem(t, err, http.StatusNotFound, CodeOrderNotFound)
	if _, err := s.ListTriggerPoints(ctx, ids.New()); err == nil {
		t.Fatal("list of an unknown order")
	}

	// Another tenant sees neither the order nor its trigger points.
	other := tenantCtx(testkit.TenantID(64))
	if _, err := s.ListTriggerPoints(other, o.ID); err == nil {
		t.Fatal("another tenant listed the trigger points")
	}
	expectProblem(t, s.DeleteTriggerPoint(other, first.ID), http.StatusNotFound, CodeTriggerPointNotFound)

	// FR-21.18: removing the operation removes its trigger points.
	if _, err := st.edit(domain.OrderEdits{Operations: []domain.OperationEdit{{Op: domain.EditRemove, OperNumber: 20}}}, false); err != nil {
		t.Fatal(err)
	}
	if tps, err := s.ListTriggerPoints(ctx, o.ID); err != nil || len(tps) != 0 {
		t.Fatalf("trigger points of a removed operation: %+v %v", tps, err)
	}

	st.must(domain.CmdCancel, nil)
	closed := def
	closed.IdempotencyKey, closed.RequestHash, closed.OperNumber = "tp-5", "h6", 10
	_, _, err = st.trigger(closed)
	expectProblem(t, err, http.StatusConflict, CodeTriggerOrderClosed)
}
