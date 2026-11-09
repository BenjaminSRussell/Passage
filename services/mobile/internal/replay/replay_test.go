package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/store"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/warehouse"
	"github.com/BenjaminSRussell/passage/services/mobile/migrations"
)

type call struct {
	op, key, user string
	params        map[string]string
}

type fakeWarehouse struct {
	calls     []call
	effects   map[string]int
	order     []string
	failEvery int
	lostEvery int
	conflicts map[string]string
	rejects   map[string]int
}

func newFake() *fakeWarehouse {
	return &fakeWarehouse{effects: map[string]int{}, conflicts: map[string]string{}, rejects: map[string]int{}}
}

func (f *fakeWarehouse) Do(ctx context.Context, op, key string, params map[string]string) (warehouse.Outcome, error) {
	p, _ := tenant.FromContext(ctx)
	f.calls = append(f.calls, call{op: op, key: key, user: p.UserID, params: params})
	n := len(f.calls)
	if f.failEvery > 0 && n%f.failEvery == 0 {
		return warehouse.Outcome{}, errors.New("connection reset")
	}
	if detail, ok := f.conflicts[key]; ok {
		return warehouse.Outcome{Status: 409, Code: "WMS-0110", Detail: detail}, nil
	}
	if status, ok := f.rejects[key]; ok {
		return warehouse.Outcome{Status: status, Code: "WMS-0001", Detail: "not allowed"}, nil
	}
	replayed := f.effects[key] > 0
	if !replayed {
		f.effects[key]++
		f.order = append(f.order, key)
	}
	if f.lostEvery > 0 && n%f.lostEvery == 0 {
		return warehouse.Outcome{}, errors.New("response lost")
	}
	return warehouse.Outcome{Status: 200, Replayed: replayed}, nil
}

func setup(t *testing.T) (Runner, *store.Store, *fakeWarehouse, context.Context) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "warehouse.work.execute")
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(app)
	fake := newFake()
	return Runner{Ledger: st, Warehouse: fake}, st, fake, ctx
}

func confirm(seq int64, line int) Item {
	return Item{Seq: seq, Command: "ConfirmWorkLine", CapturedAt: time.Now(),
		Payload: json.RawMessage(fmt.Sprintf(`{"workId":"w-1","lineNo":%d,"qty":"5"}`, line))}
}

func batch(n int) []Item {
	items := make([]Item, n)
	for i := range items {
		items[i] = confirm(int64(i+1), i+1)
	}
	return items
}

func statuses(t *testing.T, st *store.Store, ctx context.Context, device string, n int) []string {
	t.Helper()
	out := make([]string, n)
	for i := range out {
		tx, err := st.GetOffline(ctx, store.OfflineKey(device, int64(i+1)))
		if err != nil {
			out[i] = "ABSENT"
			continue
		}
		out[i] = tx.Status
	}
	return out
}

func TestAC323TwoHundredQueuedTransactionsApplyOnceInOrderDespiteFailures(t *testing.T) {
	r, st, fake, ctx := setup(t)
	fake.failEvery, fake.lostEvery = 7, 11
	items := batch(200)
	for round := 0; round < 200; round++ {
		_, _ = r.Run(ctx, "dev-ac3", "", items)
		if got := statuses(t, st, ctx, "dev-ac3", 200); got[199] == store.TxApplied {
			break
		}
	}
	for i, s := range statuses(t, st, ctx, "dev-ac3", 200) {
		if s != store.TxApplied {
			t.Fatalf("seq %d is %s, want APPLIED", i+1, s)
		}
	}
	if len(fake.order) != 200 {
		t.Fatalf("%d distinct effects, want 200", len(fake.order))
	}
	for i, key := range fake.order {
		if want := store.OfflineKey("dev-ac3", int64(i+1)); key != want {
			t.Fatalf("effect %d was %s, want %s: the queue must apply in seq order", i, key, want)
		}
	}
	for key, n := range fake.effects {
		if n != 1 {
			t.Fatalf("%s took effect %d times", key, n)
		}
	}
	for _, c := range fake.calls {
		if c.user != "user-test" || c.op != "confirm" {
			t.Fatalf("call %+v must act as the transaction's user with the mapped op", c)
		}
	}
	fake.failEvery, fake.lostEvery = 0, 0
	before := len(fake.calls)
	res, err := r.Run(ctx, "dev-ac3", "", items)
	if err != nil || len(res) != 200 || len(fake.calls) != before {
		t.Fatalf("a replay of an applied queue must be a no-op: %d results, %d new calls, %v", len(res), len(fake.calls)-before, err)
	}
}

