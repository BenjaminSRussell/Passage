package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/localization/internal/domain"
)

func newRouter(t *testing.T) chi.Router {
	t.Helper()
	packs, err := domain.LoadPacks(os.DirFS("../../../../countrypacks"))
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	New(packs).Register(r)
	return r
}

func call(t *testing.T, h http.Handler, method, path string, body any, duties ...string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf).WithContext(testkit.Ctx(ids.New(), duties...))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestListAndGetPacks(t *testing.T) {
	h := newRouter(t)
	rec := call(t, h, http.MethodGet, "/v1/localization/packs", nil, "LocalizationAdmin")
	var list struct {
		Items []domain.Pack `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); rec.Code != http.StatusOK || err != nil || len(list.Items) != 7 {
		t.Fatalf("list: %d %v %s", rec.Code, err, rec.Body.String())
	}
	if rec := call(t, h, http.MethodGet, "/v1/localization/packs/DE", nil, "LocalizationAdmin"); rec.Code != http.StatusOK {
		t.Fatalf("get DE: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/v1/localization/packs/ZZ", nil, "LocalizationAdmin"); rec.Code != http.StatusNotFound {
		t.Fatalf("get ZZ: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/v1/localization/packs", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("no duty: %d", rec.Code)
	}
}

func TestInstallCheck(t *testing.T) {
	h := newRouter(t)
	ok := call(t, h, http.MethodPost, "/v1/localization/packs/DE/install:check",
		InstallCheckRequest{EntityCountry: "DE", CalendarOpenPeriodOK: true}, "LocalizationAdmin")
	if ok.Code != http.StatusOK {
		t.Fatalf("installable DE: %d %s", ok.Code, ok.Body.String())
	}
	wrong := call(t, h, http.MethodPost, "/v1/localization/packs/DE/install:check",
		InstallCheckRequest{EntityCountry: "FR", CalendarOpenPeriodOK: true}, "LocalizationAdmin")
	if wrong.Code != http.StatusUnprocessableEntity || !strings.Contains(wrong.Body.String(), domain.CodeCountry) {
		t.Fatalf("country mismatch: %d %s", wrong.Code, wrong.Body.String())
	}
}

func TestTaxIDAndMTDValidation(t *testing.T) {
	h := newRouter(t)
	if rec := call(t, h, http.MethodPost, "/v1/localization/tax-ids:validate", TaxIDRequest{Scheme: "DE_USTID", Value: "DE123456789"}, "LocalizationAdmin"); rec.Code != http.StatusOK {
		t.Fatalf("valid tax id: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, "/v1/localization/tax-ids:validate", TaxIDRequest{Scheme: "DE_USTID", Value: "DE1"}, "LocalizationAdmin"); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid tax id: %d", rec.Code)
	}
	raw, err := os.ReadFile("../../../../countrypacks/uk/fixtures/mtd-vat-26a1.json")
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/localization/statutory/uk-mtd-vat:validate"
	if rec := call(t, h, http.MethodPost, path, json.RawMessage(raw), "LocalizationAdmin"); rec.Code != http.StatusOK {
		t.Fatalf("fixture: %d %s", rec.Code, rec.Body.String())
	}
	broken := strings.Replace(string(raw), `"netVatDue": 750.00`, `"netVatDue": 1.00`, 1)
	rec := call(t, h, http.MethodPost, path, json.RawMessage(broken), "LocalizationAdmin")
	var p struct {
		Code   string            `json:"code"`
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || rec.Code != http.StatusUnprocessableEntity || p.Code != domain.CodeSchema || p.Errors["netVatDue"] == "" {
		t.Fatalf("broken return: %d %s", rec.Code, rec.Body.String())
	}
	// The draft's box1..box9 shape is not HMRC's schema and is refused.
	legacy := `{"periodKey":"26A1","boxes":{"box1":"1"}}`
	if rec := call(t, h, http.MethodPost, path, json.RawMessage(legacy), "LocalizationAdmin"); rec.Code != http.StatusBadRequest {
		t.Fatalf("legacy body: %d", rec.Code)
	}
}

func TestTDSWithholdEndpoint(t *testing.T) {
	h := newRouter(t)
	path := "/v1/localization/in/tds:withhold"
	// AC-48.21: 194J at 10% without PAN withholds 20%.
	rec := call(t, h, http.MethodPost, path, map[string]any{"section": "194J", "amount": "100000", "panPresent": false}, "LocalizationAdmin")
	if rec.Code != http.StatusOK {
		t.Fatalf("withhold: %d %s", rec.Code, rec.Body.String())
	}
	var res domain.WithholdResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.TDSAmount.String() != "20000" || !res.RatePct.Equal(decimal.NewFromInt(20)) || res.Reason != domain.ReasonNoPAN || res.ReturnForm != "26Q" {
		t.Fatalf("want 20000 at 20%% on 26Q, got %+v", res)
	}
	if !strings.Contains(rec.Body.String(), `"tdsAmount":"20000"`) {
		t.Fatalf("amounts travel as decimal strings: %s", rec.Body.String())
	}
	for _, tc := range []struct {
		name   string
		body   map[string]any
		duty   string
		status int
		want   string
	}{
		{"unknown section", map[string]any{"section": "999Z", "amount": "1"}, "LocalizationAdmin", http.StatusUnprocessableEntity, domain.CodeTDSSection},
		{"missing section", map[string]any{"amount": "1"}, "LocalizationAdmin", http.StatusBadRequest, `"section"`},
		{"sub-paisa amount", map[string]any{"section": "194J", "amount": "1.005"}, "LocalizationAdmin", http.StatusBadRequest, `"amount"`},
		{"194C without payee type", map[string]any{"section": "194C", "amount": "40000", "panPresent": true}, "LocalizationAdmin", http.StatusUnprocessableEntity, domain.CodeTDSPayee},
		{"unknown field", map[string]any{"section": "194J", "amount": "1", "rate": "5"}, "LocalizationAdmin", http.StatusBadRequest, ""},
		{"wrong duty", map[string]any{"section": "194J", "amount": "1"}, "ComplianceView", http.StatusForbidden, ""},
	} {
		rec := call(t, h, http.MethodPost, path, tc.body, tc.duty)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}
}
