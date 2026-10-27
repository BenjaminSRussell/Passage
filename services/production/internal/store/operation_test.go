package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	prodevents "github.com/BenjaminSRussell/passage/services/production/internal/events"
)

// operate runs :finish or :reopen on an operation under key with the stepper's version.
func (st *stepper) operate(oper int, action domain.OperAction, key string) (Order, OperationOutcome, error) {
	o, res, err := st.s.ApplyOperationCommand(st.ctx, OperationCommand{OrderID: st.orderID, OperNumber: oper, Action: action,
		IfMatch: st.version, Actor: "supervisor-1", TraceID: "trace-op", IdempotencyKey: key, RequestHash: fmt.Sprintf("%s/%d", action, oper)})
	if err == nil {
		st.version = o.Version
	}
	return o, res, err
}

func operationEvents(t *testing.T, pool *pgxpool.Pool, orderID string) []prodevents.OperationStatusChangedData {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select payload from outbox where aggregate_id = $1 and type = $2 order by created_at, id`,
		orderID, prodevents.OperationStatusChanged)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []prodevents.OperationStatusChangedData
	for rows.Next() {
		var raw json.RawMessage
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var evt struct {
			Data prodevents.OperationStatusChangedData `json:"data"`
		}
		if err := json.Unmarshal(raw, &evt); err != nil {
			t.Fatal(err)
		}
		out = append(out, evt.Data)
	}
	return out
}

// FR-21.6: the finished flag closes an operation to further feedback unless reopened.
func TestOperationCommands_FinishAndReopen(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(41))
	st := startedOrder(t, s, ctx, routedOrder(t, "op-cmd", ids.New(), 10, 20, 30))

	// A route card closes operation 10 with its feedback; the next one is refused.
	st.routeCard(HoursInput{OperNumber: 10, Hours: d("1.5"), QtyGood: d("100"), Finished: true})
	if op := operation(st.refresh(), 10); op.Status != domain.OperFinished {
		t.Fatalf("operation 10 after the finishing route card: %s", op.Status)
	}
	more := NewJournal{Type: domain.JournalRouteCard, Hours: []HoursInput{{OperNumber: 10, Hours: d("0.5")}}}
	_, err := st.journal(more)
	expectProblem(t, err, http.StatusConflict, CodeOperationFinished)

	// Reopen: started again (it has feedback), the order version moves, the log and the event record it.
	before := st.version
	o, res, err := st.operate(10, domain.OperReopen, "reopen-10")
	if err != nil || !res.Changed || res.Replayed {
		t.Fatalf("reopen: %+v %v", res, err)
	}
	if operation(o, 10).Status != domain.OperStarted || o.Version != before+1 {
		t.Fatalf("after reopen: status %s version %d (was %d)", operation(o, 10).Status, o.Version, before)
	}
	st.routeCard(more.Hours...)
	if got := operation(st.refresh(), 10); got.Status != domain.OperStarted || !got.HoursReported.Equal(d("2")) {
		t.Fatalf("operation 10 after more feedback: %s %s h", got.Status, got.HoursReported)
	}

	// Finish operation 20 without a route card, then reopen it: open again, since it has no feedback.
	if o, res, err = st.operate(20, domain.OperFinish, "finish-20"); err != nil || !res.Changed || operation(o, 20).Status != domain.OperFinished {
		t.Fatalf("finish 20: %+v %s %v", res, operation(o, 20).Status, err)
	}
	// The remaining proposal leaves a finished operation out.
	proposed := st.mustJournal(NewJournal{Type: domain.JournalRouteCard})
	for _, l := range proposed.Lines {
		if l.OperNumber != nil && *l.OperNumber == 20 {
			t.Fatal("the proposal must skip finished operation 20")
		}
	}
	if err := s.DeleteJournal(ctx, proposed.ID); err != nil {
		t.Fatal(err)
	}

	// Finish again: already in effect, nothing changes and nothing is emitted.
	version := st.version
	if o, res, err = st.operate(20, domain.OperFinish, "finish-20-again"); err != nil || res.Changed || res.Replayed || o.Version != version {
		t.Fatalf("repeat finish: %+v version %d (was %d) %v", res, o.Version, version, err)
	}
	if o, res, err = st.operate(20, domain.OperReopen, "reopen-20"); err != nil || !res.Changed || operation(o, 20).Status != domain.OperOpen {
		t.Fatalf("reopen 20: %+v %s %v", res, operation(o, 20).Status, err)
	}

	// A replay of a key answers the current order and changes nothing; the key cannot serve another command.
	version = st.version
	o, res, err = s.ApplyOperationCommand(ctx, OperationCommand{OrderID: st.orderID, OperNumber: 20, Action: domain.OperFinish,
		IfMatch: 1, Actor: "supervisor-1", IdempotencyKey: "finish-20", RequestHash: "finish/20"})
	if err != nil || !res.Replayed || o.Version != version || operation(o, 20).Status != domain.OperOpen {
		t.Fatalf("replay: %+v version %d %s %v", res, o.Version, operation(o, 20).Status, err)
	}
	_, _, err = s.ApplyOperationCommand(ctx, OperationCommand{OrderID: st.orderID, OperNumber: 30, Action: domain.OperFinish,
		IfMatch: version, Actor: "supervisor-1", IdempotencyKey: "finish-20", RequestHash: "finish/30"})
	expectProblem(t, err, http.StatusUnprocessableEntity, CodeIdempotencyReuse)

	// Stale version, unknown operation.
	_, _, err = s.ApplyOperationCommand(ctx, OperationCommand{OrderID: st.orderID, OperNumber: 30, Action: domain.OperFinish,
		IfMatch: version - 1, Actor: "supervisor-1", IdempotencyKey: "stale", RequestHash: "finish/30"})
	expectProblem(t, err, http.StatusPreconditionFailed, CodeVersionConflict)
	_, _, err = st.operate(99, domain.OperFinish, "missing")
	expectProblem(t, err, http.StatusNotFound, CodeOperationNotFound)
	_, _, err = s.ApplyOperationCommand(ctx, OperationCommand{OrderID: ids.New(), OperNumber: 10, Action: domain.OperFinish,
		IfMatch: 1, Actor: "supervisor-1", IdempotencyKey: "no-order", RequestHash: "finish/10"})
	expectProblem(t, err, http.StatusNotFound, CodeOrderNotFound)

	// One event per change, in order, with the order version it produced.
	events := operationEvents(t, pool, st.orderID)
	want := []struct {
		oper     int
		action   string
		from, to domain.OperStatus
	}{
		{10, "reopen", domain.OperFinished, domain.OperStarted},
		{20, "finish", domain.OperOpen, domain.OperFinished},
		{20, "reopen", domain.OperFinished, domain.OperOpen},
	}
	if len(events) != len(want) {
		t.Fatalf("operation events: %+v", events)
	}
	for i, w := range want {
		e := events[i]
		if e.OrderID != st.orderID || e.OperNumber != w.oper || e.Action != w.action || e.From != string(w.from) || e.To != string(w.to) ||
			e.Actor != "supervisor-1" || e.OrderVersion < 1 || e.OccurredAt == "" {
			t.Errorf("event %d: %+v, want %+v", i, e, w)
		}
	}
	var logged int
	if err := pool.QueryRow(context.Background(), `select count(*) from production_operation_log where order_id = $1`, st.orderID).Scan(&logged); err != nil || logged != 3 {
		t.Fatalf("operation log rows: %d %v", logged, err)
	}
}

func TestOperationCommands_RefusedOutsideFeedback(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(42))

	// A Released order takes no operation command (its first route card would start it).
	in := routedOrder(t, "op-released", ids.New(), 10)
	allProfile(t, s, ctx, in.LegalEntityID)
	o, _, err := s.CreateOrder(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	st := &stepper{t: t, s: s, ctx: ctx, orderID: o.ID, version: o.Version}
	for _, cmd := range []domain.Command{domain.CmdEstimate, domain.CmdSchedule, domain.CmdRelease} {
		st.must(cmd, nil)
	}
	version := st.version
	_, _, err = st.operate(10, domain.OperFinish, "released-finish")
	expectProblem(t, err, http.StatusConflict, domain.CodeOperationState)
	if got := st.refresh(); got.Version != version || operation(got, 10).Status != domain.OperOpen {
		t.Fatalf("a refused command changed the order: version %d status %s", got.Version, operation(got, 10).Status)
	}

	// A subcontracted operation of a Started order.
	sub := startedOrder(t, s, ctx, subcontractOrder(t, "op-sub", ids.New(), ids.New()))
	var subOper int
	for _, op := range sub.refresh().Operations {
		if op.Subcontract {
			subOper = op.OperNumber
		}
	}
	if subOper == 0 {
		t.Fatal("the subcontract order has no subcontract operation")
	}
	_, _, err = sub.operate(subOper, domain.OperFinish, "sub-finish")
	expectProblem(t, err, http.StatusConflict, domain.CodeOperationState)
}

// PRD 4.21.13 "Estimate on create": the order is created Estimated, in one transaction.
func TestCreateOrder_EstimateOnCreate(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(43))
	in := sampleOrder(t, "estimate-on-create", ids.New())
	in.EstimateOnCreate, in.TraceID = true, "trace-create"
	o, created, err := s.CreateOrder(ctx, in)
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	if o.Status != domain.Estimated || o.Version != 2 {
		t.Fatalf("want Estimated at version 2, got %s v%d", o.Status, o.Version)
	}
	// BR-21.7: 300 kg at 10.00 and 50 h at 30.00 labor and 10.00 overhead.
	if m, l := groupSum(o.Costs, domain.GroupMaterial), groupSum(o.Costs, domain.GroupLabor); !m.Equal(d("3000")) || !l.Equal(d("1500")) {
		t.Fatalf("planned material %s labor %s", m, l)
	}
	log, err := s.StatusLog(ctx, o.ID)
	if err != nil || len(log) != 1 || log[0].Command != domain.CmdEstimate || log[0].From != domain.Created || log[0].To != domain.Estimated ||
		log[0].TraceID != "trace-create" {
		t.Fatalf("status log: %+v %v", log, err)
	}
	if types := outboxTypes(t, pool, o.ID); len(types) != 2 || types[0] != prodevents.OrderCreated || types[1] != prodevents.OrderStatusChanged {
		t.Fatalf("outbox: %v", types)
	}

	// The replay returns the estimated order and emits nothing more.
	replay, created, err := s.CreateOrder(ctx, in)
	if err != nil || created || replay.ID != o.ID || replay.Status != domain.Estimated || replay.Version != 2 {
		t.Fatalf("replay: %v %s v%d %v", created, replay.Status, replay.Version, err)
	}
	if types := outboxTypes(t, pool, o.ID); len(types) != 2 {
		t.Fatalf("outbox after the replay: %v", types)
	}

	// Without the parameter the order stays Created.
	plain, _, err := s.CreateOrder(ctx, sampleOrder(t, "no-estimate", in.LegalEntityID))
	if err != nil || plain.Status != domain.Created || plain.Version != 1 || len(plain.Costs) != 0 {
		t.Fatalf("plain create: %s v%d costs %d %v", plain.Status, plain.Version, len(plain.Costs), err)
	}
}
