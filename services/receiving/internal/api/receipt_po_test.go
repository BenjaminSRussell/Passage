package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/purchasing"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

type poOverrides struct {
	legalEntity, vendor, currency, product, unit, approval, status string
	qty, received                                                  string
	extraLine                                                      string
}

func purchaseOrderJSON(o poOverrides) string {
	pick := func(v, def string) string {
		if v == "" {
			return def
		}
		return v
	}
	return fmt.Sprintf(`{"id":%q,"legalEntityId":%q,"vendorId":%q,"currency":%q,"approvalStatus":%q,"documentStatus":"NONE","lines":[{"lineNo":1,"productId":%q,"unit":%q,"qty":%q,"unitPrice":"250.00","priceUnit":"100","discountPct":"0","receivedQty":%q,"invoicedQty":"0","status":%q}%s]}`,
		poID, pick(o.legalEntity, legalEntityID), pick(o.vendor, vendorID), pick(o.currency, "USD"), pick(o.approval, "APPROVED"),
		pick(o.product, productID), pick(o.unit, "EA"), pick(o.qty, "10"), pick(o.received, "4"), pick(o.status, "OPEN"), o.extraLine)
}

type purchasingStub struct {
	body   atomic.Value
	status atomic.Int64
	paths  atomic.Value
}

func newPurchasingRouter(t *testing.T, o poOverrides) (*chi.Mux, *purchasingStub) {
	t.Helper()
	stub := &purchasingStub{}
	stub.body.Store(purchaseOrderJSON(o))
	stub.status.Store(http.StatusOK)
	stub.paths.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.paths.Store(r.URL.Path)
		w.WriteHeader(int(stub.status.Load()))
		_, _ = w.Write([]byte(stub.body.Load().(string)))
	}))
	t.Cleanup(srv.Close)
	a := api.New(memstore.New(), api.Ports{
		Periods: domain.FakePeriods{Status: domain.PeriodOpen}, Ledger: &domain.FakeLedger{}, PurchaseOrders: purchasing.New(srv.URL, nil),
	})
	r := httpx.NewRouter()
	a.Register(r)
	return r, stub
}

func poArrivalJSON(slip, lineFields string) string {
	return fmt.Sprintf(`{"vendorId":%q,"purchaseOrderId":%q,"legalEntityId":%q,"packingSlip":%q,"receiptDate":"2026-10-25","currency":"USD","lines":[{"poLineNo":1,"productId":%q,"overDeliveryPct":5,"unit":"EA"%s}]}`,
		vendorID, poID, legalEntityID, slip, productID, lineFields)
}

type arrivedReceipt struct {
	ID    string `json:"id"`
	Lines []struct {
		POLineNo         int    `json:"poLineNo"`
		OrderedRemaining string `json:"orderedRemaining"`
		UnitPrice        string `json:"unitPrice"`
	} `json:"lines"`
}

