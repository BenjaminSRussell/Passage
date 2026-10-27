package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

// fakeStore answers the Store port without a database. It records the last
// apply call so tests can assert what the handler forwarded.
type fakeStore struct {
	lastVersion int
	lastPlan    store.ApplyRequest
	lastKey     string
	lastFilter  store.PaymentFilter
	payment     store.Payment
}

func (f *fakeStore) CreateOpenItem(context.Context, store.OpenItem) (store.OpenItem, error) {
	return store.OpenItem{}, nil
}
func (f *fakeStore) ListOpenItems(context.Context, store.OpenItemFilter, int) ([]store.OpenItem, error) {
	return []store.OpenItem{{Currency: "USD", DueDate: "2027-03-01", OpenAmount: decimal.RequireFromString("12.00")}}, nil
}
func (f *fakeStore) CreatePayment(_ context.Context, in store.Payment, _ string) (store.Payment, error) {
	in.ID, in.Version, in.Status = ids.New(), 1, "RECEIVED"
	f.payment = in
	return in, nil
}
func (f *fakeStore) AgingRows(context.Context) ([]domain.AgingRow, error) {
	due, _ := store.ParseDay("2027-03-01")
	return []domain.AgingRow{{Currency: "USD", DueDate: due, OpenAmount: decimal.RequireFromString("12.00")}}, nil
}
func (f *fakeStore) ListPayments(_ context.Context, filter store.PaymentFilter, _ int) ([]store.Payment, error) {
	f.lastFilter = filter
	return []store.Payment{}, nil
}
func (f *fakeStore) GetPayment(context.Context, string) (store.Payment, error) { return f.payment, nil }
func (f *fakeStore) MatchPayment(context.Context, string) (domain.Result, error) {
	return domain.Result{Decision: domain.DecisionOnAccount, Top: []domain.Candidate{}}, nil
}
func (f *fakeStore) ApplyPayment(_ context.Context, id string, version int, plan store.ApplyRequest, key string) (store.ApplyResult, error) {
	f.lastVersion, f.lastPlan, f.lastKey = version, plan, key
	return store.ApplyResult{Payment: store.Payment{ID: id, Version: version + 1, Status: "APPLIED"}}, nil
}
func (f *fakeStore) Ping(context.Context) error { return nil }

const (
	testTenant = "00000000-0000-0000-0000-000000000001"
	testLE     = "00000000-0000-0000-0000-000000000002"
	testPay    = "00000000-0000-0000-0000-000000000003"
	testItem   = "00000000-0000-0000-0000-000000000004"
)

func router(s store.Store) http.Handler {
	r := chi.NewRouter()
	r.Use(tenant.Middleware)
	New(s).Register(r)
	return r
}

