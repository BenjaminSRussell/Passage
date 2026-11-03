package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	platformtenant "github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func (f *fakeStore) OrderTypes(_ context.Context, orderIDs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range orderIDs {
		if t, ok := f.orderTypes[id]; ok {
			out[id] = t
		}
	}
	return out, nil
}

func (f *fakeStore) HeldOrders(_ context.Context, orderIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range orderIDs {
		if f.heldOrders[id] {
			out[id] = true
		}
	}
	return out, nil
}

type fakeOrders struct {
	got       []store.OrderConfirmedIn
	holds     []store.OrderHoldIn
	remainder []store.RemainderCancelledIn
	err       error
}

func (f *fakeOrders) ApplyOrderRemainderCancelled(_ context.Context, _ pgx.Tx, in store.RemainderCancelledIn) (store.RemainderCancelledResult, error) {
	f.remainder = append(f.remainder, in)
	return store.RemainderCancelledResult{LinesApplied: len(in.Lines)}, f.err
}

func (f *fakeOrders) ApplyOrderHold(_ context.Context, _ pgx.Tx, in store.OrderHoldIn) (bool, error) {
	f.holds = append(f.holds, in)
	return true, f.err
}

func (f *fakeOrders) ApplyOrderConfirmed(_ context.Context, _ pgx.Tx, in store.OrderConfirmedIn) (bool, error) {
	f.got = append(f.got, in)
	return true, f.err
}

func orderEvent(t *testing.T, data map[string]any) events.Event {
	t.Helper()
	raw, _ := json.Marshal(data)
	return events.Event{ID: "evt-1", Source: OrderConfirmedSource, Type: OrderConfirmedType, TenantID: testkit.TenantID(75), Data: raw}
}

// The order consumer stores the type (ORDER when sales names none is refused, a known type is kept),
// refuses bad events permanently, dead-letters another source and retries a store error.
func TestOrderConsumer_StoresTypeAndRefusesBadEvents(t *testing.T) {
	ctx := platformtenant.With(context.Background(), platformtenant.Principal{TenantID: testkit.TenantID(75), UserID: "service:" + OrderConsumerName, Duties: map[string]struct{}{"fulfillment.order.sync": {}}})
	f := &fakeOrders{}
	h := OrderConsumerHandlers(f)[OrderConfirmedType]
	order := ids.New()
	good := map[string]any{"order_id": strings.ToUpper(order), "order_type": "JOURNAL", "version": 3, "lines": []map[string]any{{"line_id": ids.New(), "atp_qty": "2"}}}
	if err := h.Handle(ctx, nil, orderEvent(t, good)); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 1 || f.got[0].OrderID != order || f.got[0].OrderType != "JOURNAL" || f.got[0].Version != 3 || f.got[0].EventID != "evt-1" {
		t.Fatalf("stored = %+v", f.got)
	}
	for name, bad := range map[string]map[string]any{
		"no order":     {"order_type": "ORDER", "version": 1},
		"no version":   {"order_id": order, "order_type": "ORDER"},
		"unknown type": {"order_id": order, "order_type": "QUOTE", "version": 1},
		"no type":      {"order_id": order, "version": 1},
	} {
		if err := h.Handle(ctx, nil, orderEvent(t, bad)); !eventing.IsPermanent(err) {
			t.Fatalf("%s: err = %v, want a permanent error", name, err)
		}
	}
	n := len(f.got)
	foreign := orderEvent(t, good)
	foreign.Source = "purchasing"
	if err := h.Handle(ctx, nil, foreign); !eventing.IsPermanent(err) || len(f.got) != n {
		t.Fatalf("foreign source: err = %v, stored %d", err, len(f.got)-n)
	}
	noDuty := platformtenant.With(context.Background(), platformtenant.Principal{TenantID: testkit.TenantID(75), UserID: "x"})
	if err := h.Handle(noDuty, nil, orderEvent(t, good)); !eventing.IsPermanent(err) {
		t.Fatalf("no duty: %v", err)
	}
	f.err = errors.New("db down")
	if err := h.Handle(ctx, nil, orderEvent(t, good)); err == nil || eventing.IsPermanent(err) {
		t.Fatalf("store error = %v, want a retryable error", err)
	}
}

