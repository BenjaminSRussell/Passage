package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/purchasing"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

const (
	evalPOID   = "00000000-0000-4000-8000-0000000000b1"
	callerUser = "00000000-0000-4000-8000-0000000000c1"
)

const purchasingOrder = `{"id":"` + evalPOID + `","number":"PO-1","legalEntityId":"","vendorId":"v-1","currency":"USD","approvalStatus":"APPROVED","documentStatus":"CONFIRMED","rowVersion":2,
"lines":[{"lineNo":1,"productId":"p-1","unit":"EA","qty":"10","unitPrice":"250.00","priceUnit":"100","discountPct":"0","grossAmount":"25.00","lineAmount":"25.00","receivedQty":"10","invoicedQty":"0","status":"OPEN"}]}`

func evaluateThrough(t *testing.T, status int, upstream string, wire bool) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	var seen http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = *r.Clone(r.Context())
		w.WriteHeader(status)
		_, _ = w.Write([]byte(upstream))
	}))
	t.Cleanup(srv.Close)
	ports := api.Ports{Periods: domain.FakePeriods{Status: domain.PeriodOpen}, Ledger: &domain.FakeLedger{}}
	if wire {
		ports.PurchaseOrders = purchasing.New(srv.URL, nil)
	}
	a := api.New(memstore.New(), ports)
	r := httpx.NewRouter()
	a.Register(r)
	req := httptest.NewRequest(http.MethodPost, evaluatePath, strings.NewReader(poBody(`"poLineNo":1`)))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-User", callerUser)
	req.Header.Set("X-Passage-Duties", "MatchingPolicyMaintain,PurchaseOrderInquire")
	req.Header.Set("X-Request-Id", "req-42")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec, &seen
}

func poBody(lineExtra string) string {
	return `{"purchaseOrderId":"` + evalPOID + `","currency":"USD","headerTotal":25,"lines":[{"qty":10,"unitPrice":2.5,"receiptState":"POSTED","receivedUninvoicedQty":10,` + lineExtra + `}]}`
}

func TestEvaluateTakesPoLinesFromPurchasing(t *testing.T) {
	rec, seen := evaluateThrough(t, http.StatusOK, purchasingOrder, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"MATCHED"`) {
		t.Fatalf("priceUnit 100 must normalise to 2.5 per unit: %d %s", rec.Code, rec.Body)
	}
	if seen.URL.Path != "/v1/procurement/purchase-orders/"+evalPOID {
		t.Fatalf("path %s", seen.URL.Path)
	}
	for header, want := range map[string]string{
		"X-Passage-Tenant": testkit.TenantID(1),
		"X-Passage-User":   callerUser,
		"X-Passage-Duties": "MatchingPolicyMaintain,PurchaseOrderInquire",
		"X-Request-Id":     "req-42",
	} {
		if got := seen.Header.Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestEvaluateReportsPurchasingFailures(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		upstream string
		wire     bool
		want     int
		code     string
	}{
		{"unknown order", http.StatusNotFound, `{}`, true, http.StatusNotFound, "RCV-NF-003"},
		{"caller lacks the duty", http.StatusForbidden, `{}`, true, http.StatusForbidden, "AUTH-0003"},
		{"outage", http.StatusInternalServerError, `{}`, true, http.StatusServiceUnavailable, "COM-0503"},
		{"no lookup configured", http.StatusOK, purchasingOrder, false, http.StatusServiceUnavailable, "COM-0503"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, _ := evaluateThrough(t, tc.status, tc.upstream, tc.wire)
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.code) {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestEvaluateRefusesUnusablePurchaseOrders(t *testing.T) {
	line := func(status string) []domain.PurchaseOrderLine {
		return []domain.PurchaseOrderLine{{LineNo: 1, PriceUnit: decimal.NewFromInt(1), Status: status}}
	}
	tests := []struct {
		name string
		po   domain.PurchaseOrder
		code string
	}{
		{"draft order", domain.PurchaseOrder{ApprovalStatus: "DRAFT", DocumentStatus: "NONE", Lines: line("OPEN")}, "RCV-PO-001"},
		{"cancelled order", domain.PurchaseOrder{ApprovalStatus: "APPROVED", DocumentStatus: "CANCELLED", Lines: line("OPEN")}, "RCV-PO-001"},
		{"cancelled line", domain.PurchaseOrder{ApprovalStatus: "APPROVED", DocumentStatus: "CONFIRMED", Lines: line("CANCELLED")}, "RCV-PO-002"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := api.New(memstore.New(), api.Ports{
				Periods:        domain.FakePeriods{Status: domain.PeriodOpen},
				Ledger:         &domain.FakeLedger{},
				PurchaseOrders: domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{evalPOID: tc.po}},
			})
			r := httpx.NewRouter()
			a.Register(r)
			rec := do(r, http.MethodPost, evaluatePath, poBody(`"poLineNo":1`), "MatchingPolicyMaintain", true)
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), tc.code) {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestEvaluatePoReferenceValidation(t *testing.T) {
	a := api.New(memstore.New(), api.Ports{
		Periods:        domain.FakePeriods{Status: domain.PeriodOpen},
		Ledger:         &domain.FakeLedger{},
		PurchaseOrders: domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{evalPOID: {ID: evalPOID, Currency: "USD", ApprovalStatus: "APPROVED", DocumentStatus: "CONFIRMED", Lines: []domain.PurchaseOrderLine{{LineNo: 1, PriceUnit: decimal.NewFromInt(1), Status: "OPEN"}}}}},
	})
	r := httpx.NewRouter()
	a.Register(r)
	tests := []struct{ name, body, field string }{
		{"not a uuid", strings.Replace(poBody(`"poLineNo":1`), evalPOID, "po-1", 1), "purchaseOrderId"},
		{"line without order", strings.Replace(poBody(`"poLineNo":1`), `"purchaseOrderId":"`+evalPOID+`",`, "", 1), "lines[0].poLineNo"},
		{"id without any poLineNo", strings.Replace(poBody(`"poUnitPrice":2.5`), evalPOID, "po-1", 1), "purchaseOrderId"},
		{"unknown line", poBody(`"poLineNo":9`), "lines[0].poLineNo"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(r, http.MethodPost, evaluatePath, tc.body, "MatchingPolicyMaintain", true)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.field) {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}
