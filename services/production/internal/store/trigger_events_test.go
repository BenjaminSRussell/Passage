package store

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

// FR-21.17 "operation status change": trigger points on the operation finishing are evaluated
// against the operation's totals, by the :finish command and by a card that finishes it, and
// never by feedback; a function that fails is recorded and the finish stands.
func TestTriggers_OperationFinished(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(65))
	st := startedOrder(t, s, ctx, routedOrder(t, "key-trigger-finish", ids.New(), 10, 20, 30))
	rw := ids.New()
	fin := domain.EventOperationFinished

	// Compound condition over derived fields: 10 of 100 is 10 %, and 3 h is more than 2.
	notify := st.mustTrigger(NewTriggerPoint{OperNumber: 20, Sequence: 10, Event: fin, Condition: "error_pct >= 5 and hours > 2", Function: domain.FnNotify})
	rework := st.mustTrigger(NewTriggerPoint{OperNumber: 20, Sequence: 20, Event: fin, Condition: "qty_error > 0 or qty_total < 1", Function: domain.FnCreateReworkOrder,
		Operations: []domain.TriggerOperation{{ResourceID: rw, RunHPer: d("0.2"), LaborRate: d("30")}}})
	notFired := st.mustTrigger(NewTriggerPoint{OperNumber: 20, Sequence: 30, Event: fin, Condition: "error_pct > 50", Function: domain.FnNotify})
	onFeedback := st.mustTrigger(NewTriggerPoint{OperNumber: 20, Sequence: 40, Condition: "hours > 0", Function: domain.FnNotify})
	if notify.Event != fin || onFeedback.Event != domain.EventFeedback {
		t.Fatalf("stored events: %q %q", notify.Event, onFeedback.Event)
	}

	// Two cards of feedback: only the feedback trigger fires, once per card (it is not repeatable: once).
	first := st.routeCard(HoursInput{OperNumber: 20, Hours: d("1"), QtyGood: d("40"), QtyError: d("4")})
	second := st.routeCard(HoursInput{OperNumber: 20, Hours: d("2"), QtyGood: d("50"), QtyError: d("6")})
	if len(first.TriggerFirings) != 1 || first.TriggerFirings[0].Event != "feedback" || len(second.TriggerFirings) != 0 {
		t.Fatalf("feedback firings: %+v %+v", first.TriggerFirings, second.TriggerFirings)
	}

	// :finish evaluates the finish triggers against the totals: 3 h, 90 good, 10 error.
	if _, res, err := st.operate(20, domain.OperFinish, "finish-20"); err != nil || !res.Changed {
		t.Fatalf("finish: %+v %v", res, err)
	}
	o := st.refresh()
	tps, err := s.ListTriggerPoints(ctx, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	var fired []TriggerFiring
	byID := map[string]TriggerPoint{}
	for _, tp := range tps {
		byID[tp.ID] = tp
		for _, f := range tp.Firings {
			if f.Event == string(fin) {
				fired = append(fired, f)
			}
		}
	}
	if len(fired) != 2 || byID[notFired.ID].FiredCount != 0 || byID[onFeedback.ID].FiredCount != 1 {
		t.Fatalf("finish firings: %+v", fired)
	}
	n, r := byID[notify.ID].Firings[0], byID[rework.ID].Firings[0]
	if n.Status != FiringFired || n.JournalID != nil || !n.Feedback.Hours.Equal(d("3")) || !n.Feedback.QtyGood.Equal(d("90")) || !n.Feedback.QtyError.Equal(d("10")) {
		t.Fatalf("notify firing: %+v", n)
	}
	if r.Status != FiringFired || r.ReworkOrderID == nil || r.JournalID != nil {
		t.Fatalf("rework firing: %+v", r)
	}
	ro, err := s.GetOrder(ctx, *r.ReworkOrderID)
	if err != nil || !ro.Quantity.Equal(d("10")) || ro.OriginType != domain.OriginRework || ro.ReworkReference == nil || ro.ReworkReference.ID != o.ID {
		t.Fatalf("rework order: %+v %v", ro, err)
	}
	// Order of the outbox: the finish, then what its triggers did.
	evts := firedEvents(t, pool, o.ID)
	last := evts[len(evts)-2:]
	if last[0].Event != string(fin) || last[0].JournalID != nil || last[0].Sequence != 10 || last[0].Condition != "error_pct >= 5 and hours > 2" ||
		last[1].ReworkOrderID == nil || *last[1].ReworkOrderID != ro.ID || last[1].Feedback.QtyError != "10" {
		t.Fatalf("triggerpoint.fired on finish: %+v", last)
	}
	if ops := operationEvents(t, pool, o.ID); len(ops) != 1 || ops[0].Action != string(domain.OperFinish) {
		t.Fatalf("operation events: %+v", ops)
	}

	// Reopen and finish again: the once-only triggers stay fired and the finish needs no replay of them.
	st.operate(20, domain.OperReopen, "reopen-20")
	if _, _, err := st.operate(20, domain.OperFinish, "finish-20-again"); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.ListTriggerPoints(ctx, o.ID); countFinish(again) != 2 {
		t.Fatalf("a second finish fired once-only triggers again: %d", countFinish(again))
	}

	// A card that finishes operation 10 evaluates its finish triggers in the post.
	end := st.mustTrigger(NewTriggerPoint{OperNumber: 10, Sequence: 1, Event: fin, Condition: "hours >= 1", Function: domain.FnNotify})
	j := st.routeCard(HoursInput{OperNumber: 10, Hours: d("1.5"), QtyGood: d("100"), Finished: true})
	if len(j.TriggerFirings) != 0 {
		t.Fatalf("a finish firing is not a firing of the card: %+v", j.TriggerFirings)
	}
	after, _ := s.ListTriggerPoints(ctx, o.ID)
	for _, tp := range after {
		if tp.ID == end.ID && (tp.FiredCount != 1 || len(tp.Firings) != 1 || tp.Firings[0].Event != string(fin) || tp.Firings[0].JournalID != nil ||
			!tp.Firings[0].Feedback.Hours.Equal(d("1.5"))) {
			t.Fatalf("card finish firing: %+v", tp)
		}
	}
	if countFinish(after) != 3 {
		t.Fatalf("finish firings after the card: %d", countFinish(after))
	}

	// A repeatable finish trigger fires on every finish: finish, reopen, finish again.
	rep := st.mustTrigger(NewTriggerPoint{OperNumber: 30, Sequence: 1, Event: fin, Condition: "hours >= 0", Function: domain.FnNotify, Repeatable: true})
	for i, step := range []struct {
		action domain.OperAction
		key    string
	}{{domain.OperFinish, "finish-30"}, {domain.OperReopen, "reopen-30"}, {domain.OperFinish, "finish-30-again"}} {
		if _, res, err := st.operate(30, step.action, step.key); err != nil || !res.Changed {
			t.Fatalf("step %d %s: %+v %v", i, step.action, res, err)
		}
	}
	final, err := s.ListTriggerPoints(ctx, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tp := range final {
		if tp.ID == rep.ID && (tp.FiredCount != 2 || len(tp.Firings) != 2) {
			t.Fatalf("repeatable finish trigger: %+v", tp)
		}
	}
}

// An operation the last report as finished closes fires no finish trigger: the order is
// complete, so there is nothing left to insert or rework on it.
func TestTriggers_OperationClosedByReportAsFinishedFiresNothing(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(68))
	st := startedOrder(t, s, ctx, routedOrder(t, "key-trigger-finish-raf", ids.New(), 10))
	st.mustTrigger(NewTriggerPoint{OperNumber: 10, Sequence: 1, Event: domain.EventOperationFinished, Condition: "hours >= 0", Function: domain.FnNotify})
	st.routeCard(HoursInput{OperNumber: 10, Hours: d("1"), QtyGood: d("100")})
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalReportFinished, QtyGood: d("100")}).ID)
	o := st.refresh()
	if o.Operations[0].Status != domain.OperFinished {
		t.Fatalf("the report as finished did not close the operation: %+v", o.Operations[0])
	}
	if evts := firedEvents(t, pool, o.ID); len(evts) != 0 {
		t.Fatalf("an operation closed by the report as finished fired: %+v", evts)
	}
}

