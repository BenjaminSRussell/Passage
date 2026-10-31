package api_test

import (
	"crypto/ed25519"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/purchasing"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

const (
	evalPOID   = "00000000-0000-4000-8000-0000000000b1"
	callerUser = "00000000-0000-4000-8000-0000000000c1"
	evalLE     = "00000000-0000-4000-8000-0000000000e1"
)

const purchasingOrder = `{"id":"` + evalPOID + `","number":"PO-1","legalEntityId":"` + evalLE + `","vendorId":"v-1","currency":"USD","approvalStatus":"APPROVED","documentStatus":"NONE","rowVersion":2,
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
	req.Header.Set("X-Passage-Legal-Entity", evalLE)
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
		{"unknown order", http.StatusNotFound, `{}`, true, http.StatusNotFound, "RCV-NF-004"},
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
		{"in review order", domain.PurchaseOrder{ApprovalStatus: "IN_REVIEW", DocumentStatus: "NONE", Lines: line("OPEN")}, "RCV-PO-001"},
		{"rejected order", domain.PurchaseOrder{ApprovalStatus: "REJECTED", DocumentStatus: "NONE", Lines: line("OPEN")}, "RCV-PO-001"},
		{"finalized order", domain.PurchaseOrder{ApprovalStatus: "FINALIZED", DocumentStatus: "INVOICED", Lines: line("OPEN")}, "RCV-PO-001"},
		{"cancelled order", domain.PurchaseOrder{ApprovalStatus: "CANCELLED", DocumentStatus: "NONE", Lines: line("OPEN")}, "RCV-PO-001"},
		{"cancelled line", domain.PurchaseOrder{ApprovalStatus: "CONFIRMED", DocumentStatus: "NONE", Lines: line("CANCELLED")}, "RCV-PO-002"},
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
			metrics := httptest.NewRecorder()
			a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			if !strings.Contains(metrics.Body.String(), "receiving_evaluate_rejected_total 1\n") {
				t.Fatalf("refusal not counted: %s", metrics.Body)
			}
		})
	}
}

func TestEvaluateAcceptsApprovedAndConfirmedOrders(t *testing.T) {
	for _, approval := range []string{"APPROVED", "CONFIRMED"} {
		t.Run(approval, func(t *testing.T) {
			po := domain.PurchaseOrder{ApprovalStatus: approval, DocumentStatus: "RECEIVED_PARTIAL", Currency: "USD", Lines: []domain.PurchaseOrderLine{{LineNo: 1, UnitPrice: decimal.RequireFromString("2.5"), PriceUnit: decimal.NewFromInt(1), Qty: decimal.NewFromInt(10), Status: "RECEIVED"}}}
			a := api.New(memstore.New(), api.Ports{
				Periods:        domain.FakePeriods{Status: domain.PeriodOpen},
				Ledger:         &domain.FakeLedger{},
				PurchaseOrders: domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{evalPOID: po}},
			})
			r := httpx.NewRouter()
			a.Register(r)
			rec := do(r, http.MethodPost, evaluatePath, poBody(`"poLineNo":1`), "MatchingPolicyMaintain", true)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"MATCHED"`) {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestEvaluatePoReferenceValidation(t *testing.T) {
	a := api.New(memstore.New(), api.Ports{
		Periods:        domain.FakePeriods{Status: domain.PeriodOpen},
		Ledger:         &domain.FakeLedger{},
		PurchaseOrders: domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{evalPOID: {ID: evalPOID, Currency: "USD", ApprovalStatus: "CONFIRMED", DocumentStatus: "NONE", Lines: []domain.PurchaseOrderLine{{LineNo: 1, PriceUnit: decimal.NewFromInt(1), Status: "OPEN"}}}}},
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

func purchasingCounter(t *testing.T) (*atomic.Int32, http.Handler) {
	t.Helper()
	var lookups atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		lookups.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(purchasingOrder))
	}))
	t.Cleanup(srv.Close)
	a := api.New(memstore.New(), api.Ports{
		Periods: domain.FakePeriods{Status: domain.PeriodOpen}, Ledger: &domain.FakeLedger{},
		PurchaseOrders: purchasing.New(srv.URL, nil),
	})
	r := httpx.NewRouter()
	a.Register(r)
	return &lookups, r
}

