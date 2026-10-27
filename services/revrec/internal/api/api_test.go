package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

const bundleBody = `{
  "currency": "USD",
  "transactionPrice": "10800.00",
  "obligations": [
    {"id": "hardware", "ssp": "8000", "sspSource": "EXPLICIT", "method": "POINT_IN_TIME"},
    {"id": "installation", "ssp": "1500", "sspSource": "EXPLICIT", "method": "MILESTONE"},
    {"id": "support", "ssp": "2500", "sspSource": "EXPLICIT", "method": "RATABLE",
     "termStart": "2027-03-10", "termEnd": "2028-03-09"}
  ]
}`

func serve(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := httpx.NewRouter()
	New(nil).Register(router)
	req := httptest.NewRequest(http.MethodPost, "/v1/revrec/allocations:preview", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", "revrec.view")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

type previewBody struct {
	Obligations []struct {
		ID        string `json:"id"`
		Allocated string `json:"allocated"`
		Schedule  []struct {
			Amount string `json:"amount"`
		} `json:"schedule"`
	} `json:"obligations"`
}

func TestPreview_BundleAllocatesAndSchedules(t *testing.T) {
	rec := serve(t, bundleBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got previewBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"7200.00", "1350.00", "2250.00"}
	for i, o := range got.Obligations {
		if o.Allocated != want[i] {
			t.Errorf("%s allocated %s, want %s", o.ID, o.Allocated, want[i])
		}
	}
	if n := len(got.Obligations[2].Schedule); n != 13 {
		t.Errorf("support schedule has %d lines, want 13", n)
	}
}

// TestPreview_MonthlyBasis previews the support on the MONTHLY basis: every
// month the term touches takes 173.08, the residual 173.04 goes last.
func TestPreview_MonthlyBasis(t *testing.T) {
	rec := serve(t, strings.Replace(bundleBody, `"currency": "USD",`, `"currency": "USD", "ratableBasis": "MONTHLY",`, 1))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got previewBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	sup := got.Obligations[2].Schedule
	if len(sup) != 13 || sup[0].Amount != "173.08" || sup[12].Amount != "173.04" {
		t.Errorf("monthly schedule %+v, want 13 lines from 173.08 to 173.04", sup)
	}
	if !strings.Contains(rec.Body.String(), `"ratableBasis":"MONTHLY"`) {
		t.Errorf("response does not echo the basis: %s", rec.Body)
	}
}

func TestPreview_ValidationProblems(t *testing.T) {
	tests := []struct {
		name string
		body string
		code string
		key  string
	}{
		{"unknown field", `{"currency":"USD","transactionPrice":"1","obligations":[],"extra":1}`, "COM-0001", ""},
		{"bad currency", strings.Replace(bundleBody, `"USD"`, `"usd"`, 1), "COM-0001", "currency"},
		{"ratable without term", strings.Replace(bundleBody, `"termEnd": "2028-03-09"`, `"termEnd": null`, 1), "COM-0001", "obligations[2].term"},
		{"zero ssp", strings.Replace(bundleBody, `"ssp": "8000"`, `"ssp": "0"`, 1), "COM-0001", "obligations[0].ssp"},
		{"exponent ssp", strings.Replace(bundleBody, `"ssp": "8000"`, `"ssp": "8e3"`, 1), "COM-0001", "obligations[0].ssp"},
		{"duplicate id", strings.Replace(bundleBody, `"id": "installation"`, `"id": "hardware"`, 1), "COM-0001", "obligations[1].id"},
		{"bad residual rule", strings.Replace(bundleBody, `"currency": "USD",`, `"currency": "USD", "residualRule": "MIDDLE",`, 1), "COM-0001", "residualRule"},
		{"no obligations", `{"currency":"USD","transactionPrice":"1","obligations":[]}`, "COM-0001", "obligations"},
		{"bad ratable basis", strings.Replace(bundleBody, `"currency": "USD",`, `"currency": "USD", "ratableBasis": "WEEKLY",`, 1), "COM-0001", "ratableBasis"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body)
			}
			var p httpx.Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatal(err)
			}
			if p.Code != tt.code {
				t.Errorf("code %s, want %s", p.Code, tt.code)
			}
			if tt.key != "" {
				if _, ok := p.Errors[tt.key]; !ok {
					t.Errorf("no field error for %s: %v", tt.key, p.Errors)
				}
			}
		})
	}
}

func TestPreview_RequiresTenant(t *testing.T) {
	router := httpx.NewRouter()
	New(nil).Register(router)
	req := httptest.NewRequest(http.MethodPost, "/v1/revrec/allocations:preview", strings.NewReader(bundleBody))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
}

func TestPreview_RequiresDuty(t *testing.T) {
	router := httpx.NewRouter()
	New(nil).Register(router)
	req := httptest.NewRequest(http.MethodPost, "/v1/revrec/allocations:preview", strings.NewReader(bundleBody))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", "billing.invoice.view")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403", rec.Code)
	}
}

// BR-29.1: a contract price of 10,799.99 leaves 0.02 that goes to the largest obligation.
func TestPreview_ResidualToLargestAndMetrics(t *testing.T) {
	a := New(nil)
	router := httpx.NewRouter()
	a.Register(router)
	body := strings.Replace(bundleBody, `"10800.00"`, `"10799.99"`, 1)
	req := httptest.NewRequest(http.MethodPost, "/v1/revrec/allocations:preview", strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", "revrec.view")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var got previewBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"7200.01", "1349.99", "2249.99"}
	for i, o := range got.Obligations {
		if o.Allocated != want[i] {
			t.Errorf("%s allocated %s, want %s", o.ID, o.Allocated, want[i])
		}
	}
	m := httptest.NewRecorder()
	a.Metrics().ServeHTTP(m, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(m.Body.String(), "revrec_allocation_previews_total 1") {
		t.Errorf("metrics:\n%s", m.Body)
	}
}
