package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

const (
	arrivalsPath  = "/v1/receiving/arrivals"
	vendorID      = "11111111-1111-4111-8111-111111111111"
	poID          = "22222222-2222-4222-8222-222222222222"
	legalEntityID = "33333333-3333-4333-8333-333333333333"
	productID     = "44444444-4444-4444-8444-444444444444"
	poLineID      = "55555555-5555-4555-8555-555555555555"
)

type headers map[string]string

func doWith(h http.Handler, method, path, body string, hdr headers) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func maintainer(key string) headers {
	return headers{"X-Passage-Tenant": testkit.TenantID(1), "X-Passage-Duties": "ReceivingMaintain", "Idempotency-Key": key}
}

func arrivalJSON(slip string, lineFields string) string {
	return fmt.Sprintf(`{"vendorId":%q,"purchaseOrderId":%q,"legalEntityId":%q,"packingSlip":%q,"receiptDate":"2026-10-25","currency":"USD","lines":[{"poLineId":%q,"productId":%q,"orderedRemaining":10,"overDeliveryPct":5,"unitPrice":2.5,"unit":"EA"%s}]}`,
		vendorID, poID, legalEntityID, slip, poLineID, productID, lineFields)
}

type createErrStore struct {
	*memstore.Store
	err error
}

func (s createErrStore) CreateArrival(context.Context, store.Receipt) (store.Receipt, error) {
	return store.Receipt{}, s.err
}