func TestAC326ConflictParksAndBlocksUntilSupervisorResolves(t *testing.T) {
	for _, action := range []string{store.ResolveReject, store.ResolveReapply} {
		t.Run(action, func(t *testing.T) {
			r, st, fake, ctx := setup(t)
			fake.conflicts[store.OfflineKey("dev-ac6", 3)] = "line 3 was CANCELLED by alice"
			items := batch(5)
			res, err := r.Run(ctx, "dev-ac6", "", items)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{store.TxApplied, store.TxApplied, store.TxConflict, "PENDING", "PENDING"}
			for i, s := range statuses(t, st, ctx, "dev-ac6", 5) {
				if s != want[i] || res[i].Status != want[i] {
					t.Fatalf("seq %d: stored %s result %s, want %s", i+1, s, res[i].Status, want[i])
				}
			}
			if res[2].Reason != "line 3 was CANCELLED by alice" {
				t.Fatalf("conflict reason: %q", res[2].Reason)
			}
			calls := len(fake.calls)
			if _, err := r.Run(ctx, "dev-ac6", "", items); err != nil || len(fake.calls) != calls {
				t.Fatalf("an unresolved conflict blocks every later transaction: %d new calls, %v", len(fake.calls)-calls, err)
			}
			queue, _ := st.ListConflicts(ctx, time.Time{}, "", 10)
			if len(queue) != 1 || queue[0].Seq != 3 {
				t.Fatalf("supervisor queue: %+v", queue)
			}
			sup := testkit.Ctx(testkit.TenantID(1), "warehouse.offline-conflict.resolve")
			if _, _, err := st.Resolve(sup, store.Idem{Key: "res-" + action, Hash: action}, queue[0].IdempotencyKey, action, queue[0].Version); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Run(ctx, "dev-ac6", "", items); err != nil {
				t.Fatal(err)
			}
			tail := statuses(t, st, ctx, "dev-ac6", 5)
			if tail[3] != store.TxApplied || tail[4] != store.TxApplied {
				t.Fatalf("resolving unblocks the rest of the queue: %v", tail)
			}
		})
	}
}

func TestRejectedTransactionStopsTheRunWithItsReason(t *testing.T) {
	r, st, fake, ctx := setup(t)
	fake.rejects[store.OfflineKey("dev-rej", 2)] = 422
	res, err := r.Run(ctx, "dev-rej", "", batch(3))
	if err != nil {
		t.Fatal(err)
	}
	if res[1].Status != store.TxRejected || res[1].Reason != "WMS-0001: not allowed" || res[2].Status != "PENDING" {
		t.Fatalf("results: %+v", res)
	}
	if got := statuses(t, st, ctx, "dev-rej", 3); got[0] != store.TxApplied || got[1] != store.TxRejected {
		t.Fatalf("stored: %v", got)
	}
	if queue, _ := st.ListConflicts(ctx, time.Time{}, "", 10); len(queue) != 0 {
		t.Fatalf("a rejection is not a supervisor conflict: %+v", queue)
	}
}

func TestTransportFailureLeavesTheRestPending(t *testing.T) {
	r, st, fake, ctx := setup(t)
	fake.failEvery = 2
	res, err := r.Run(ctx, "dev-net", "", batch(3))
	if err == nil {
		t.Fatal("a warehouse outage must be reported so the caller answers 503")
	}
	if len(res) != 3 || res[0].Status != store.TxApplied || res[1].Status != "PENDING" || res[2].Status != "PENDING" {
		t.Fatalf("results: %+v", res)
	}
	if got := statuses(t, st, ctx, "dev-net", 3); got[1] != "PENDING" {
		t.Fatalf("stored: %v", got)
	}
}

func TestSequenceGapIsRefusedWithMOB0108(t *testing.T) {
	r, st, _, ctx := setup(t)
	res, err := r.Run(ctx, "dev-gap", "", []Item{confirm(1, 1), confirm(3, 3)})
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Status != store.TxApplied || res[1].Status != StatusGap || res[1].Code != CodeGap {
		t.Fatalf("results: %+v", res)
	}
	if _, err := st.GetOffline(ctx, store.OfflineKey("dev-gap", 3)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a gapped transaction is not stored: %v", err)
	}
}

func TestReusedKeyWithAnotherPayloadIsRefused(t *testing.T) {
	r, _, _, ctx := setup(t)
	if _, err := r.Run(ctx, "dev-reuse", "", []Item{confirm(1, 1)}); err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(ctx, "dev-reuse", "", []Item{confirm(1, 9)})
	if err != nil || res[0].Status != StatusReused {
		t.Fatalf("results: %+v %v", res, err)
	}
}

