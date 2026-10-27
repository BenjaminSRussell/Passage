package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/costing/internal/store"
)

const (
	testTenant = "00000000-0000-0000-0000-000000000001"
	allDuties  = "costing.read,costing.version.write,costing.version.activate,costing.ppv.post"
)

// fakeStore records calls; slice3 (activation_test.go) holds the activation,
// copy, read and lookup fakes.
type fakeStore struct {
	imported []store.ItemCost
	versions map[string]store.CostingVersion
	slice3
}

func (f *fakeStore) CreateCostingVersion(_ context.Context, in store.CostingVersion, key string) (store.CostingVersion, bool, error) {
	if v, ok := f.versions[key]; ok {
		return v, false, nil
	}
	v := store.CostingVersion{ID: ids.New(), LegalEntityID: in.LegalEntityID, Code: in.Code, Type: in.Type,
		Status: "DRAFT", Currency: in.Currency, EffectiveFrom: in.EffectiveFrom, Version: 1}
	f.versions[key] = v
	return v, true, nil
}

func (f *fakeStore) ImportItemCosts(_ context.Context, _ string, items []store.ItemCost) (int, error) {
	f.imported = append(f.imported, items...)
	return len(items), nil
}

func (f *fakeStore) Ping(context.Context) error { return nil }

func server() (http.Handler, *fakeStore) {
	fs := &fakeStore{versions: map[string]store.CostingVersion{}}
	r := httpx.NewRouter()
	New(fs).Register(r)
	return r, fs
}

