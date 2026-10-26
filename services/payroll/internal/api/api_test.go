package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const testTenant = "00000000-0000-0000-0000-000000000001"

func newServer(t *testing.T) http.Handler {
	t.Helper()
	router := httpx.NewRouter()
	New(nil).Register(router)
	return router
}

func post(t *testing.T, h http.Handler, path, body, duties string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderTenant, testTenant)
	if duties != "" {
		req.Header.Set(tenant.HeaderDuties, duties)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// br431 is the BR-43.1 worked example as a request.
const br431 = `{
  "workerId": "00000000-0000-0000-0000-0000000000a1",
  "runType": "REGULAR", "country": "US", "taxYear": 2025, "periodsPerYear": 26,
  "w4": {"filingStatus": "SINGLE"}, "state": "CO",
  "earnings": [{"code": "REGULAR", "amount": "4000.00"}],
  "preTax": [{"code": "401K", "amount": "240.00"}],
  "employerMatch": {"code": "401K", "percent": "0.5", "capRate": "0.06"},
  "ytd": {}
}`

func TestCalculateWorkedExample(t *testing.T) {
	rec := post(t, newServer(t), "/v1/payroll/calculations", br431, "payroll.run.maintain")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Net     string `json:"net"`
		RuleSet string `json:"ruleSet"`
		Hash    string `json:"hash"`
		Lines   []struct {
			Code    string `json:"code"`
			Amount  string `json:"amount"`
			Bracket *struct {
				Schedule string `json:"schedule"`
				Over     string `json:"over"`
			} `json:"bracket"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// Pub 15-T Worksheet 1A for a 2020+ W-4 (see the domain test for the PRD figure).
	if got.Net != "2783.90" || got.RuleSet != "US-FED-2025.1" || len(got.Hash) != 64 {
		t.Fatalf("net %s rule %s hash %q", got.Net, got.RuleSet, got.Hash)
	}
	found := false
	for _, l := range got.Lines {
		if l.Code == "FED_WH" && l.Amount == "504.66" && l.Bracket != nil && l.Bracket.Schedule == "STANDARD" && l.Bracket.Over == "54875.00" {
			found = true
		}
	}
	if !found {
		t.Fatalf("FED_WH 504.66 with its schedule row missing: %s", rec.Body.String())
	}
}

func TestCalculatePreTwentyTwentyW4ReproducesPRDFigures(t *testing.T) {
	body := strings.Replace(br431, `"w4": {"filingStatus": "SINGLE"}`, `"w4": {"filingStatus": "SINGLE", "legacy": true}`, 1)
	rec := post(t, newServer(t), "/v1/payroll/calculations", body, "payroll.run.maintain")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"net":"2711.13"`) || !strings.Contains(rec.Body.String(), `"amount":"577.43"`) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCalculateRejectsMalformedTenant(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/payroll/calculations", bytes.NewBufferString(br431))
	req.Header.Set(tenant.HeaderTenant, "not-a-uuid")
	req.Header.Set(tenant.HeaderDuties, "payroll.run.maintain")
	rec := httptest.NewRecorder()
	newServer(t).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "AUTH-0001") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCalculateRequiresDuty(t *testing.T) {
	rec := post(t, newServer(t), "/v1/payroll/calculations", br431, "")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCalculateValidation(t *testing.T) {
	body := strings.Replace(br431, `"periodsPerYear": 26`, `"periodsPerYear": 30`, 1)
	body = strings.Replace(body, `"amount": "4000.00"`, `"amount": "-1"`, 1)
	rec := post(t, newServer(t), "/v1/payroll/calculations", body, "payroll.run.maintain")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d", rec.Code)
	}
	for _, field := range []string{"periodsPerYear", "earnings[0].amount"} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Errorf("error does not name %s: %s", field, rec.Body.String())
		}
	}
}

func TestCalculateFailsClosedWithoutPublishedRuleSet(t *testing.T) {
	body := strings.Replace(br431, `"taxYear": 2025`, `"taxYear": 2031`, 1)
	rec := post(t, newServer(t), "/v1/payroll/calculations", body, "payroll.run.maintain")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "PAY-RULE-001") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCalculateRejectsInvalidW4AndPreTax(t *testing.T) {
	tests := []struct {
		name, from, to, field string
	}{
		{"unknown filing status", `"w4": {"filingStatus": "SINGLE"}`, `"w4": {"filingStatus": "MARRIED"}`, "w4.filingStatus"},
		{"head of household on a pre-2020 W-4", `"w4": {"filingStatus": "SINGLE"}`, `"w4": {"filingStatus": "HEAD_OF_HOUSEHOLD", "legacy": true}`, "w4.filingStatus"},
		{"allowances on a 2020+ W-4", `"w4": {"filingStatus": "SINGLE"}`, `"w4": {"filingStatus": "SINGLE", "allowances": 2}`, "w4.allowances"},
		{"negative Step 3 credit", `"w4": {"filingStatus": "SINGLE"}`, `"w4": {"filingStatus": "SINGLE", "dependentsCredit": "-1"}`, "w4.dependentsCredit"},
		{"pre-tax above gross", `"amount": "240.00"`, `"amount": "4000.01"`, "preTax"},
		{"unknown state", `"state": "CO"`, `"state": "co"`, "state"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(t, newServer(t), "/v1/payroll/calculations", strings.Replace(br431, tc.from, tc.to, 1), "payroll.run.maintain")
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+tc.field+`"`) {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestCalculateStateWithoutRateFailsClosed(t *testing.T) {
	rec := post(t, newServer(t), "/v1/payroll/calculations", strings.Replace(br431, `"state": "CO"`, `"state": "ZZ"`, 1), "payroll.run.maintain")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "PAY-RULE-001") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestReadyzWithoutDatabase(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	newServer(t).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
}