func TestClockSkewBlocksReplayWithMOB0114(t *testing.T) {
	r, st, fake, ctx := setup(t)
	now := time.Now()
	if _, _, err := st.StartSession(ctx, store.StartInput{DeviceID: "dev-skew", WarehouseID: "5e5510a0-0000-4000-8000-0000000000a1", MenuKey: "default", Now: now, Idle: 30 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	behind := now.Add(-6 * time.Minute)
	sessions, _ := st.ListSessions(ctx, "ACTIVE", "", 5)
	if _, _, err := st.Heartbeat(ctx, sessions[0].SessionID, now, &behind); err != nil {
		t.Fatal(err)
	}
	var skew *SkewError
	if _, err := r.Run(ctx, "dev-skew", "", batch(2)); !errors.As(err, &skew) || skew.Skew != 6*time.Minute {
		t.Fatalf("want MOB-0114 skew error of 6 minutes, got %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("nothing may be applied while the clock is off: %d calls", len(fake.calls))
	}
	if _, err := st.GetOffline(ctx, store.OfflineKey("dev-skew", 1)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("nothing may be recorded either: %v", err)
	}
}

func TestUnmappedCommandIsRejectedWithMOB0118(t *testing.T) {
	r, _, fake, ctx := setup(t)
	res, err := r.Run(ctx, "dev-unmapped", "", []Item{{Seq: 1, Command: "DeleteEverything", Payload: json.RawMessage(`{}`), CapturedAt: time.Now()}})
	if err != nil || res[0].Status != store.TxRejected || len(fake.calls) != 0 {
		t.Fatalf("results: %+v %v calls=%d", res, err, len(fake.calls))
	}
	if want := CodeUnmapped; len(res[0].Reason) < len(want) || res[0].Reason[:len(want)] != want {
		t.Fatalf("reason: %q", res[0].Reason)
	}
}

func TestBatchOverTheCapIsRefused(t *testing.T) {
	r, _, _, ctx := setup(t)
	if _, err := r.Run(ctx, "dev-big", "", make([]Item, MaxBatch+1)); !errors.Is(err, ErrBatchTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func TestItemsAreAppliedInSeqOrderWhateverTheArrivalOrder(t *testing.T) {
	r, _, fake, ctx := setup(t)
	items := batch(4)
	items[0], items[3] = items[3], items[0]
	items[1], items[2] = items[2], items[1]
	if _, err := r.Run(ctx, "dev-order", "", items); err != nil {
		t.Fatal(err)
	}
	for i, key := range fake.order {
		if key != store.OfflineKey("dev-order", int64(i+1)) {
			t.Fatalf("order: %v", fake.order)
		}
	}
}

func TestMapAcceptsTopLevelValuesAndFieldsPayloads(t *testing.T) {
	cases := []struct {
		name, command, payload, op string
		want                       map[string]string
	}{
		{"top level confirm", "ConfirmWorkLine", `{"workId":"w1","lineNo":2,"qty":"3"}`, "confirm", map[string]string{"workId": "w1", "lineNo": "2", "qty": "3"}},
		{"flow runner values", "ConfirmWorkLine", `{"values":{"work_id":"w1","line_no":"2","enter_qty":4}}`, "confirm", map[string]string{"workId": "w1", "lineNo": "2", "qty": "4"}},
		{"web fields", "ReceiveLine", `{"fields":{"scan_receipt":"r9","enter_line":1,"enter_qty":"12"}}`, "receive", map[string]string{"receiptId": "r9", "lineNo": "1", "qty": "12"}},
		{"exception", "ReportException", `{"workId":"w1","action":"SHORT","reason":"damaged","lineNo":3,"qty":"1"}`, "exception", map[string]string{"workId": "w1", "action": "SHORT", "reason": "damaged", "lineNo": "3", "qty": "1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			op, params, err := Map(c.command, json.RawMessage(c.payload))
			if err != nil || op != c.op {
				t.Fatalf("op %q err %v", op, err)
			}
			for k, v := range c.want {
				if params[k] != v {
					t.Errorf("param %s = %q, want %q", k, params[k], v)
				}
			}
		})
	}
}

func TestMapRefusesWhatCannotBeReplayed(t *testing.T) {
	for name, c := range map[string]struct{ command, payload string }{
		"unknown command": {"Nope", `{}`},
		"not an object":   {"ConfirmWorkLine", `[1]`},
		"missing qty":     {"ConfirmWorkLine", `{"workId":"w","lineNo":1}`},
		"missing receipt": {"ReceiveLine", `{"lineNo":1,"qty":1}`},
		"missing reason":  {"ReportException", `{"workId":"w","action":"SHORT"}`},
	} {
		if _, _, err := Map(c.command, json.RawMessage(c.payload)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