// The stored order type wins over the caller's except that JOURNAL from either side wins: a JOURNAL order
// the caller left as ORDER is skipped, a stored ORDER the caller calls JOURNAL is skipped too (the stricter
// value), and an order fulfillment does not know keeps the caller's value.
func TestRunRelease_StoredOrderTypeOverridesTheCaller(t *testing.T) {
	journalOrder, plainOrder, unknownOrder := ids.New(), ids.New(), ids.New()
	fs := &fakeStore{orderTypes: map[string]string{journalOrder: "JOURNAL", plainOrder: "ORDER"}}
	h := newServer(t, fs)
	customer, le := ids.New(), ids.New()
	with := func(order, extra string) string {
		l := relLine(order, customer, le, ids.New())
		if extra == "" {
			return l
		}
		return strings.Replace(l, `"atpOk":true}`, `"atpOk":true,`+extra+`}`, 1)
	}
	post := func(key string, lines ...string) *httpServerResult {
		rec := do(t, h, http.MethodPost, "/v1/fulfillment/releases:run", `{"warehouseId":"`+ids.New()+`","lines":[`+strings.Join(lines, ",")+`]}`, map[string]string{"Idempotency-Key": key})
		return &httpServerResult{code: rec.Code, body: rec.Body.String()}
	}
	r := post("stored-journal", with(journalOrder, ""))
	if r.code != http.StatusCreated || len(fs.released) != 0 || !strings.Contains(r.body, "journal_order") {
		t.Fatalf("stored JOURNAL: %d %s released=%v", r.code, r.body, fs.released)
	}
	r = post("stored-order-caller-journal", with(plainOrder, `"orderType":"JOURNAL"`))
	if r.code != http.StatusCreated || len(fs.released) != 0 || !strings.Contains(r.body, "journal_order") {
		t.Fatalf("stored ORDER, caller JOURNAL: %d %s released=%v", r.code, r.body, fs.released)
	}
	r = post("stored-order", with(plainOrder, `"orderType":"SUBSCRIPTION"`))
	if r.code != http.StatusCreated || len(fs.released) != 1 || fs.released[0].Lines[0].OrderType != "ORDER" {
		t.Fatalf("stored ORDER: %d %s released=%v", r.code, r.body, fs.released)
	}
	r = post("unknown", with(unknownOrder, `"orderType":"JOURNAL"`))
	if r.code != http.StatusCreated || len(fs.released) != 0 || !strings.Contains(r.body, "journal_order") {
		t.Fatalf("unknown order keeps the caller's JOURNAL: %d %s", r.code, r.body)
	}
	r = post("unknown-order", with(unknownOrder, ""))
	if r.code != http.StatusCreated || len(fs.released) != 1 {
		t.Fatalf("unknown order keeps ORDER: %d %s", r.code, r.body)
	}
}

type httpServerResult struct {
	code int
	body string
}

// Drop-ship creation refuses a stored JOURNAL order even when the caller says ORDER, and a caller's JOURNAL
// even when the stored type is ORDER.
func TestDropShip_StoredJournalOrderIsRefused(t *testing.T) {
	order := ids.New()
	fs := &fakeStore{orderTypes: map[string]string{order: "JOURNAL"}}
	h := newServer(t, fs)
	body := dropShipBody(func(b string) string {
		return strings.Replace(b, `"orderId":"`+extractOrderID(b)+`"`, `"orderId":"`+order+`"`, 1)
	})
	rec := do(t, h, http.MethodPost, "/v1/fulfillment/drop-ships", body, map[string]string{"Idempotency-Key": "ds-journal"})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodeJournalOrder) {
		t.Fatalf("stored JOURNAL drop-ship: %d %s", rec.Code, rec.Body.String())
	}
	// A caller's JOURNAL is kept over a stored ORDER.
	fs.orderTypes[order] = "ORDER"
	journal := strings.Replace(body, `"orderId":"`+order+`"`, `"orderId":"`+order+`","orderType":"JOURNAL"`, 1)
	if rec := do(t, h, http.MethodPost, "/v1/fulfillment/drop-ships", journal, map[string]string{"Idempotency-Key": "ds-caller-journal"}); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodeJournalOrder) {
		t.Fatalf("caller JOURNAL over stored ORDER: %d %s", rec.Code, rec.Body.String())
	}
	other := newServer(t, &fakeStore{})
	if rec := do(t, other, http.MethodPost, "/v1/fulfillment/drop-ships", dropShipBody(nil), map[string]string{"Idempotency-Key": "ds-plain"}); rec.Code != http.StatusCreated {
		t.Fatalf("unknown order drop-ship: %d %s", rec.Code, rec.Body.String())
	}
}

