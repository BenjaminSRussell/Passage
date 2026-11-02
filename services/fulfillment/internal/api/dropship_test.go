package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

var lastDropShipIn store.DropShipInput
var lastConfirm store.DropShipConfirmInput

func (f *fakeStore) CreateDropShip(_ context.Context, in store.DropShipInput, idem store.Idem) (store.DropShip, error) {
	f.lastIdem = idem
	lastDropShipIn = in
	return store.DropShip{ID: ids.New(), Number: "DS-0000001", Status: "OPEN", Qty: in.Qty, UnitCost: in.UnitCost, Version: 1, Journal: []store.JournalEntry{}}, nil
}

func (f *fakeStore) ListDropShips(_ context.Context, fl store.DropShipFilter) ([]store.DropShip, error) {
	return []store.DropShip{{ID: ids.New(), Number: "DS-0000001", Status: fl.Status, Qty: decimal.NewFromInt(1), Version: 1, Journal: []store.JournalEntry{}}}, nil
}

func (f *fakeStore) GetDropShip(_ context.Context, id string) (store.DropShip, error) {
	return store.DropShip{ID: id, Status: "OPEN", Version: 4, Journal: []store.JournalEntry{}}, nil
}

func (f *fakeStore) ConfirmDropShip(_ context.Context, id string, version int, in store.DropShipConfirmInput, idem store.Idem) (store.DropShip, error) {
	f.lastIdem = idem
	lastConfirm = in
	return store.DropShip{ID: id, Status: "CONFIRMED", Version: version + 1, Journal: []store.JournalEntry{}}, nil
}

func (f *fakeStore) CancelDropShip(_ context.Context, id string, version int, idem store.Idem) (store.DropShip, error) {
	f.lastIdem = idem
	return store.DropShip{ID: id, Status: "CANCELLED", Version: version + 1, Journal: []store.JournalEntry{}}, nil
}

func dropShipBody(mut func(string) string) string {
	b := `{"legalEntityId":"` + ids.New() + `","orderId":"` + ids.New() + `","orderLineId":"` + ids.New() + `","customerId":"` + ids.New() +
		`","productId":"` + ids.New() + `","unit":"EA","qty":"4","vendorId":"` + ids.New() + `","purchaseOrderId":"` + ids.New() +
		`","purchaseOrderLineId":"` + ids.New() + `","unitCost":"12.50","currency":"USD"}`
	if mut != nil {
		return mut(b)
	}
	return b
}