func do(h http.Handler, method, path, body string, duties string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", testTenant)
	req.Header.Set("X-Passage-Duties", duties)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func validPayment() string {
	return `{"legalEntityId":"` + testLE + `","paymentMode":"CHECK","source":"MANUAL","sourceRef":"CHK-1001",
		"amount":"1250.00","currency":"USD","receivedDate":"2027-03-05","valueDate":"2027-03-05",
		"remittance":[{"reference":"INV-20001","amount":"1250.00"}]}`
}

func TestCreatePayment_Validation(t *testing.T) {
	h := router(&fakeStore{})
	key := map[string]string{"Idempotency-Key": "k-1"}
	tests := []struct {
		name    string
		body    string
		headers map[string]string
		duties  string
		status  int
		field   string
	}{
		{"missing idempotency key", validPayment(), nil, "ARPaymentPost", http.StatusBadRequest, ""},
		{"without the duty", validPayment(), key, "ARView", http.StatusForbidden, ""},
		{"lockbox source is an importer's job", strings.Replace(validPayment(), `"MANUAL"`, `"LOCKBOX"`, 1), key, "ARPaymentPost", http.StatusBadRequest, "source"},
		{"zero amount", strings.Replace(validPayment(), `"1250.00"`, `"0"`, 1), key, "ARPaymentPost", http.StatusBadRequest, "amount"},
		{"unknown payment mode", strings.Replace(validPayment(), `"CHECK"`, `"BARTER"`, 1), key, "ARPaymentPost", http.StatusBadRequest, "paymentMode"},
		{"bad value date", strings.Replace(validPayment(), `"valueDate":"2027-03-05"`, `"valueDate":"05/03/2027"`, 1), key, "ARPaymentPost", http.StatusBadRequest, "valueDate"},
		{"valid", validPayment(), key, "ARPaymentPost", http.StatusCreated, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(h, http.MethodPost, "/api/v1/receivables/payments", tc.body, tc.duties, tc.headers)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.field != "" {
				var p struct {
					Errors map[string]string `json:"errors"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Errors[tc.field] == "" {
					t.Fatalf("want field error %q, got %s", tc.field, rec.Body.String())
				}
			}
			if tc.status == http.StatusCreated && rec.Header().Get("ETag") != `"1"` {
				t.Fatalf("ETag = %q, want version 1", rec.Header().Get("ETag"))
			}
		})
	}
}

func TestApplyPayment_RequiresIfMatchAndForwardsPlan(t *testing.T) {
	fs := &fakeStore{}
	h := router(fs)
	path := "/api/v1/receivables/payments/" + testPay + ":apply"
	body := `{"settlements":[{"openItemId":"` + testItem + `","amount":"1000.00","cashDiscount":"20.00"}],"onAccount":"0"}`

	if rec := do(h, http.MethodPost, path, body, "ARPaymentPost", nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("without If-Match: status = %d, want 428", rec.Code)
	}
	if rec := do(h, http.MethodPost, path, body, "ARView", map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusForbidden {
		t.Fatalf("without the duty: status = %d, want 403", rec.Code)
	}
	bad := `{"settlements":[{"openItemId":"not-a-uuid","amount":"1.00"}]}`
	if rec := do(h, http.MethodPost, path, bad, "ARPaymentPost", map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad item id: status = %d, want 400", rec.Code)
	}
	rec := do(h, http.MethodPost, path, body, "ARPaymentPost", map[string]string{"If-Match": `"2"`, "Idempotency-Key": "apply-7"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if fs.lastVersion != 2 || len(fs.lastPlan.Allocations) != 1 || !fs.lastPlan.Allocations[0].CashDiscount.Equal(decimal.RequireFromString("20.00")) {
		t.Fatalf("store received version %d plan %+v", fs.lastVersion, fs.lastPlan)
	}
	if rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("ETag = %q, want the new version 3", rec.Header().Get("ETag"))
	}
	if fs.lastKey != "apply-7" {
		t.Fatalf("Idempotency-Key forwarded = %q, want apply-7", fs.lastKey)
	}
}

func TestCustomMethods_UnknownActionIsNotFound(t *testing.T) {
	h := router(&fakeStore{})
	rec := do(h, http.MethodPost, "/api/v1/receivables/payments/"+testPay+":refund", `{}`, "ARPaymentPost", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	rec = do(h, http.MethodPost, "/api/v1/receivables/payments/"+testPay, `{}`, "ARPaymentPost", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("no action: status = %d, want 404", rec.Code)
	}
}

// The PRD (§5.30.5, §5.30.17) puts :match under ARPaymentPost, not ARView.
func TestMatch_NeedsPaymentPostDuty(t *testing.T) {
	h := router(&fakeStore{})
	path := "/api/v1/receivables/payments/" + testPay + ":match"
	if rec := do(h, http.MethodPost, path, "", "ARView", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	rec := do(h, http.MethodPost, path, "", "ARPaymentPost", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"decision":"ON_ACCOUNT"`) {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
}

func TestReads_ValidateQueryParameters(t *testing.T) {
	h := router(&fakeStore{})
	for _, path := range []string{
		"/api/v1/receivables/open-items?status=sometimes",
		"/api/v1/receivables/open-items?customer=nope",
		"/api/v1/receivables/open-items?limit=0",
		"/api/v1/receivables/aging?asOf=yesterday",
		"/api/v1/receivables/payments?status=PAID",
		"/api/v1/receivables/payments?customer=nope",
		"/api/v1/receivables/payments?limit=501",
	} {
		if rec := do(h, http.MethodGet, path, "", "ARView", nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", path, rec.Code)
		}
	}
	rec := do(h, http.MethodGet, "/api/v1/receivables/aging?asOf=2027-03-31", "", "ARView", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"bucket":"1-30"`) {
		t.Fatalf("aging: status = %d body %s", rec.Code, rec.Body.String())
	}
}

func TestMissingTenantIsUnauthorized(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/receivables/aging", nil)
	rec := httptest.NewRecorder()
	router(&fakeStore{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestListPayments_ForwardsFiltersAndNeedsView(t *testing.T) {
	fs := &fakeStore{}
	h := router(fs)
	path := "/api/v1/receivables/payments?status=ON_ACCOUNT&customer=" + testItem
	if rec := do(h, http.MethodGet, path, "", "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("without ARView: status = %d, want 403", rec.Code)
	}
	rec := do(h, http.MethodGet, path, "", "ARView", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if fs.lastFilter.Status != "ON_ACCOUNT" || fs.lastFilter.CustomerID != testItem {
		t.Fatalf("filter = %+v", fs.lastFilter)
	}
}
