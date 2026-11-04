package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

var savedTol struct {
	in      store.DeliveryToleranceInput
	version int
}

func (f *fakeStore) ListDeliveryTolerances(_ context.Context, productID, customerID string, limit int) ([]store.DeliveryTolerance, error) {
	p := productID
	if p == "" {
		p = ids.New()
	}
	return []store.DeliveryTolerance{{ID: ids.New(), ProductID: &p, OverdeliveryPct: decimal.RequireFromString("5"), Version: 2, UpdatedAt: time.Now()}}, nil
}

func (f *fakeStore) SaveDeliveryTolerance(_ context.Context, in store.DeliveryToleranceInput, version int, idem store.Idem) (store.DeliveryTolerance, error) {
	f.lastIdem = idem
	savedTol.in, savedTol.version = in, version
	if version != 2 {
		return store.DeliveryTolerance{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeSettingVersion, "stale")
	}
	return store.DeliveryTolerance{ID: ids.New(), ProductID: in.ProductID, CustomerID: in.CustomerID, OverdeliveryPct: in.OverdeliveryPct, Version: 3, UpdatedAt: time.Now()}, nil
}

func TestDeliveryTolerance_ListSaveValidateAndDuty(t *testing.T) {
	h := newServer(t, &fakeStore{})
	const path = "/v1/fulfillment/delivery-tolerances"
	product, customer := ids.New(), ids.New()
	if rec := do(t, h, http.MethodGet, path+"?productId="+product, "", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"overdeliveryPct":"5"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	for _, q := range []string{"?productId=x", "?customerId=x", "?limit=0"} {
		if rec := do(t, h, http.MethodGet, path+q, "", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("list %s: %d", q, rec.Code)
		}
	}
	body := `{"productId":"` + product + `","customerId":"` + customer + `","overdeliveryPct":"2.5"}`
	hdr := func(ifMatch, key string) map[string]string {
		m := map[string]string{"X-Passage-Duties": "WarehouseView,PackingRuleMaintain"}
		if ifMatch != "" {
			m["If-Match"] = ifMatch
		}
		if key != "" {
			m["Idempotency-Key"] = key
		}
		return m
	}
	if rec := do(t, h, http.MethodPut, path, body, hdr("", "t-1")); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPut, path, body, hdr(`"x"`, "t-1")); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad If-Match: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPut, path, body, hdr(`"2"`, "")); rec.Code != http.StatusBadRequest {
		t.Fatalf("no idempotency key: %d", rec.Code)
	}
	bad := map[string]struct{ body, field string }{
		"product":  {`{"productId":"nope","overdeliveryPct":"1"}`, "productId"},
		"customer": {`{"customerId":"nope","overdeliveryPct":"1"}`, "customerId"},
		"missing":  {`{}`, "overdeliveryPct"},
		"negative": {`{"overdeliveryPct":"-1"}`, "overdeliveryPct"},
		"over 100": {`{"overdeliveryPct":"100.5"}`, "overdeliveryPct"},
		"scale":    {`{"overdeliveryPct":"1.00001"}`, "overdeliveryPct"},
		"text":     {`{"overdeliveryPct":"lots"}`, "overdeliveryPct"},
	}
	for name, c := range bad {
		rec := do(t, h, http.MethodPut, path, c.body, hdr(`"2"`, "t-bad"))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.field) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := do(t, h, http.MethodPut, path, body, hdr(`"1"`, "t-2")); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale: %d", rec.Code)
	}
	rec := do(t, h, http.MethodPut, path, body, hdr(`"2"`, "t-3"))
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` || !strings.Contains(rec.Body.String(), `"overdeliveryPct":"2.5"`) {
		t.Fatalf("save: %d %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if savedTol.version != 2 || savedTol.in.ProductID == nil || *savedTol.in.ProductID != product || *savedTol.in.CustomerID != customer || !savedTol.in.OverdeliveryPct.Equal(decimal.RequireFromString("2.5")) {
		t.Fatalf("store got %+v", savedTol)
	}
	// The general scope names neither product nor customer.
	if rec := do(t, h, http.MethodPut, path, `{"overdeliveryPct":"0"}`, hdr(`"2"`, "t-4")); rec.Code != http.StatusOK || savedTol.in.ProductID != nil || savedTol.in.CustomerID != nil {
		t.Fatalf("general: %d %+v", rec.Code, savedTol)
	}
	if rec := do(t, h, http.MethodPut, path, body, map[string]string{"If-Match": `"2"`, "Idempotency-Key": "t-5", "X-Passage-Duties": "WarehouseView"}); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer save: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, path, "", map[string]string{"X-Passage-Duties": "WarehousePick"}); rec.Code != http.StatusForbidden {
		t.Fatalf("picker read: %d", rec.Code)
	}
}