func TestArrival_CreatesReceiptResource(t *testing.T) {
	r, _ := newRouter(memstore.New())
	rec := doWith(r, http.MethodPost, arrivalsPath, arrivalJSON("PS-1", `,"serialTracked":true`), maintainer("k1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		ID            string `json:"id"`
		ReceiptNumber string `json:"receiptNumber"`
		State         string `json:"state"`
		ReceiptDate   string `json:"receiptDate"`
		ExchangeRate  string `json:"exchangeRate"`
		Lines         []struct {
			LineNo    int      `json:"lineNo"`
			UnitPrice string   `json:"unitPrice"`
			Serials   []string `json:"serials"`
			Amount    *string  `json:"amount"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^RCV-\d{8}$`).MatchString(got.ReceiptNumber) || got.State != "ARRIVED" || got.ReceiptDate != "2026-10-25" || got.ExchangeRate != "1" {
		t.Fatalf("unexpected resource %+v", got)
	}
	if len(got.Lines) != 1 || got.Lines[0].LineNo != 1 || got.Lines[0].UnitPrice != "2.5" || got.Lines[0].Serials == nil || got.Lines[0].Amount != nil {
		t.Fatalf("unexpected line %+v", got.Lines)
	}
	if !strings.Contains(rec.Body.String(), `"serials":[]`) {
		t.Fatalf("serials must serialise as an empty array: %s", rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/v1/receiving/receipts/"+got.ID {
		t.Fatalf("Location %q", loc)
	}
}

func TestArrival_DuplicatePackingSlipNamesFirstReceipt(t *testing.T) {
	r, _ := newRouter(memstore.New())
	first := doWith(r, http.MethodPost, arrivalsPath, arrivalJSON("PS-1", ""), maintainer("k1"))
	second := doWith(r, http.MethodPost, arrivalsPath, arrivalJSON("PS-1", ""), maintainer("k2"))
	var created struct {
		ReceiptNumber string `json:"receiptNumber"`
	}
	_ = json.Unmarshal(first.Body.Bytes(), &created)
	if second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "RCV-DUP-001") || !strings.Contains(second.Body.String(), created.ReceiptNumber) {
		t.Fatalf("got %d %s", second.Code, second.Body.String())
	}
}

func TestArrival_AuthAndScope(t *testing.T) {
	r, _ := newRouter(memstore.New())
	tests := []struct {
		name       string
		hdr        headers
		wantStatus int
		wantCode   string
	}{
		{"no tenant", headers{"Idempotency-Key": "k"}, http.StatusUnauthorized, "AUTH-0001"},
		{"missing duty", headers{"X-Passage-Tenant": testkit.TenantID(1), "X-Passage-Duties": "APInquire", "Idempotency-Key": "k"}, http.StatusForbidden, "AUTH-0003"},
		{"other legal entity", headers{"X-Passage-Tenant": testkit.TenantID(1), "X-Passage-Duties": "ReceivingMaintain", "X-Passage-Legal-Entity": "66666666-6666-4666-8666-666666666666", "Idempotency-Key": "k"}, http.StatusForbidden, "AUTH-0003"},
		{"same legal entity", headers{"X-Passage-Tenant": testkit.TenantID(1), "X-Passage-Duties": "ReceivingMaintain", "X-Passage-Legal-Entity": legalEntityID, "Idempotency-Key": "k"}, http.StatusCreated, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := doWith(r, http.MethodPost, arrivalsPath, arrivalJSON("PS-"+tc.name, ""), tc.hdr)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantCode) {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestArrival_FieldErrors(t *testing.T) {
	validLine := fmt.Sprintf(`{"poLineId":%q,"productId":%q,"orderedRemaining":10,"unitPrice":1,"unit":"EA"}`, poLineID, productID)
	header := func(patch string) string {
		return fmt.Sprintf(`{"vendorId":%q,"purchaseOrderId":%q,"legalEntityId":%q,"packingSlip":"PS","receiptDate":"2026-10-25","currency":"USD",%s"lines":[%s]}`, vendorID, poID, legalEntityID, patch, validLine)
	}
	withLine := func(fields string) string {
		return strings.Replace(header(""), validLine, fmt.Sprintf(`{"poLineId":%q,"productId":%q,"unit":"EA",%s}`, poLineID, productID, fields), 1)
	}
	tests := []struct {
		name  string
		body  string
		field string
	}{
		{"501 lines", strings.Replace(header(""), "["+validLine+"]", "["+strings.TrimSuffix(strings.Repeat(validLine+",", 501), ",")+"]", 1), `"lines"`},
		{"empty lines", strings.Replace(header(""), "["+validLine+"]", "[]", 1), `"lines"`},
		{"missing packing slip", strings.Replace(header(""), `"packingSlip":"PS",`, "", 1), `"packingSlip"`},
		{"lower-case currency", strings.Replace(header(""), `"USD"`, `"usd"`, 1), `"currency"`},
		{"non-UUID vendor", strings.Replace(header(""), vendorID, "not-a-uuid", 1), `"vendorId"`},
		{"zero exchange rate", header(`"exchangeRate":0,`), `"exchangeRate"`},
		{"bad date", strings.Replace(header(""), "2026-10-25", "2026-13-40", 1), `"receiptDate"`},
		{"negative pallets", header(`"palletCount":-1,`), `"palletCount"`},
		{"fractional pallets", header(`"palletCount":1.5,`), "COM-0001"},
		{"long carrier", header(`"carrier":"` + strings.Repeat("c", 129) + `",`), `"carrier"`},
		{"string unit price", withLine(`"orderedRemaining":1,"unitPrice":"2"`), `"lines[0].unitPrice"`},
		{"missing unit price", withLine(`"orderedRemaining":1`), `"lines[0].unitPrice"`},
		{"zero ordered remaining", withLine(`"orderedRemaining":0,"unitPrice":1`), `"lines[0].orderedRemaining"`},
		{"over-delivery 101", withLine(`"orderedRemaining":1,"unitPrice":1,"overDeliveryPct":101`), `"lines[0].overDeliveryPct"`},
		{"negative over-delivery", withLine(`"orderedRemaining":1,"unitPrice":1,"overDeliveryPct":-1`), `"lines[0].overDeliveryPct"`},
		{"negative unit price", withLine(`"orderedRemaining":1,"unitPrice":-1`), `"lines[0].unitPrice"`},
		{"unit too long", withLine(`"orderedRemaining":1,"unitPrice":1,"unit":"` + strings.Repeat("u", 17) + `"`), `"lines[0].unit"`},
		{"unknown field", header(`"extra":1,`), "COM-0001"},
		{"malformed json", `{`, "COM-0001"},
		{"null lines", strings.Replace(header(""), "["+validLine+"]", "null", 1), `"lines"`},
		{"object lines", strings.Replace(header(""), "["+validLine+"]", "{}", 1), "COM-0001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(memstore.New())
			rec := doWith(r, http.MethodPost, arrivalsPath, tc.body, maintainer("k"))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") || !strings.Contains(rec.Body.String(), tc.field) {
				t.Fatalf("got %d %s; want 400 containing %s", rec.Code, rec.Body.String(), tc.field)
			}
		})
	}
}

func TestArrival_StoreErrorsMapToProblems(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantDetail string
	}{
		{"over delivery", &domain.Error{Code: domain.CodeOverDelivery, Detail: "line 1 too many"}, http.StatusUnprocessableEntity, "RCV-OVR-001", "line 1 too many"},
		{"duplicate serial", &domain.Error{Code: domain.CodeDuplicateSerial, Detail: "serial S1"}, http.StatusUnprocessableEntity, "INV-SER-001", "serial S1"},
		{"invalid receipt", &domain.Error{Code: domain.CodeInvalidReceipt, Detail: "needs batch"}, http.StatusUnprocessableEntity, "RCV-VAL-001", "needs batch"},
		{"invalid transition", &domain.Error{Code: domain.CodeInvalidTransition, Detail: "not arrived"}, http.StatusConflict, "RCV-STA-001", "not arrived"},
		{"duplicate packing slip", &store.DuplicatePackingSlipError{ReceiptNumber: "RCV-00000042"}, http.StatusConflict, "RCV-DUP-001", "RCV-00000042"},
		{"wrapped not found", fmt.Errorf("lookup: %w", store.ErrNotFound), http.StatusNotFound, "RCV-NF-001", ""},
		{"unmapped domain code", &domain.Error{Code: domain.CodePeriodClosed, Detail: "closed"}, http.StatusInternalServerError, "COM-0500", "internal error"},
		{"arbitrary error", errors.New("connection refused to 10.0.0.7"), http.StatusInternalServerError, "COM-0500", "internal error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(createErrStore{Store: memstore.New(), err: tc.err})
			rec := doWith(r, http.MethodPost, arrivalsPath, arrivalJSON("PS", ""), maintainer("k"))
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), `"code":"`+tc.wantCode+`"`) || !strings.Contains(rec.Body.String(), tc.wantDetail) {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
			if tc.wantStatus == http.StatusInternalServerError && (strings.Contains(rec.Body.String(), "10.0.0.7") || strings.Contains(rec.Body.String(), "closed")) {
				t.Fatalf("500 body leaks the error: %s", rec.Body.String())
			}
		})
	}
}

func TestMetrics_CountArrivals(t *testing.T) {
	r, a := newRouter(memstore.New())
	doWith(r, http.MethodPost, arrivalsPath, arrivalJSON("PS-1", ""), maintainer("k1"))
	doWith(r, http.MethodPost, arrivalsPath, arrivalJSON("PS-1", ""), maintainer("k2"))
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{
		"receiving_receipts_arrived_total 1",
		"receiving_receipts_registered_total 0",
		"# TYPE receiving_receipts_arrived_total counter",
		"receiving_matches_evaluated_total 0",
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("metrics missing %q in %s", want, rec.Body.String())
		}
	}
}