func TestDropShip_CreateValidatesAndNeedsAKeyAndDuty(t *testing.T) {
	h := newServer(t, &fakeStore{})
	path := "/v1/fulfillment/drop-ships"
	key := map[string]string{"Idempotency-Key": "d-1"}
	if rec := do(t, h, http.MethodPost, path, dropShipBody(nil), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: %d", rec.Code)
	}
	rec := do(t, h, http.MethodPost, path, dropShipBody(nil), key)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` || !lastDropShipIn.UnitCost.Equal(decimal.RequireFromString("12.5")) {
		t.Fatalf("create: %d %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	for name, mut := range map[string]func(string) string{
		"qty zero":      func(b string) string { return strings.Replace(b, `"qty":"4"`, `"qty":"0"`, 1) },
		"negative cost": func(b string) string { return strings.Replace(b, `"unitCost":"12.50"`, `"unitCost":"-1"`, 1) },
		"zero cost":     func(b string) string { return strings.Replace(b, `"unitCost":"12.50"`, `"unitCost":"0"`, 1) },
		"qty scale":     func(b string) string { return strings.Replace(b, `"qty":"4"`, `"qty":"0.0000001"`, 1) },
		"cost scale":    func(b string) string { return strings.Replace(b, `"unitCost":"12.50"`, `"unitCost":"1.1234567"`, 1) },
		"bad currency":  func(b string) string { return strings.Replace(b, `"USD"`, `"usd"`, 1) },
		"bad vendor id": func(b string) string { return strings.Replace(b, `"vendorId":"`, `"vendorId":"x`, 1) },
		"unknown field": func(b string) string { return strings.Replace(b, `"unit":"EA"`, `"unit":"EA","extra":1`, 1) },
		"unit too long": func(b string) string { return strings.Replace(b, `"EA"`, `"EACHEACHEACH"`, 1) },
	} {
		if rec := do(t, h, http.MethodPost, path, dropShipBody(mut), map[string]string{"Idempotency-Key": "d-" + name}); rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want a validation error", name, rec.Code)
		}
	}
	if rec := do(t, h, http.MethodPost, path, dropShipBody(nil), map[string]string{"Idempotency-Key": "d-2", "X-Passage-Duties": "WarehouseView"}); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer create: %d, want 403", rec.Code)
	}
}

func TestDropShip_ConfirmNeedsVersionDateAndThePostingDuty(t *testing.T) {
	h := newServer(t, &fakeStore{})
	path := "/v1/fulfillment/drop-ships/" + ids.New() + ":confirm"
	today := time.Now().UTC().Format(dateLayout)
	body := `{"shippedOn":"` + today + `","vendorShipmentRef":"VS-1"}`
	if rec := do(t, h, http.MethodPost, path, body, map[string]string{"X-Passage-Duties": "PurchaseOrderConfirm", "Idempotency-Key": "c-1"}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, `{}`, map[string]string{"X-Passage-Duties": "PurchaseOrderConfirm", "Idempotency-Key": "c-2", "If-Match": `"1"`}); rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
		t.Fatalf("no date: %d", rec.Code)
	}
	future := time.Now().UTC().AddDate(0, 0, 3).Format(dateLayout)
	if rec := do(t, h, http.MethodPost, path, `{"shippedOn":"`+future+`"}`, map[string]string{"X-Passage-Duties": "PurchaseOrderConfirm", "Idempotency-Key": "c-3", "If-Match": `"1"`}); rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
		t.Fatalf("future date: %d", rec.Code)
	}
	rec := do(t, h, http.MethodPost, path, body, map[string]string{"X-Passage-Duties": "PurchaseOrderConfirm", "Idempotency-Key": "c-4", "If-Match": `"1"`})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` || lastConfirm.VendorShipmentRef != "VS-1" || lastConfirm.PostingDate != nil {
		t.Fatalf("confirm: %d %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	// Section 5.28.5: the vendor confirmation is a purchasing duty, not a warehouse or packing slip one.
	for _, duties := range []string{"WarehouseRelease,WarehouseView", "PackingSlipPost,WarehouseView"} {
		if rec := do(t, h, http.MethodPost, path, body, map[string]string{"Idempotency-Key": "c-5" + duties, "If-Match": `"1"`, "X-Passage-Duties": duties}); rec.Code != http.StatusForbidden {
			t.Fatalf("confirm with %s: %d, want 403 (needs PurchaseOrderConfirm)", duties, rec.Code)
		}
	}
	if rec := do(t, h, http.MethodPost, path, body, map[string]string{"Idempotency-Key": "c-6", "If-Match": `"1"`, "X-Passage-Duties": "PurchaseOrderConfirm"}); rec.Code != http.StatusOK {
		t.Fatalf("confirm with PurchaseOrderConfirm: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDropShip_ListGetCancel(t *testing.T) {
	h := newServer(t, &fakeStore{})
	base := "/v1/fulfillment/drop-ships"
	if rec := do(t, h, http.MethodGet, base+"?status=OPEN", "", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items"`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, base+"?status=DONE", "", nil); rec.Code == http.StatusOK {
		t.Fatalf("bad status accepted")
	}
	if rec := do(t, h, http.MethodGet, base+"?vendorId=nope", "", nil); rec.Code == http.StatusOK {
		t.Fatalf("bad vendor accepted")
	}
	id := ids.New()
	if rec := do(t, h, http.MethodGet, base+"/"+id, "", nil); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("get: %d %q", rec.Code, rec.Header().Get("ETag"))
	}
	if rec := do(t, h, http.MethodGet, base+"/nope", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d, want 404", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, base+"/"+id+":cancel", "", map[string]string{"Idempotency-Key": "x-1"}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("cancel without If-Match: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, base+"/"+id+":cancel", "", map[string]string{"Idempotency-Key": "x-2", "If-Match": `"4"`}); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "CANCELLED") {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
}
