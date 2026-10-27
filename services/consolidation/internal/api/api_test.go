package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/api"
)

const tenantID = "01890a5d-ac96-774b-bcce-b302099a8057"

const (
	eliminationsPath = "/v1/consol/eliminations:preview"
	translationsPath = "/v1/consol/translations:preview"
)

// psBody is the FR-40.9 worked example: P (USD) owns 80 % of S (EUR).
const psBody = `{
  "currency":"USD","ownershipPct":"80",
  "rates":{"closing":"1.10","average":"1.08","historical":"1.05"},
  "icSales":"100000","icCost":"70000","unsoldShare":"0.4","icReceivable":"100000",
  "payableGroup":"108500","shareCapital":"500000","preAcqRe":"200000",
  "investmentCost":"620000","netIncome":"150000","sellerIsSubsidiary":true
}`

// ac406Body is AC-40.6 with the 80 % holding of the worked example.
const ac406Body = `{
  "currency":"USD","ownershipPct":"80",
  "rates":{"opening":"1.05","average":"1.08","closing":"1.10"},
  "openingNetAssets":"700000","netIncome":"150000","closingNetAssets":"850000"
}`

func do(a *api.API, path, tenant, duties, body string) *httptest.ResponseRecorder {
	r := httpx.NewRouter()
	a.Register(r)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if tenant != "" {
		req.Header.Set("X-Passage-Tenant", tenant)
	}
	if duties != "" {
		req.Header.Set("X-Passage-Duties", duties)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestPreviewEliminations_PSExample(t *testing.T) {
	rec := do(api.New(), eliminationsPath, tenantID, api.DutyRun, psBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Entries []struct {
			Rule, Account, Debit, Credit string
		} `json:"entries"`
		Rules    map[string]map[string]string `json:"rules"`
		Totals   map[string]string            `json:"totals"`
		Balanced bool                         `json:"balanced"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Balanced || len(got.Entries) != 16 {
		t.Fatalf("response = %+v", got)
	}
	if got.Totals["debit"] != "1032952.00" || got.Totals["credit"] != "1032952.00" {
		t.Fatalf("totals = %v", got.Totals)
	}
	ruleDebits := map[string]string{"E1": "108000.00", "E2": "110000.00", "E3": "15552.00", "E4": "767000.00", "E5": "32400.00"}
	for rule, want := range ruleDebits {
		if got.Rules[rule]["debit"] != want || got.Rules[rule]["credit"] != want {
			t.Fatalf("rule %s totals = %v; want %s each side", rule, got.Rules[rule], want)
		}
	}
	if got.Entries[2].Rule != "E2" || got.Entries[2].Account != "IC_PAYABLE" || got.Entries[2].Debit != "108500.00" {
		t.Fatalf("E2 first leg = %+v", got.Entries[2])
	}
}

func TestPreviewEliminations_Authorization(t *testing.T) {
	if rec := do(api.New(), eliminationsPath, tenantID, "", psBody); rec.Code != http.StatusForbidden {
		t.Fatalf("without duty status = %d; want 403", rec.Code)
	}
	if rec := do(api.New(), eliminationsPath, tenantID, api.DutyView, psBody); rec.Code != http.StatusForbidden {
		t.Fatalf("view duty may not compute eliminations: status = %d; want 403", rec.Code)
	}
	if rec := do(api.New(), eliminationsPath, "", api.DutyRun, psBody); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without tenant status = %d; want 401", rec.Code)
	}
	if rec := do(api.New(), eliminationsPath, "", api.DutyRun, `{"currency":"usd"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without tenant an invalid body must still be 401, not validated: status = %d", rec.Code)
	}
}

func TestPreviewEliminations_Errors(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		code       string
		field      string
	}{
		{"missing closing rate", strings.Replace(psBody, `"closing":"1.10",`, ``, 1), http.StatusBadRequest, "COM-0001", "rates.closing"},
		{"bad decimal", strings.Replace(psBody, `"icSales":"100000"`, `"icSales":"ten"`, 1), http.StatusBadRequest, "COM-0001", "icSales"},
		{"exponent decimal", strings.Replace(psBody, `"icSales":"100000"`, `"icSales":"1e5"`, 1), http.StatusBadRequest, "COM-0001", "icSales"},
		{"bad currency", strings.Replace(psBody, `"USD"`, `"usd"`, 1), http.StatusBadRequest, "COM-0001", "currency"},
		{"ownership above 100", strings.Replace(psBody, `"ownershipPct":"80"`, `"ownershipPct":"120"`, 1), http.StatusBadRequest, "COM-0001", ""},
		{"bargain purchase", strings.Replace(psBody, `"investmentCost":"620000"`, `"investmentCost":"1"`, 1), http.StatusUnprocessableEntity, "CNS-0920", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(api.New(), eliminationsPath, tenantID, api.DutyRun, c.body)
			var p struct {
				Code   string            `json:"code"`
				Errors map[string]string `json:"errors"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &p)
			if rec.Code != c.status || p.Code != c.code || (c.field != "" && p.Errors[c.field] == "") {
				t.Fatalf("status %d body %s; want %d %s on %q", rec.Code, rec.Body.String(), c.status, c.code, c.field)
			}
		})
	}
}

func TestPreviewTranslation_AC406(t *testing.T) {
	a := api.New()
	rec := do(a, translationsPath, tenantID, api.DutyRun, ac406Body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"currency": "USD", "openingNetAssets": "735000.00", "netIncome": "162000.00", "closingNetAssets": "935000.00",
		"cta": "38000.00", "nciShareOfCta": "7600.00", "groupShareOfCta": "30400.00",
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %q; want %q (body %s)", k, got[k], v, rec.Body.String())
		}
	}

	mrec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(mrec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(mrec.Body.String(), "consolidation_translation_previews_total 1") {
		t.Fatalf("metrics = %s", mrec.Body.String())
	}
}

func TestPreviewTranslation_Errors(t *testing.T) {
	cases := []struct {
		name, body string
		field      string
	}{
		{"missing opening rate", strings.Replace(ac406Body, `"opening":"1.05",`, ``, 1), "rates.opening"},
		{"missing net income", strings.Replace(ac406Body, `"netIncome":"150000",`, ``, 1), "netIncome"},
		{"bad closing net assets", strings.Replace(ac406Body, `"850000"`, `"lots"`, 1), "closingNetAssets"},
		{"zero rate", strings.Replace(ac406Body, `"closing":"1.10"`, `"closing":"0"`, 1), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(api.New(), translationsPath, tenantID, api.DutyRun, c.body)
			var p struct {
				Code   string            `json:"code"`
				Errors map[string]string `json:"errors"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &p)
			if rec.Code != http.StatusBadRequest || p.Code != "COM-0001" || (c.field != "" && p.Errors[c.field] == "") {
				t.Fatalf("status %d body %s; want 400 COM-0001 on %q", rec.Code, rec.Body.String(), c.field)
			}
		})
	}
}