func TestArrivalWithPOLineNo_TakesPriceAndOpenQuantityFromPurchasing(t *testing.T) {
	r, stub := newPurchasingRouter(t, poOverrides{})
	rec := doWith(r, http.MethodPost, arrivalsPath, poArrivalJSON("PS-PO", ""), maintainer("k1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	var got arrivedReceipt
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	line := got.Lines[0]
	if line.POLineNo != 1 || line.OrderedRemaining != "10" || line.UnitPrice != "2.5" {
		t.Fatalf("line %+v: want PO line 1, open 10 (nothing received here yet), net price 2.5 per unit", line)
	}
	if stub.paths.Load() != "/v1/procurement/purchase-orders/"+poID {
		t.Fatalf("purchasing was asked for %v", stub.paths.Load())
	}
	read := doWith(r, http.MethodGet, "/v1/receiving/receipts/"+got.ID, "", maintainer("k2"))
	if !strings.Contains(read.Body.String(), `"poLineNo":1`) {
		t.Fatalf("PO line number not persisted: %s", read.Body)
	}
}

func TestArrivalWithPOLineNo_RefusesWhatThePurchaseOrderContradicts(t *testing.T) {
	tests := []struct {
		name       string
		po         poOverrides
		lineFields string
		upstream   int
		wantStatus int
		wantCode   string
	}{
		{"other legal entity", poOverrides{legalEntity: "99999999-9999-4999-8999-999999999990"}, "", 0, http.StatusForbidden, "AUTH-0003"},
		{"other vendor", poOverrides{vendor: "99999999-9999-4999-8999-999999999991"}, "", 0, http.StatusUnprocessableEntity, "RCV-PO-004"},
		{"other currency", poOverrides{currency: "EUR"}, "", 0, http.StatusUnprocessableEntity, "RCV-PO-005"},
		{"product mismatch", poOverrides{product: "99999999-9999-4999-8999-999999999992"}, "", 0, http.StatusBadRequest, "COM-0001"},
		{"unit mismatch", poOverrides{unit: "KG"}, "", 0, http.StatusBadRequest, "COM-0001"},
		{"cancelled line", poOverrides{status: "CANCELLED"}, "", 0, http.StatusUnprocessableEntity, "RCV-PO-002"},
		{"caller poLineId", poOverrides{}, `,"poLineId":"` + poLineID + `"`, 0, http.StatusBadRequest, "COM-0001"},
		{"order not approved", poOverrides{approval: "DRAFT"}, "", 0, http.StatusUnprocessableEntity, "RCV-PO-001"},
		{"unknown order", poOverrides{}, "", http.StatusNotFound, http.StatusNotFound, "RCV-NF-004"},
		{"purchasing down", poOverrides{}, "", http.StatusInternalServerError, http.StatusServiceUnavailable, "COM-0503"},
		{"caller price", poOverrides{}, `,"unitPrice":1`, 0, http.StatusBadRequest, "COM-0001"},
		{"caller open quantity", poOverrides{}, `,"orderedRemaining":99`, 0, http.StatusBadRequest, "COM-0001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, stub := newPurchasingRouter(t, tc.po)
			if tc.upstream != 0 {
				stub.status.Store(int64(tc.upstream))
				stub.body.Store(`{}`)
			}
			rec := doWith(r, http.MethodPost, arrivalsPath, poArrivalJSON("PS-X", tc.lineFields), maintainer("k1"))
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantCode) {
				t.Fatalf("got %d %s, want %d %s", rec.Code, rec.Body, tc.wantStatus, tc.wantCode)
			}
		})
	}
}

