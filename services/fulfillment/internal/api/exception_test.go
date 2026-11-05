package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	platformtenant "github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

var fakeExceptions []store.Exception

func (f *fakeStore) ListExceptions(_ context.Context, fl store.ExceptionFilter) ([]store.Exception, error) {
	f.exceptionFilter = fl
	return fakeExceptions, nil
}

func (f *fakeStore) GetException(_ context.Context, id string) (store.Exception, error) {
	for _, e := range fakeExceptions {
		if e.ID == id {
			return e, nil
		}
	}
	return store.Exception{}, httpx.NewProblem(http.StatusNotFound, store.CodeExceptionNotFound, "exception not found")
}

func (f *fakeStore) ResolveException(_ context.Context, id string, version int, note string, idem store.Idem) (store.Exception, error) {
	f.resolved = note
	f.resolvedVersion = version
	e, err := f.GetException(context.Background(), id)
	e.Status, e.Version = "RESOLVED", e.Version+1
	return e, err
}

func TestExceptions_ListGetResolve(t *testing.T) {
	ex := store.Exception{ID: ids.New(), LegalEntityID: ids.New(), Kind: store.ExceptionSlipPostedBeyondKept, Status: "OPEN", OrderID: ids.New(),
		OrderLineID: ids.New(), Qty: "2", KeptQty: "5", Detail: "d", Version: 1, CreatedAt: time.Now()}
	fakeExceptions = []store.Exception{ex}
	t.Cleanup(func() { fakeExceptions = nil })
	fs := &fakeStore{}
	h := newServer(t, fs)

	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/exceptions?status=OPEN&limit=20", "", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), ex.ID) || fs.exceptionFilter.Status != "OPEN" || fs.exceptionFilter.Limit != 20 {
		t.Fatalf("list: %d %s %+v", rec.Code, rec.Body.String(), fs.exceptionFilter)
	}
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/exceptions?status=DONE", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/exceptions/"+ex.ID, "", nil); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("get: %d etag %q", rec.Code, rec.Header().Get("ETag"))
	}
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/exceptions/not-a-uuid", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
	path := "/v1/fulfillment/exceptions/" + ex.ID + ":resolve"
	if rec := do(t, h, http.MethodPost, path, `{"note":"returned"}`, map[string]string{"Idempotency-Key": "r1"}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, `{"note":"returned"}`, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, `{"note":" "}`, map[string]string{"If-Match": `"1"`, "Idempotency-Key": "r2"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("blank note: %d", rec.Code)
	}
	rec := do(t, h, http.MethodPost, path, `{"note":" returned via RMA "}`, map[string]string{"If-Match": `"1"`, "Idempotency-Key": "r3"})
	if rec.Code != http.StatusOK || fs.resolved != "returned via RMA" || fs.resolvedVersion != 1 || !strings.Contains(rec.Body.String(), "RESOLVED") {
		t.Fatalf("resolve: %d %s %q", rec.Code, rec.Body.String(), fs.resolved)
	}
	// Resolving needs fulfillment.warehouse.release; viewing needs fulfillment.warehouse.view.
	rec = do(t, h, http.MethodPost, path, `{"note":"x"}`, map[string]string{"If-Match": `"1"`, "Idempotency-Key": "r4", "X-Passage-Duties": "fulfillment.warehouse.view"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("resolve without fulfillment.warehouse.release: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/v1/fulfillment/exceptions", "", map[string]string{"X-Passage-Duties": "fulfillment.warehouse.pick"}); rec.Code != http.StatusForbidden {
		t.Fatalf("list without fulfillment.warehouse.view: %d", rec.Code)
	}
}

func remainderEvent(t *testing.T) (ctx context.Context, h eventing.Handler, f *fakeOrders) {
	t.Helper()
	ctx = platformtenant.With(context.Background(), platformtenant.Principal{TenantID: testkit.TenantID(75), UserID: "service:" + OrderConsumerName, Duties: map[string]struct{}{"fulfillment.order.sync": {}}})
	f = &fakeOrders{}
	h = OrderConsumerHandlers(f)[OrderRemainderCancelledType]
	return ctx, h, f
}

// The consumer follows order.remaindercancelled.v1: it passes the kept quantity per line to the
// store, refuses malformed events and another source permanently, and retries a store error.
func TestOrderConsumer_FollowsRemainderCancelled(t *testing.T) {
	ctx, h, f := remainderEvent(t)
	order, le, line1, line2 := ids.New(), ids.New(), ids.New(), ids.New()
	good := map[string]any{"order_id": strings.ToUpper(order), "legal_entity_id": le, "version": 7, "status": "SHIPPED", "reason": "customer",
		"lines": []map[string]any{{"line_id": line1, "kept_qty": "4.5"}, {"line_id": strings.ToUpper(line2), "kept_qty": "0"}}}
	ev := orderEvent(t, good)
	ev.Type = OrderRemainderCancelledType
	if err := h.Handle(ctx, nil, ev); err != nil {
		t.Fatal(err)
	}
	if len(f.remainder) != 1 {
		t.Fatalf("calls = %d", len(f.remainder))
	}
	got := f.remainder[0]
	if got.OrderID != order || got.LegalEntityID != le || got.Version != 7 || got.EventID != "evt-1" || len(got.Lines) != 2 ||
		got.Lines[0].OrderLineID != line1 || got.Lines[0].KeptQty.String() != "4.5" || got.Lines[1].OrderLineID != line2 || !got.Lines[1].KeptQty.IsZero() {
		t.Fatalf("stored = %+v", got)
	}
	bad := map[string]map[string]any{
		"no order":      {"legal_entity_id": le, "version": 1, "lines": good["lines"]},
		"no legal ent":  {"order_id": order, "version": 1, "lines": good["lines"]},
		"no version":    {"order_id": order, "legal_entity_id": le, "lines": good["lines"]},
		"no lines":      {"order_id": order, "legal_entity_id": le, "version": 1},
		"negative kept": {"order_id": order, "legal_entity_id": le, "version": 1, "lines": []map[string]any{{"line_id": line1, "kept_qty": "-1"}}},
		"not a number":  {"order_id": order, "legal_entity_id": le, "version": 1, "lines": []map[string]any{{"line_id": line1, "kept_qty": "x"}}},
		"bad line id":   {"order_id": order, "legal_entity_id": le, "version": 1, "lines": []map[string]any{{"line_id": "1", "kept_qty": "1"}}},
	}
	for name, d := range bad {
		e := orderEvent(t, d)
		e.Type = OrderRemainderCancelledType
		if err := h.Handle(ctx, nil, e); !eventing.IsPermanent(err) {
			t.Fatalf("%s: err = %v, want permanent", name, err)
		}
	}
	foreign := ev
	foreign.Source = "purchasing"
	if err := h.Handle(ctx, nil, foreign); !eventing.IsPermanent(err) {
		t.Fatalf("foreign source: %v", err)
	}
	noDuty := platformtenant.With(context.Background(), platformtenant.Principal{TenantID: testkit.TenantID(75), UserID: "x"})
	if err := h.Handle(noDuty, nil, ev); !eventing.IsPermanent(err) {
		t.Fatalf("no duty: %v", err)
	}
	if len(f.remainder) != 1 {
		t.Fatalf("bad events reached the store: %d calls", len(f.remainder))
	}
	f.err = context.DeadlineExceeded
	if err := h.Handle(ctx, nil, ev); err == nil || eventing.IsPermanent(err) {
		t.Fatalf("store error = %v, want retryable", err)
	}
}