func call(h http.Handler, method, path, body, duties string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Passage-Tenant", testTenant)
	req.Header.Set("X-Passage-Duties", duties)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeMap(rec *httptest.ResponseRecorder) (map[string]any, error) {
	var m map[string]any
	err := json.Unmarshal(rec.Body.Bytes(), &m)
	return m, err
}

func TestPreviewSettlements_FR201FIFO(t *testing.T) {
	h, _ := server()
	body := `{"method":"FIFO","currency":"EUR","transactions":[
	  {"id":"R1","kind":"RECEIPT","qty":"100","value":"1000.00","financialDate":"2026-01-05"},
	  {"id":"R2","kind":"RECEIPT","qty":"100","value":"1200.00","financialDate":"2026-01-20"},
	  {"id":"I1","kind":"ISSUE","qty":"150","financialDate":"2026-01-25"}]}`
	rec := call(h, http.MethodPost, "/v1/costing/settlements:preview", body, allDuties, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	m, err := decodeMap(rec)
	if err != nil {
		t.Fatal(err)
	}
	costs := m["issueCosts"].(map[string]any)
	if costs["I1"] != "1600.00" || m["remainingValue"] != "600.00" || m["remainingQty"] != "50" {
		t.Errorf("response = %v", m)
	}
	if len(m["settlements"].([]any)) != 2 {
		t.Errorf("settlements = %v, want 2 rows", m["settlements"])
	}
}

func TestPreviewSettlements_RefusesBadInput(t *testing.T) {
	h, _ := server()
	tests := []struct {
		name string
		body string
		want int
	}{
		{"bad currency", `{"method":"FIFO","currency":"euro","transactions":[{"id":"I","kind":"ISSUE","qty":"1","financialDate":"2026-01-01"}]}`, http.StatusBadRequest},
		{"unknown method", `{"method":"HIFO","currency":"EUR","transactions":[{"id":"I","kind":"ISSUE","qty":"1","financialDate":"2026-01-01"}]}`, http.StatusBadRequest},
		{"receipt without value", `{"method":"FIFO","currency":"EUR","transactions":[{"id":"R","kind":"RECEIPT","qty":"1","financialDate":"2026-01-01"}]}`, http.StatusBadRequest},
		{"issue larger than stock is listed, not refused", `{"method":"FIFO","currency":"EUR","transactions":[{"id":"I","kind":"ISSUE","qty":"1","financialDate":"2026-01-01"}]}`, http.StatusOK},
		{"unknown fallback", `{"method":"FIFO","currency":"EUR","fallback":"GUESS","transactions":[{"id":"I","kind":"ISSUE","qty":"1","financialDate":"2026-01-01"}]}`, http.StatusBadRequest},
		{"bad date", `{"method":"FIFO","currency":"EUR","transactions":[{"id":"I","kind":"ISSUE","qty":"1","financialDate":"01/02/2026"}]}`, http.StatusBadRequest},
		{"standard without a standard cost", `{"method":"STANDARD","currency":"EUR","transactions":[{"id":"I","kind":"ISSUE","qty":"1","financialDate":"2026-01-01"}]}`, http.StatusUnprocessableEntity},
		{"zero quantity is a domain refusal", `{"method":"FIFO","currency":"EUR","transactions":[{"id":"I","kind":"ISSUE","qty":"0","financialDate":"2026-01-01"}]}`, http.StatusUnprocessableEntity},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(h, http.MethodPost, "/v1/costing/settlements:preview", tc.body, allDuties, nil)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestPreviewSettlements_RequiresDuty(t *testing.T) {
	h, _ := server()
	rec := call(h, http.MethodPost, "/v1/costing/settlements:preview", `{}`, "", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
}

func TestPPV_AC204(t *testing.T) {
	h, _ := server()
	receipt := `{"kind":"RECEIPT","currency":"EUR","qty":"100","standardUnitCost":"10.00","poUnitPrice":"10.50","sourceDocumentId":"GR-1"}`
	rec := call(h, http.MethodPost, "/internal/v1/costing/ppv", receipt, allDuties, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("receipt status %d: %s", rec.Code, rec.Body.String())
	}
	if m, _ := decodeMap(rec); m["amount"] != "50.00" || m["direction"] != "DR" {
		t.Errorf("receipt PPV = %v, want 50.00 DR", m)
	}
	invoice := `{"kind":"INVOICE","currency":"EUR","qty":"100","poUnitPrice":"10.50","invoiceUnitPrice":"10.80","sourceDocumentId":"INV-1"}`
	rec = call(h, http.MethodPost, "/internal/v1/costing/ppv", invoice, allDuties, nil)
	if m, _ := decodeMap(rec); m["amount"] != "30.00" || m["direction"] != "DR" {
		t.Errorf("invoice PPV = %v, want 30.00 DR", m)
	}
	bad := `{"kind":"RECEIPT","currency":"EUR","qty":"0","standardUnitCost":"10.00","poUnitPrice":"10.50","sourceDocumentId":"GR-1"}`
	rec = call(h, http.MethodPost, "/internal/v1/costing/ppv", bad, allDuties, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("zero qty status %d, want 422", rec.Code)
	}
}

func TestCreateVersion_RequiresIdempotencyKeyAndScope(t *testing.T) {
	h, _ := server()
	le := ids.New()
	body := `{"legalEntityId":"` + le + `","code":"STD-2026","type":"STANDARD","currency":"EUR","effectiveFrom":"2026-01-01"}`
	rec := call(h, http.MethodPost, "/v1/costing/versions", body, allDuties, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing key status %d, want 400", rec.Code)
	}
	rec = call(h, http.MethodPost, "/v1/costing/versions", body, allDuties, map[string]string{"Idempotency-Key": strings.Repeat("k", 256)})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("over-long key status %d, want 400", rec.Code)
	}
	rec = call(h, http.MethodPost, "/v1/costing/versions", body, allDuties, map[string]string{"Idempotency-Key": "k1"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status %d: %s", rec.Code, rec.Body.String())
	}
	if m, _ := decodeMap(rec); m["status"] != "DRAFT" || m["code"] != "STD-2026" {
		t.Errorf("created = %v", m)
	}
	rec = call(h, http.MethodPost, "/v1/costing/versions", body, allDuties,
		map[string]string{"Idempotency-Key": "k2", "X-Passage-Legal-Entity": ids.New()})
	if rec.Code != http.StatusForbidden {
		t.Errorf("out-of-scope status %d, want 403", rec.Code)
	}
}

func TestImportItemCosts_ValidatesAndRejectsDuplicates(t *testing.T) {
	h, fs := server()
	version := ids.New()
	p, s := ids.New(), ids.New()
	path := "/v1/costing/versions/" + version + "/item-costs:import"
	good := `{"items":[{"productId":"` + p + `","siteId":"` + s + `","unitCost":"10.00"}]}`
	rec := call(h, http.MethodPost, path, good, allDuties, nil)
	if rec.Code != http.StatusOK || len(fs.imported) != 1 {
		t.Fatalf("import status %d, stored %d: %s", rec.Code, len(fs.imported), rec.Body.String())
	}
	dup := `{"items":[{"productId":"` + p + `","siteId":"` + s + `","unitCost":"10.00"},{"productId":"` + p + `","siteId":"` + s + `","unitCost":"11.00"}]}`
	if rec = call(h, http.MethodPost, path, dup, allDuties, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("duplicate status %d, want 400", rec.Code)
	}
	for _, cost := range []string{"-1", "1.0000001", "100000000000000", "ten"} {
		body := `{"items":[{"productId":"` + p + `","siteId":"` + s + `","unitCost":"` + cost + `"}]}`
		if rec = call(h, http.MethodPost, path, body, allDuties, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("unit cost %s status %d, want 400", cost, rec.Code)
		}
	}
	if len(fs.imported) != 1 {
		t.Errorf("refused imports reached the store: %d rows", len(fs.imported))
	}
	if rec = call(h, http.MethodPost, "/v1/costing/versions/not-a-uuid/item-costs:import", good, allDuties, nil); rec.Code != http.StatusNotFound {
		t.Errorf("malformed version status %d, want 404", rec.Code)
	}
}

func TestPreviewSettlements_ShortageIsSettledByLaterReceipt(t *testing.T) {
	h, _ := server()
	body := `{"method":"WEIGHTED_AVG_RUNNING","currency":"JPY","transactions":[
	  {"id":"R1","kind":"RECEIPT","qty":"3","value":"1000","financialDate":"2026-01-05"},
	  {"id":"I1","kind":"ISSUE","qty":"4","financialDate":"2026-01-06"},
	  {"id":"R2","kind":"RECEIPT","qty":"2","value":"700","financialDate":"2026-01-20"}]}`
	rec := call(h, http.MethodPost, "/v1/costing/settlements:preview", body, allDuties, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	m, err := decodeMap(rec)
	if err != nil {
		t.Fatal(err)
	}
	// JPY has no minor unit: 1000 for the first 3, 350 for the fourth from R2.
	if m["issueCosts"].(map[string]any)["I1"] != "1350" || m["remainingValue"] != "350" || m["remainingQty"] != "1" {
		t.Errorf("response = %v", m)
	}
	if len(m["exceptions"].([]any)) != 0 {
		t.Errorf("exceptions = %v, want none once R2 covers the shortage", m["exceptions"])
	}
}

func TestCreateVersion_ReplayIsNotCountedAsCreated(t *testing.T) {
	fs := &fakeStore{versions: map[string]store.CostingVersion{}}
	a := New(fs)
	r := httpx.NewRouter()
	a.Register(r)
	body := `{"legalEntityId":"` + ids.New() + `","code":"STD-2026","type":"STANDARD","currency":"EUR","effectiveFrom":"2026-01-01"}`
	for i := 0; i < 2; i++ {
		if rec := call(r, http.MethodPost, "/v1/costing/versions", body, allDuties, map[string]string{"Idempotency-Key": "same"}); rec.Code != http.StatusCreated {
			t.Fatalf("call %d status %d: %s", i, rec.Code, rec.Body.String())
		}
	}
	if got := a.versionsCreated.Load(); got != 1 {
		t.Errorf("versions created counter = %d, want 1 (the replay is not a creation)", got)
	}
}