func TestArrivalWithPOLineNo_NeedsLookupAndAllLinesNamed(t *testing.T) {
	r, _ := newRouter(memstore.New())
	rec := doWith(r, http.MethodPost, arrivalsPath, poArrivalJSON("PS-NOLOOKUP", ""), maintainer("k1"))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "COM-0503") {
		t.Fatalf("no lookup configured must not accept an unvalidated PO line: %d %s", rec.Code, rec.Body)
	}
	pr, _ := newPurchasingRouter(t, poOverrides{})
	mixed := strings.Replace(poArrivalJSON("PS-MIX", ""), `]}`, `,{"poLineId":"`+poLineID+`","productId":"`+productID+`","orderedRemaining":1,"unitPrice":1,"unit":"EA"}]}`, 1)
	rec = doWith(pr, http.MethodPost, arrivalsPath, mixed, maintainer("k2"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "lines[1].poLineNo") {
		t.Fatalf("a line without poLineNo beside one with it must be refused: %d %s", rec.Code, rec.Body)
	}
	twice := strings.Replace(poArrivalJSON("PS-DUP", ""), `]}`, `,{"poLineNo":1,"productId":"`+productID+`","unit":"EA"}]}`, 1)
	rec = doWith(pr, http.MethodPost, arrivalsPath, twice, maintainer("k3"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "duplicates lines[0].poLineNo") {
		t.Fatalf("one PO line twice must be refused: %d %s", rec.Code, rec.Body)
	}
}

func directPOBody(slip string, qty int, lineExtra string) string {
	return fmt.Sprintf(`{"vendorId":%q,"purchaseOrderId":%q,"legalEntityId":%q,"packingSlip":%q,"receiptDate":"2026-10-25","currency":"USD","lines":[{"poLineNo":1,"productId":%q,"overDeliveryPct":5,"unit":"EA","qty":%d,"locationId":%q%s}]}`,
		vendorID, poID, legalEntityID, slip, productID, qty, locationID, lineExtra)
}

func poRegisterBody(qty int) string {
	return fmt.Sprintf(`{"lines":[{"lineNo":1,"qty":%d,"locationId":%q}]}`, qty, locationID)
}

func registerCall(r http.Handler, id, key string, qty int) *httptest.ResponseRecorder {
	hdr := maintainer(key)
	hdr["If-Match"] = "1"
	return doWith(r, http.MethodPost, "/v1/receiving/receipts/"+id+":register", poRegisterBody(qty), hdr)
}

func arriveOne(t *testing.T, r http.Handler, slip, key string) string {
	t.Helper()
	rec := doWith(r, http.MethodPost, arrivalsPath, poArrivalJSON(slip, ""), maintainer(key))
	if rec.Code != http.StatusCreated {
		t.Fatalf("arrival %s: %d %s", slip, rec.Code, rec.Body)
	}
	var got arrivedReceipt
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	return got.ID
}

func TestRegister_CountsWhatOtherReceiptsAlreadyTookFromThePOLine(t *testing.T) {
	r, _ := newPurchasingRouter(t, poOverrides{received: "9"})
	first, second := arriveOne(t, r, "PS-A", "k1"), arriveOne(t, r, "PS-B", "k2")

	if rec := registerCall(r, first, "k3", 8); rec.Code != http.StatusOK {
		t.Fatalf("8 of 10 must be accepted: %d %s", rec.Code, rec.Body)
	}
	rec := registerCall(r, second, "k4", 5)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "RCV-OVR-001") {
		t.Fatalf("a second arrival must not take more than the 2 (+5%%) the first receipt left: %d %s", rec.Code, rec.Body)
	}
	if rec := registerCall(r, second, "k5", 2); rec.Code != http.StatusOK {
		t.Fatalf("the remaining 2 must be accepted: %d %s", rec.Code, rec.Body)
	}
	late := doWith(r, http.MethodPost, arrivalsPath, poArrivalJSON("PS-C", ""), maintainer("k6"))
	if late.Code != http.StatusUnprocessableEntity || !strings.Contains(late.Body.String(), "RCV-OVR-001") {
		t.Fatalf("a fully received PO line has no open quantity: %d %s", late.Code, late.Body)
	}
}

