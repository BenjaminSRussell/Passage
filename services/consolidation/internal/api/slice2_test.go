package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/consolidation/internal/api"
)

const (
	profitPath      = "/v1/consol/profit-in-inventory:preview"
	equityPath      = "/v1/consol/equity-method:preview"
	divestPath      = "/v1/consol/divestitures:preview"
	icDiffPath      = "/v1/consol/ic-differences:preview"
	validationsPath = "/v1/consol/validations:preview"
)

type problemBody struct {
	Code   string            `json:"code"`
	Errors map[string]string `json:"errors"`
}

type errCase struct {
	name, body, duties string
	status             int
	code, field        string
}

func runErrors(t *testing.T, path string, cases []errCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(api.New(), path, tenantID, c.duties, c.body)
			var p problemBody
			_ = json.Unmarshal(rec.Body.Bytes(), &p)
			if rec.Code != c.status || (c.code != "" && p.Code != c.code) || (c.field != "" && p.Errors[c.field] == "") {
				t.Fatalf("status %d body %s; want %d %s on %q", rec.Code, rec.Body.String(), c.status, c.code, c.field)
			}
		})
	}
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

type entryOut struct{ Rule, Account, Debit, Credit string }

// AC-40.12 plus one layer without group cost that falls back to a 10 % margin.
const profitBody = `{"currency":"EUR","sellerMarginPct":"10","layers":[
  {"id":"RCPT-0042","quantity":"40","legalUnitCost":"120.00","groupUnitCost":"100.00"},
  {"id":"RCPT-0043","quantity":"5","legalUnitCost":"120.00"}
]}`

func TestPreviewProfitInInventory_AC4012(t *testing.T) {
	a := api.New()
	only := `{"currency":"EUR","layers":[{"id":"RCPT-0042","quantity":"40","legalUnitCost":"120.00","groupUnitCost":"100.00"}]}`
	got := decode[struct {
		Layers        []struct{ ID, Basis, Profit string }
		Total         string
		FallbackCount int
		Warnings      []struct{ Code string }
	}](t, do(a, profitPath, tenantID, api.DutyRun, only))
	if got.Total != "800.00" || len(got.Layers) != 1 || got.Layers[0].ID != "RCPT-0042" || got.Layers[0].Basis != "GroupCost" || len(got.Warnings) != 0 {
		t.Fatalf("AC-40.12 = %+v; want 800.00 on layer RCPT-0042 by group cost", got)
	}

	got = decode[struct {
		Layers        []struct{ ID, Basis, Profit string }
		Total         string
		FallbackCount int
		Warnings      []struct{ Code string }
	}](t, do(a, profitPath, tenantID, api.DutyRun, profitBody))
	// 800.00 + 5 × 120.00 × 10 % = 860.00, with the fallback layer listed and CNS-0908 raised.
	if got.Total != "860.00" || got.FallbackCount != 1 || got.Layers[1].Basis != "MarginFallback" || got.Layers[1].Profit != "60.00" ||
		len(got.Warnings) != 1 || got.Warnings[0].Code != "CNS-0908" {
		t.Fatalf("mixed layers = %+v", got)
	}
}