func countFinish(tps []TriggerPoint) int {
	n := 0
	for _, tp := range tps {
		for _, f := range tp.Firings {
			if f.Event == string(domain.EventOperationFinished) {
				n++
			}
		}
	}
	return n
}

// A finish trigger whose function cannot act is recorded as failed and the finish stands; the
// parameter that turns trigger points off silences them.
func TestTriggers_OperationFinishedFailureAndSwitch(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(66))
	st := startedOrder(t, s, ctx, routedOrder(t, "key-trigger-finish-fail", ids.New(), 10, 20))
	// No error quantity on the operation: the rework order has nothing to rework.
	bad := st.mustTrigger(NewTriggerPoint{OperNumber: 10, Sequence: 1, Event: domain.EventOperationFinished, Condition: "hours > 0", Function: domain.FnCreateReworkOrder,
		Operations: []domain.TriggerOperation{{ResourceID: ids.New()}}})
	st.routeCard(HoursInput{OperNumber: 10, Hours: d("1"), QtyGood: d("100")})
	o, res, err := st.operate(10, domain.OperFinish, "finish-10")
	if err != nil || !res.Changed || o.Operations[0].Status != domain.OperFinished {
		t.Fatalf("finish: %+v %+v %v", o.Operations[0], res, err)
	}
	tps, _ := s.ListTriggerPoints(ctx, o.ID)
	if len(tps) != 1 || tps[0].ID != bad.ID || tps[0].FiredCount != 0 || len(tps[0].Firings) != 1 || tps[0].Firings[0].Status != FiringFailed || tps[0].Firings[0].Reason == nil {
		t.Fatalf("failed finish trigger: %+v", tps)
	}
	if evts := firedEvents(t, pool, o.ID); len(evts) != 1 || evts[0].Status != FiringFailed || evts[0].JournalID != nil {
		t.Fatalf("failed event: %+v", evts)
	}

	// Trigger points off for the order's site: a finish fires nothing.
	saveParams(t, s, ctx, o.LegalEntityID, func(p *domain.Parameters) { p.TriggerPointsEnabled = false }, domain.AllKey, 0)
	st.mustTrigger(NewTriggerPoint{OperNumber: 20, Sequence: 1, Event: domain.EventOperationFinished, Condition: "hours >= 0", Function: domain.FnNotify})
	st.routeCard(HoursInput{OperNumber: 20, Hours: d("1"), QtyGood: d("100")})
	st.operate(20, domain.OperFinish, "finish-20")
	if evts := firedEvents(t, pool, o.ID); len(evts) != 1 {
		t.Fatalf("trigger points off still fired: %+v", evts)
	}
}