func TestDirectReceiptWithPOLineNo_ChecksTheOpenQuantityUnderTheLine(t *testing.T) {
	r, _ := newPurchasingRouter(t, poOverrides{})
	if rec := doWith(r, http.MethodPost, receiptsPath, directPOBody("PS-1", 8, ""), maintainer("k1")); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"poLineNo":1`) {
		t.Fatalf("direct receipt: %d %s", rec.Code, rec.Body)
	}
	rec := doWith(r, http.MethodPost, receiptsPath, directPOBody("PS-2", 5, ""), maintainer("k2"))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "RCV-OVR-001") {
		t.Fatalf("5 more than the 2 left: %d %s", rec.Code, rec.Body)
	}
}

func TestRegister_RefusesWhatChangedOnThePurchaseOrderSinceArrival(t *testing.T) {
	tests := []struct {
		name string
		po   poOverrides
		code string
	}{
		{"line cancelled", poOverrides{status: "CANCELLED"}, "RCV-PO-002"},
		{"product changed", poOverrides{product: "99999999-9999-4999-8999-999999999992"}, "RCV-PO-007"},
		{"unit changed", poOverrides{unit: "KG"}, "RCV-PO-007"},
		{"line removed", poOverrides{extraLine: ""}, "RCV-PO-006"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, stub := newPurchasingRouter(t, poOverrides{})
			id := arriveOne(t, r, "PS-X", "k1")
			body := purchaseOrderJSON(tc.po)
			if tc.code == "RCV-PO-006" {
				body = strings.Replace(body, `"lineNo":1`, `"lineNo":2`, 1)
			}
			stub.body.Store(body)
			rec := registerCall(r, id, "k2", 2)
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), tc.code) {
				t.Fatalf("got %d %s, want %s", rec.Code, rec.Body, tc.code)
			}
		})
	}
}

func TestArrivalWithPOLineNo_RefusesAPurchaseOrderOfAnotherLegalEntity(t *testing.T) {
	po := domain.PurchaseOrder{ID: poID, VendorID: vendorID, Currency: "USD", LegalEntityID: "99999999-9999-4999-8999-999999999990", ApprovalStatus: "APPROVED",
		Lines: []domain.PurchaseOrderLine{{LineNo: 1, ProductID: productID, Unit: "EA", Qty: dec("10"), UnitPrice: dec("2.5"), PriceUnit: dec("1"), Status: "OPEN"}}}
	a := api.New(memstore.New(), api.Ports{Periods: domain.FakePeriods{Status: domain.PeriodOpen}, Ledger: &domain.FakeLedger{},
		PurchaseOrders: domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: po}}})
	r := httpx.NewRouter()
	a.Register(r)
	rec := doWith(r, http.MethodPost, arrivalsPath, poArrivalJSON("PS-LE", ""), maintainer("k1"))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "RCV-PO-003") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestArrival_RequiresPOLineNoWhenTheLookupIsConfiguredForReal(t *testing.T) {
	a := api.New(memstore.New(), api.Ports{Periods: domain.FakePeriods{Status: domain.PeriodOpen}, Ledger: &domain.FakeLedger{},
		PurchaseOrders: domain.FakePurchaseOrders{}, RequirePOLine: true})
	r := httpx.NewRouter()
	a.Register(r)
	rec := doWith(r, http.MethodPost, arrivalsPath, arrivalJSON("PS-LEGACY", ""), maintainer("k1"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "lines[0].poLineNo") {
		t.Fatalf("a legacy line must be refused: %d %s", rec.Code, rec.Body)
	}
	direct := doWith(r, http.MethodPost, receiptsPath, directBody("PS-LEGACY-2", 10, 4, ""), maintainer("k2"))
	if direct.Code != http.StatusBadRequest || !strings.Contains(direct.Body.String(), "lines[0].poLineNo") {
		t.Fatalf("a legacy direct line must be refused: %d %s", direct.Code, direct.Body)
	}
}

func TestCorrect_UpwardCorrectionCountsWhatOtherReceiptsTookFromThePOLine(t *testing.T) {
	r, _ := newPurchasingRouter(t, poOverrides{})
	first, second := arriveOne(t, r, "PS-A", "k1"), arriveOne(t, r, "PS-B", "k2")
	if rec := registerCall(r, first, "k3", 4); rec.Code != http.StatusOK {
		t.Fatalf("register A: %d %s", rec.Code, rec.Body)
	}
	if rec := registerCall(r, second, "k4", 6); rec.Code != http.StatusOK {
		t.Fatalf("register B: %d %s", rec.Code, rec.Body)
	}
	postHdr := func(key string) headers {
		return headers{"X-Passage-Tenant": maintainer(key)["X-Passage-Tenant"], "X-Passage-Duties": "ReceivingPost", "Idempotency-Key": key, "If-Match": "2"}
	}
	if rec := doWith(r, http.MethodPost, "/v1/receiving/receipts/"+first+":post", "", postHdr("k5")); rec.Code != http.StatusOK {
		t.Fatalf("post A: %d %s", rec.Code, rec.Body)
	}
	correct := func(key string, delta string) *httptest.ResponseRecorder {
		hdr := postHdr(key)
		hdr["If-Match"] = "3"
		return doWith(r, http.MethodPost, "/v1/receiving/receipts/"+first+":correct", `{"lines":[{"lineNo":1,"qtyDelta":`+delta+`}]}`, hdr)
	}

	refused := correct("k6", "6")

	if refused.Code != http.StatusUnprocessableEntity || !strings.Contains(refused.Body.String(), "RCV-OVR-001") {
		t.Fatalf("+6 on A would take the PO line to 12 of 10: %d %s", refused.Code, refused.Body)
	}
	if ok := correct("k7", "0.2"); ok.Code != http.StatusOK {
		t.Fatalf("+0.2 stays inside the 5%% tolerance left by B: %d %s", ok.Code, ok.Body)
	}
}