func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder) httpx.Problem {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem: %v: %s", err, rec.Body)
	}
	return p
}

func TestEvaluatePoValidationPrecedesLookup(t *testing.T) {
	lookups, r := purchasingCounter(t)
	body := strings.Replace(poBody(`"poLineNo":1`), `"qty":10`, `"qty":-10`, 1)
	rec := do(r, http.MethodPost, evaluatePath, body, "MatchingPolicyMaintain", true)
	if rec.Code != http.StatusBadRequest || lookups.Load() != 0 {
		t.Fatalf("got status %d after %d purchase order lookups: %s", rec.Code, lookups.Load(), rec.Body)
	}
}

func TestEvaluateRejectsDuplicateAndOverriddenPOLines(t *testing.T) {
	lookups, r := purchasingCounter(t)
	const first = `{"qty":1,"unitPrice":2.5,"poLineNo":1}`
	tests := []struct {
		name  string
		lines string
		want  map[string]string
	}{
		{"adjacent duplicate PO line", first + `,` + first, map[string]string{"lines[1].poLineNo": "duplicates lines[0].poLineNo"}},
		{"non-adjacent duplicate PO line", first + `,{"qty":1,"unitPrice":2.5,"poLineNo":2},` + first, map[string]string{"lines[2].poLineNo": "duplicates lines[0].poLineNo"}},
		{"third duplicate points at the first", first + `,` + first + `,` + first, map[string]string{"lines[1].poLineNo": "duplicates lines[0].poLineNo", "lines[2].poLineNo": "duplicates lines[0].poLineNo"}},
		{"overridden PO unit price", `{"qty":1,"unitPrice":2.5,"poLineNo":1,"poUnitPrice":99}`, map[string]string{"lines[0].poUnitPrice": poOverride}},
		{"overridden remaining quantity", `{"qty":1,"unitPrice":2.5,"poLineNo":1,"poQtyRemaining":99}`, map[string]string{"lines[0].poQtyRemaining": poOverride}},
		{"overridden currency matching the invoice", `{"qty":1,"unitPrice":2.5,"poLineNo":1,"poCurrency":"USD"}`, map[string]string{"lines[0].poCurrency": poOverride}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"purchaseOrderId":"` + evalPOID + `","currency":"USD","headerTotal":2.5,"lines":[` + tc.lines + `]}`
			rec := do(r, http.MethodPost, evaluatePath, body, "MatchingPolicyMaintain", true)
			p := decodeProblem(t, rec)
			if rec.Code != http.StatusBadRequest || p.Code != "COM-0001" || !maps.Equal(p.Errors, tc.want) {
				t.Fatalf("got %d %+v", rec.Code, p)
			}
		})
	}
	if lookups.Load() != 0 {
		t.Fatalf("purchasing called %d times for refused requests", lookups.Load())
	}
}

const poOverride = "is supplied by the purchase order and cannot be overridden"

func TestEvaluateReportsBodyAndPoReferenceErrorsTogether(t *testing.T) {
	lookups, r := purchasingCounter(t)
	body := `{"currency":"usd","headerTotal":2.5,"lines":[{"qty":1,"unitPrice":2.5,"poLineNo":1},{"qty":1,"unitPrice":2.5,"poLineNo":1}]}`
	rec := do(r, http.MethodPost, evaluatePath, body, "MatchingPolicyMaintain", true)
	p := decodeProblem(t, rec)
	want := map[string]string{
		"currency":          "must be a 3-letter upper-case ISO 4217 code",
		"lines[0].poLineNo": "requires purchaseOrderId",
		"lines[1].poLineNo": "duplicates lines[0].poLineNo; requires purchaseOrderId",
	}
	if rec.Code != http.StatusBadRequest || p.Code != "COM-0001" || !maps.Equal(p.Errors, want) || lookups.Load() != 0 {
		t.Fatalf("got %d %+v after %d lookups", rec.Code, p, lookups.Load())
	}
}

func TestEvaluateKeepsBodyErrorWhenPoReferenceErrorSharesTheKey(t *testing.T) {
	lookups, r := purchasingCounter(t)
	body := `{"purchaseOrderId":"` + evalPOID + `","currency":"USD","headerTotal":2.5,"lines":[{"qty":1,"unitPrice":2.5,"poLineNo":1,"poCurrency":"us"}]}`
	rec := do(r, http.MethodPost, evaluatePath, body, "MatchingPolicyMaintain", true)
	p := decodeProblem(t, rec)
	want := map[string]string{"lines[0].poCurrency": "must be a 3-letter ISO 4217 code; " + poOverride}
	if rec.Code != http.StatusBadRequest || !maps.Equal(p.Errors, want) || lookups.Load() != 0 {
		t.Fatalf("got %d %+v after %d lookups", rec.Code, p, lookups.Load())
	}
}

func TestEvaluateSignsThePurchaseOrderReadWithTheNamedLegalEntity(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("receiving", priv)
	verifier := tenant.NewServiceVerifier("purchasing", map[string]ed25519.PublicKey{"receiving": pub})
	const otherLE = "00000000-0000-4000-8000-0000000000e2"
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		_, _ = w.Write([]byte(purchasingOrder))
	}))
	t.Cleanup(srv.Close)
	a := api.New(memstore.New(), api.Ports{
		Periods: domain.FakePeriods{Status: domain.PeriodOpen}, Ledger: &domain.FakeLedger{},
		PurchaseOrders: purchasing.New(srv.URL, nil).WithSigner(signer),
	})
	r := httpx.NewRouter()
	a.Register(r)
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, evaluatePath, strings.NewReader(body))
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Passage-Legal-Entity", evalLE+","+otherLE)
		req.Header.Set("X-Passage-User", callerUser)
		req.Header.Set("X-Passage-Duties", "MatchingPolicyMaintain")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	named := strings.Replace(poBody(`"poLineNo":1`), `"currency"`, `"legalEntityId":"`+evalLE+`","currency"`, 1)
	if rec := post(named); rec.Code != http.StatusOK {
		t.Fatalf("named entity: %d %s", rec.Code, rec.Body)
	}
	claims, err := verifier.Verify(seen.Get(tenant.HeaderServiceToken))
	if err != nil || claims.Le != evalLE || claims.Act != callerUser || seen.Get("X-Passage-Duties") != "" || len(claims.Duties) != 1 || claims.Duties[0] != "procurement.purchase-order.lookup" {
		t.Fatalf("claims %+v err %v headers %v", claims, err, seen)
	}

	seen = nil
	if rec := post(poBody(`"poLineNo":1`)); rec.Code != http.StatusOK || seen.Get(tenant.HeaderServiceToken) != "" || seen.Get("X-Passage-User") != callerUser {
		t.Fatalf("without a named entity a multi-entity user is forwarded: %d %v", rec.Code, seen)
	}

	seen = nil
	outside := strings.Replace(named, evalLE, "00000000-0000-4000-8000-0000000000e9", 1)
	if rec := post(outside); rec.Code != http.StatusForbidden || seen != nil {
		t.Fatalf("an entity outside the caller scope is refused before any call: %d %s", rec.Code, rec.Body)
	}
	if rec := post(strings.Replace(named, evalLE, "not-a-uuid", 1)); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "legalEntityId") {
		t.Fatalf("malformed entity: %d %s", rec.Code, rec.Body)
	}
}
