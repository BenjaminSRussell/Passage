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
	return fmt.Sprintf(`{"vendorId":%q,"purchaseOrderId":%q,"legalEntityId":%q,"packingSlip":%q,"receiptDate":"2026-10-25","currency":"USD","lines":[{"poLineId":%q,"poLineNo":1,"productId":%q,"overDeliveryPct":5,"unit":"EA"%s}]}`,
		vendorID, poID, legalEntityID, slip, poLineID, productID, lineFields)
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
	if line.POLineNo != 1 || line.OrderedRemaining != "6" || line.UnitPrice != "2.5" {
		t.Fatalf("line %+v: want PO line 1, open 6 (10 ordered, 4 received), net price 2.5 per unit", line)
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
		{"fully received", poOverrides{received: "10"}, "", 0, http.StatusUnprocessableEntity, "RCV-OVR-001"},
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
	twice := strings.Replace(poArrivalJSON("PS-DUP", ""), `]}`, `,{"poLineId":"`+poLineID+`","poLineNo":1,"productId":"`+productID+`","unit":"EA"}]}`, 1)
	rec = doWith(pr, http.MethodPost, arrivalsPath, twice, maintainer("k3"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "duplicates lines[0].poLineNo") {
		t.Fatalf("one PO line twice must be refused: %d %s", rec.Code, rec.Body)
	}
}

func TestRegisterRechecksOpenQuantityAgainstPurchasing(t *testing.T) {
	r, stub := newPurchasingRouter(t, poOverrides{})
	created := doWith(r, http.MethodPost, arrivalsPath, poArrivalJSON("PS-REG", ""), maintainer("k1"))
	if created.Code != http.StatusCreated {
		t.Fatalf("arrival: %d %s", created.Code, created.Body)
	}
	var got arrivedReceipt
	_ = json.Unmarshal(created.Body.Bytes(), &got)
	stub.body.Store(purchaseOrderJSON(poOverrides{received: "8"}))
	hdr := maintainer("k2")
	hdr["If-Match"] = "1"
	body := fmt.Sprintf(`{"lines":[{"lineNo":1,"qty":3,"locationId":%q}]}`, locationID)
	rec := doWith(r, http.MethodPost, "/v1/receiving/receipts/"+got.ID+":register", body, hdr)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "RCV-OVR-001") {
		t.Fatalf("2 left open (+5%%) must refuse 3: %d %s", rec.Code, rec.Body)
	}
	hdr["Idempotency-Key"] = "k3"
	ok := doWith(r, http.MethodPost, "/v1/receiving/receipts/"+got.ID+":register", strings.Replace(body, `"qty":3`, `"qty":2`, 1), hdr)
	if ok.Code != http.StatusOK {
		t.Fatalf("2 left open must be accepted: %d %s", ok.Code, ok.Body)
	}
}