func extractOrderID(b string) string {
	const key = `"orderId":"`
	i := strings.Index(b, key) + len(key)
	return b[i : i+36]
}

// Hold events set a hold active (applied) or inactive (released) and refuse bad events permanently.
func TestOrderConsumer_StoresHolds(t *testing.T) {
	ctx := platformtenant.With(context.Background(), platformtenant.Principal{TenantID: testkit.TenantID(75), UserID: "service:" + OrderConsumerName, Duties: map[string]struct{}{"fulfillment.order.sync": {}}})
	f := &fakeOrders{}
	hs := OrderConsumerHandlers(f)
	order, hold := ids.New(), ids.New()
	data := map[string]any{"order_id": order, "hold_id": hold, "hold_type": "CREDIT", "version": 4}
	if err := hs[OrderHoldAppliedType].Handle(ctx, nil, orderEvent(t, data)); err != nil {
		t.Fatal(err)
	}
	data["version"] = 5
	if err := hs[OrderHoldReleasedType].Handle(ctx, nil, orderEvent(t, data)); err != nil {
		t.Fatal(err)
	}
	if len(f.holds) != 2 || !f.holds[0].Active || f.holds[1].Active || f.holds[1].Version != 5 || f.holds[0].HoldID != hold || f.holds[0].HoldType != "CREDIT" {
		t.Fatalf("holds = %+v", f.holds)
	}
	for name, bad := range map[string]map[string]any{
		"no hold id":   {"order_id": order, "hold_type": "CREDIT", "version": 1},
		"no hold type": {"order_id": order, "hold_id": hold, "version": 1},
		"no version":   {"order_id": order, "hold_id": hold, "hold_type": "CREDIT"},
	} {
		if err := hs[OrderHoldAppliedType].Handle(ctx, nil, orderEvent(t, bad)); !eventing.IsPermanent(err) {
			t.Fatalf("%s: %v, want permanent", name, err)
		}
	}
	foreign := orderEvent(t, data)
	foreign.Source = "billing"
	if err := hs[OrderHoldAppliedType].Handle(ctx, nil, foreign); !eventing.IsPermanent(err) || len(f.holds) != 2 {
		t.Fatalf("foreign source: %v", err)
	}
}

// An order sales holds is not released, whatever the caller says; other orders in the run are.
func TestRunRelease_ActiveHoldSkipsTheOrder(t *testing.T) {
	held, free := ids.New(), ids.New()
	fs := &fakeStore{heldOrders: map[string]bool{held: true}}
	h := newServer(t, fs)
	customer, le := ids.New(), ids.New()
	body := `{"warehouseId":"` + ids.New() + `","lines":[` + relLine(held, customer, le, ids.New()) + `,` + relLine(free, customer, le, ids.New()) + `]}`
	rec := do(t, h, http.MethodPost, "/v1/fulfillment/releases:run", body, map[string]string{"Idempotency-Key": "hold-run"})
	if rec.Code != http.StatusCreated || len(fs.released) != 1 || fs.released[0].OrderID != free || !strings.Contains(rec.Body.String(), `"held"`) {
		t.Fatalf("release with a hold: %d %s released=%v", rec.Code, rec.Body.String(), fs.released)
	}
}