// FR-21.15 with FR-21.17: the new order of a split carries the original's trigger points on the
// same operations, with the count of times each fired, and its own firing history empty.
func TestSplit_CopiesTriggerPoints(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(67))
	in := routedOrder(t, "key-trigger-split", ids.New(), 10, 20)
	allProfile(t, s, ctx, in.LegalEntityID)
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	rw := ids.New()
	insert := st.mustTrigger(NewTriggerPoint{OperNumber: 20, Sequence: 10, Condition: "qty_error > 0", Function: domain.FnInsertOperations,
		Operations: []domain.TriggerOperation{{ResourceID: rw, RunHPer: d("0.5")}}})
	st.mustTrigger(NewTriggerPoint{OperNumber: 10, Sequence: 5, Event: domain.EventOperationFinished, Condition: "error_pct > 1 or hours > 4",
		Function: domain.FnNotify, Repeatable: true})
	// As if the insert trigger had fired on the original before the split.
	if _, err := pool.Exec(ctx, `update production_trigger_point set fired_count = 2 where id = $1`, insert.ID); err != nil {
		t.Fatal(err)
	}

	res, err := s.SplitOrder(ctx, Split{OrderID: o.ID, IfMatch: o.Version, SplitQty: d("40"), Actor: "planner-1", TraceID: "t", IdempotencyKey: "split-tp", RequestHash: "split-tp-40"})
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	copied, err := s.ListTriggerPoints(ctx, res.New.ID)
	if err != nil || len(copied) != 2 {
		t.Fatalf("copied trigger points: %+v %v", copied, err)
	}
	a, b := copied[0], copied[1] // by operation, then sequence
	if a.OperNumber != 10 || a.Event != domain.EventOperationFinished || a.Condition != "error_pct > 1 or hours > 4" || !a.Repeatable || a.FiredCount != 0 ||
		b.OperNumber != 20 || b.Sequence != 10 || b.Function != domain.FnInsertOperations || len(b.Operations) != 1 || b.Operations[0].ResourceID != rw ||
		b.FiredCount != 2 || b.LastFiredAt != nil || len(b.Firings) != 0 || b.ID == insert.ID {
		t.Fatalf("copies: %+v %+v", a, b)
	}
	orig, _ := s.ListTriggerPoints(ctx, o.ID)
	if len(orig) != 2 || orig[1].ID != insert.ID || orig[1].FiredCount != 2 {
		t.Fatalf("the original's trigger points changed: %+v", orig)
	}
	// A second split of the original copies again without clashing on keys.
	res2, err := s.SplitOrder(ctx, Split{OrderID: o.ID, IfMatch: res.Original.Version, SplitQty: d("10"), Actor: "planner-1", TraceID: "t", IdempotencyKey: "split-tp-2", RequestHash: "split-tp-10"})
	if err != nil {
		t.Fatalf("second split: %v", err)
	}
	if again, _ := s.ListTriggerPoints(ctx, res2.New.ID); len(again) != 2 {
		t.Fatalf("second copy: %+v", again)
	}
}