func TestPreviewProfitInInventory_Errors(t *testing.T) {
	runErrors(t, profitPath, []errCase{
		{"no layers", `{"currency":"EUR","layers":[]}`, api.DutyRun, http.StatusBadRequest, "COM-0001", "layers"},
		{"fallback layer without seller margin", `{"currency":"EUR","layers":[{"quantity":"1","legalUnitCost":"1"}]}`, api.DutyRun, http.StatusBadRequest, "COM-0001", "sellerMarginPct"},
		{"bad quantity", strings.Replace(profitBody, `"quantity":"40"`, `"quantity":"forty"`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", "layers[0].quantity"},
		{"negative quantity", strings.Replace(profitBody, `"quantity":"40"`, `"quantity":"-40"`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", ""},
		{"margin above 100", strings.Replace(profitBody, `"10"`, `"150"`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", ""},
		{"bad currency", strings.Replace(profitBody, `"EUR"`, `"eur"`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", "currency"},
		{"view duty may not run", profitBody, api.DutyView, http.StatusForbidden, "", ""},
	})
}

// equityBody is AC-40.14: 30 % associate, investment 300,000.00, net income
// 200,000.00, dividends 20,000.00.
const equityBody = `{"currency":"USD","ownershipPct":"30","openingCarrying":"300000.00","netIncome":"200000.00","dividendsReceived":"20000.00"}`

func TestPreviewEquityMethod_AC4014(t *testing.T) {
	got := decode[struct {
		ShareOfProfit, UnrecognizedLoss, ClosingCarrying string
		Entries                                          []entryOut
		Balanced                                         bool
	}](t, do(api.New(), equityPath, tenantID, api.DutyRun, equityBody))
	if got.ShareOfProfit != "60000.00" || got.ClosingCarrying != "340000.00" || got.UnrecognizedLoss != "0.00" || !got.Balanced {
		t.Fatalf("equity method = %+v", got)
	}
	want := []entryOut{
		{"EQ", "INVESTMENT_ASSOCIATE", "60000.00", "0.00"},
		{"EQ", "SHARE_OF_PROFIT_ASSOCIATES", "0.00", "60000.00"},
		{"EQ", "DIVIDEND_INCOME", "20000.00", "0.00"},
		{"EQ", "INVESTMENT_ASSOCIATE", "0.00", "20000.00"},
	}
	if len(got.Entries) != len(want) {
		t.Fatalf("entries = %+v", got.Entries)
	}
	for i := range want {
		if got.Entries[i] != want[i] {
			t.Fatalf("entry %d = %+v; want %+v", i, got.Entries[i], want[i])
		}
	}
}

func TestPreviewEquityMethod_Errors(t *testing.T) {
	runErrors(t, equityPath, []errCase{
		{"control is not an associate", strings.Replace(equityBody, `"30"`, `"60"`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", ""},
		{"missing net income", `{"currency":"USD","ownershipPct":"30","openingCarrying":"1"}`, api.DutyRun, http.StatusBadRequest, "COM-0001", "netIncome"},
		{"dividends above carrying amount", strings.Replace(equityBody, `"20000.00"`, `"400000.00"`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", ""},
		{"no duty", equityBody, "", http.StatusForbidden, "", ""},
	})
}

func TestPreviewDivestiture(t *testing.T) {
	body := `{"currency":"USD","proceeds":"500000","netAssetsCarrying":"300000","goodwill":"50000","nciCarrying":"20000","ctaRecycled":"10000"}`
	got := decode[struct{ CarryingAmount, Gain string }](t, do(api.New(), divestPath, tenantID, api.DutyRun, body))
	// 500,000 − (300,000 + 50,000 − 20,000) + 10,000 = 180,000.
	if got.CarryingAmount != "330000.00" || got.Gain != "180000.00" {
		t.Fatalf("divestiture = %+v", got)
	}
	runErrors(t, divestPath, []errCase{
		{"negative proceeds", strings.Replace(body, `"500000"`, `"-1"`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", "proceeds"},
		{"missing net assets", `{"currency":"USD","proceeds":"1"}`, api.DutyRun, http.StatusBadRequest, "COM-0001", "netAssetsCarrying"},
	})
}

const icDiffBody = `{"currency":"USD","pairs":[
  {"pair":"P-S1","receivable":"50000.00","payable":"49400.00"},
  {"pair":"P-S2","receivable":"20000.00","payable":"22500.00"}
]}`

func TestPreviewICDifferences_BR4015(t *testing.T) {
	a := api.New()
	got := decode[struct {
		Threshold, TaskStatus string
		Pairs                 []struct {
			Pair, Difference, Status, Code string
			Entries                        []entryOut
		}
	}](t, do(a, icDiffPath, tenantID, api.DutyRun, icDiffBody))
	if got.Threshold != "1000.00" || got.TaskStatus != "Error" || len(got.Pairs) != 2 {
		t.Fatalf("response = %+v; want default threshold 1000.00 and task Error", got)
	}
	ok := got.Pairs[0]
	if ok.Status != "Eliminated" || ok.Difference != "600.00" || len(ok.Entries) != 3 ||
		ok.Entries[2] != (entryOut{"E2", "IC_DIFFERENCE", "600.00", "0.00"}) {
		t.Fatalf("pair within threshold = %+v", ok)
	}
	bad := got.Pairs[1]
	if bad.Status != "Error" || bad.Code != "CNS-0905" || bad.Difference != "-2500.00" || len(bad.Entries) != 0 {
		t.Fatalf("pair above threshold = %+v", bad)
	}

	raised := strings.Replace(icDiffBody, `"pairs"`, `"threshold":"3000","pairs"`, 1)
	got = decode[struct {
		Threshold, TaskStatus string
		Pairs                 []struct {
			Pair, Difference, Status, Code string
			Entries                        []entryOut
		}
	}](t, do(a, icDiffPath, tenantID, api.DutyRun, raised))
	if got.TaskStatus != "Completed" || got.Pairs[1].Status != "Eliminated" || got.Pairs[1].Entries[2] != (entryOut{"E2", "IC_DIFFERENCE", "0.00", "2500.00"}) {
		t.Fatalf("threshold 3000 = %+v", got)
	}
	mrec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(mrec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(mrec.Body.String(), "consolidation_ic_difference_previews_total 2") {
		t.Fatalf("metrics = %s", mrec.Body.String())
	}
	runErrors(t, icDiffPath, []errCase{
		{"no pairs", `{"currency":"USD","pairs":[]}`, api.DutyRun, http.StatusBadRequest, "COM-0001", "pairs"},
		{"negative threshold", strings.Replace(icDiffBody, `"pairs"`, `"threshold":"-1","pairs"`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", "threshold"},
		{"missing pair name", strings.Replace(icDiffBody, `"P-S1"`, `""`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", "pairs[0].pair"},
		{"missing payable", `{"currency":"USD","pairs":[{"pair":"x","receivable":"1"}]}`, api.DutyRun, http.StatusBadRequest, "COM-0001", "pairs[0].payable"},
	})
}

// validationBody is AC-40.15: S1's balance sheet is out by 12.00; S2 balances
// but has an IC item without a partner.
const validationBody = `{"units":[
  {"unitId":"S1","totalAssets":"1012.00","totalLiabilities":"400.00","totalEquity":"600.00","openingRetained":"200.00","netIncome":"150.00","dividends":"50.00","closingRetained":"300.00"},
  {"unitId":"S2","totalAssets":"1000.00","totalLiabilities":"400.00","totalEquity":"600.00","openingRetained":"200.00","netIncome":"150.00","dividends":"50.00","closingRetained":"300.00","icItemsWithoutPartner":1}
]}`

type validationOut struct {
	Units []struct {
		UnitID, Status string
		Rules          []struct {
			Rule, Severity, Difference, Code string
			Failed                           bool
		}
	}
	GroupTranslation string
}

func TestPreviewValidation_AC4015(t *testing.T) {
	got := decode[validationOut](t, do(api.New(), validationsPath, tenantID, api.DutyRun, validationBody))
	if got.GroupTranslation != "Blocked" || len(got.Units) != 2 {
		t.Fatalf("response = %+v; want group translation Blocked", got)
	}
	s1 := got.Units[0]
	if s1.Status != "Error" || s1.Rules[0].Rule != "BS_BALANCES" || !s1.Rules[0].Failed || s1.Rules[0].Difference != "12" || s1.Rules[0].Code != "CNS-0906" {
		t.Fatalf("S1 = %+v; want Error with BS_BALANCES out by 12", s1)
	}
	if s2 := got.Units[1]; s2.Status != "CompletedWithWarnings" || !s2.Rules[2].Failed || s2.Rules[2].Severity != "Warning" {
		t.Fatalf("S2 = %+v; want CompletedWithWarnings from the IC partner rule", s2)
	}

	fixed := strings.Replace(validationBody, `"1012.00"`, `"1000.00"`, 1)
	got = decode[validationOut](t, do(api.New(), validationsPath, tenantID, api.DutyRun, fixed))
	if got.GroupTranslation != "NotStarted" || got.Units[0].Status != "Completed" {
		t.Fatalf("balanced group = %+v; a warning must not block translation", got)
	}
	runErrors(t, validationsPath, []errCase{
		{"no units", `{"units":[]}`, api.DutyRun, http.StatusBadRequest, "COM-0001", "units"},
		{"duplicate unit", strings.Replace(validationBody, `"S2"`, `"S1"`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", "units[1].unitId"},
		{"missing total", strings.Replace(validationBody, `"totalEquity":"600.00",`, ``, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", "units[0].totalEquity"},
		{"negative IC item count", strings.Replace(validationBody, `"icItemsWithoutPartner":1`, `"icItemsWithoutPartner":-1`, 1), api.DutyRun, http.StatusBadRequest, "COM-0001", "units[1].icItemsWithoutPartner"},
		{"view duty may not validate", validationBody, api.DutyView, http.StatusForbidden, "", ""},
	})
}
