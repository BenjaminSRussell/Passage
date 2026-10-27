package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/api"
)

const tenantID = "01890a5d-ac96-774b-bcce-b302099a8057"

func router(a *api.API) http.Handler {
	r := httpx.NewRouter()
	a.Register(r)
	return r
}

func do(h http.Handler, path, tenant, duties, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if tenant != "" {
		req.Header.Set("X-Passage-Tenant", tenant)
	}
	if duties != "" {
		req.Header.Set("X-Passage-Duties", duties)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const journalPath = "/v1/interco/journals:preview"
const classifyPath = "/v1/interco/reconciliations:classify"

// validBody is AC-40.4: USD 1,000.00 expense in A offset to B, paid from B's cash.
const validBody = `{
  "entity":"A","account":"6100-EXPENSE","side":"debit","amount":"1000.00","currency":"USD",
  "offsetEntity":"B","offsetAccount":"1000-CASH","counterCurrency":"USD",
  "matrix":[{"entityA":"A","entityB":"B","dueFrom":"1500-DUE-FROM-B","dueTo":"2500-DUE-TO-A"}]
}`

type legBody struct {
	Entity, Account, Side, Amount, Currency string
}

type journalBody struct {
	PairID   string    `json:"pairId"`
	Balanced bool      `json:"balanced"`
	Legs     []legBody `json:"legs"`
}

func TestPreviewJournal_AC404Legs(t *testing.T) {
	rec := do(router(api.New()), journalPath, tenantID, api.DutyPost, validBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var got journalBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []legBody{
		{"A", "6100-EXPENSE", "debit", "1000.00", "USD"},
		{"A", "1500-DUE-FROM-B", "credit", "1000.00", "USD"},
		{"B", "2500-DUE-TO-A", "debit", "1000.00", "USD"},
		{"B", "1000-CASH", "credit", "1000.00", "USD"},
	}
	if !got.Balanced || len(got.PairID) != 36 || len(got.Legs) != len(want) {
		t.Fatalf("response = %+v", got)
	}
	for i := range want {
		if got.Legs[i] != want[i] {
			t.Fatalf("leg %d = %+v; want %+v", i, got.Legs[i], want[i])
		}
	}
}

func TestPreviewJournal_ConvertsCounterpartyAtTheDocumentRate(t *testing.T) {
	body := strings.Replace(validBody, `"counterCurrency":"USD"`, `"counterCurrency":"EUR","rate":"0.9237"`, 1)
	rec := do(router(api.New()), journalPath, tenantID, api.DutyPost, body)
	var got journalBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if got.Legs[2].Amount != "923.70" || got.Legs[2].Currency != "EUR" || got.Legs[3].Amount != "923.70" {
		t.Fatalf("B legs = %+v %+v; want 923.70 EUR", got.Legs[2], got.Legs[3])
	}
}

func TestPreviewJournal_Authorization(t *testing.T) {
	h := router(api.New())
	if rec := do(h, journalPath, tenantID, "", validBody); rec.Code != http.StatusForbidden {
		t.Fatalf("without duty status = %d; want 403", rec.Code)
	}
	if rec := do(h, journalPath, tenantID, api.DutyView, validBody); rec.Code != http.StatusForbidden {
		t.Fatalf("view duty may not prepare a journal: status = %d; want 403", rec.Code)
	}
	if rec := do(h, journalPath, "", api.DutyPost, validBody); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without tenant status = %d; want 401", rec.Code)
	}
	if rec := do(h, journalPath, "", api.DutyPost, `{"side":"sideways"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without tenant an invalid body must still be 401, not validated: status = %d", rec.Code)
	}
}

func TestPreviewJournal_Errors(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		code       string
		field      string
	}{
		{"bad side", strings.Replace(validBody, `"debit"`, `"sideways"`, 1), http.StatusBadRequest, "COM-0001", "side"},
		{"zero amount", strings.Replace(validBody, `"1000.00"`, `"0"`, 1), http.StatusBadRequest, "COM-0001", "amount"},
		{"zero amount with decimals", strings.Replace(validBody, `"1000.00"`, `"0.00"`, 1), http.StatusBadRequest, "COM-0001", "amount"},
		{"negative amount", strings.Replace(validBody, `"1000.00"`, `"-5"`, 1), http.StatusBadRequest, "COM-0001", "amount"},
		{"amount rounding to zero", strings.Replace(validBody, `"1000.00"`, `"0.004"`, 1), http.StatusBadRequest, "COM-0001", ""},
		{"self offset", strings.Replace(validBody, `"offsetEntity":"B"`, `"offsetEntity":"A"`, 1), http.StatusBadRequest, "COM-0001", "offsetEntity"},
		{"foreign counter currency without rate", strings.Replace(validBody, `"counterCurrency":"USD"`, `"counterCurrency":"EUR"`, 1), http.StatusBadRequest, "COM-0001", "rate"},
		{"incomplete matrix row", strings.Replace(validBody, `"dueTo":"2500-DUE-TO-A"`, `"dueTo":""`, 1), http.StatusBadRequest, "COM-0001", "matrix[0].dueTo"},
		{"exponent rate", strings.Replace(validBody, `"counterCurrency":"USD"`, `"counterCurrency":"EUR","rate":"9e-1"`, 1), http.StatusBadRequest, "COM-0001", "rate"},
		{"unknown field", strings.Replace(validBody, `"entity"`, `"entityy"`, 1), http.StatusBadRequest, "COM-0001", ""},
		{"missing matrix row", strings.Replace(validBody, `"entityB":"B"`, `"entityB":"C"`, 1), http.StatusUnprocessableEntity, "IC-0805", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(router(api.New()), journalPath, tenantID, api.DutyPost, c.body)
			var p struct {
				Code   string            `json:"code"`
				Errors map[string]string `json:"errors"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &p)
			if rec.Code != c.status || p.Code != c.code {
				t.Fatalf("status %d body %s; want %d %s", rec.Code, rec.Body.String(), c.status, c.code)
			}
			if c.field != "" && p.Errors[c.field] == "" {
				t.Fatalf("errors = %v; want an entry for %s", p.Errors, c.field)
			}
		})
	}
}

type classifyBody struct {
	FXTolerancePct string `json:"fxTolerancePct"`
	Items          []struct {
		ID, Status, Difference string
		ToleranceApplied       bool
	} `json:"items"`
	Summary struct{ Matched, Timing, Mismatch int } `json:"summary"`
}

func item(id, a, b, extra string) string {
	return fmt.Sprintf(`{"id":%q,"entityA":"A","entityB":"B","period":"2026-11","currency":"USD","amountA":%q,"amountB":%q%s}`, id, a, b, extra)
}

func TestClassify_StatusesAndSummary(t *testing.T) {
	body := `{"items":[` + strings.Join([]string{
		item("matched", "1000.00", "1000.00", ""),
		item("fx", "1000.00", "996.00", `,"fxApplies":true`),
		item("timing", "250.00", "0", `,"nextPeriodB":"250.00"`),
		item("missing", "250.00", "0", ""),
		item("wrong-amount", "100.00", "105.00", ""),
	}, ",") + `]}`
	a := api.New()
	rec := do(router(a), classifyPath, tenantID, api.DutyReconcile, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var got classifyBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"matched": "Matched", "fx": "Matched", "timing": "Timing", "missing": "Mismatch", "wrong-amount": "Mismatch"}
	for _, it := range got.Items {
		if it.Status != want[it.ID] {
			t.Fatalf("item %s = %s; want %s", it.ID, it.Status, want[it.ID])
		}
		if it.ID == "fx" && (!it.ToleranceApplied || it.Difference != "4.00") {
			t.Fatalf("fx item = %+v; want tolerance applied, difference 4.00", it)
		}
	}
	if got.FXTolerancePct != "0.5" || got.Summary.Matched != 2 || got.Summary.Timing != 1 || got.Summary.Mismatch != 2 {
		t.Fatalf("tolerance %s summary %+v", got.FXTolerancePct, got.Summary)
	}

	mrec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(mrec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(mrec.Body.String(), "intercompany_recon_items_classified_total 5") ||
		!strings.Contains(mrec.Body.String(), "intercompany_recon_mismatches_total 2") {
		t.Fatalf("metrics = %s", mrec.Body.String())
	}
}

func TestClassify_ZeroToleranceTurnsFXDifferenceIntoMismatch(t *testing.T) {
	body := `{"fxTolerancePct":"0","items":[` + item("fx", "1000.00", "996.00", `,"fxApplies":true`) + `]}`
	rec := do(router(api.New()), classifyPath, tenantID, api.DutyReconcile, body)
	var got classifyBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Items) != 1 || got.Items[0].Status != "Mismatch" {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestClassify_Errors(t *testing.T) {
	cases := []struct {
		name, body, duties string
		status             int
		field              string
	}{
		{"no items", `{"items":[]}`, api.DutyReconcile, http.StatusBadRequest, "items"},
		{"exponent tolerance", `{"fxTolerancePct":"5e-1","items":[` + item("x", "1", "1", "") + `]}`, api.DutyReconcile, http.StatusBadRequest, "fxTolerancePct"},
		{"tolerance above 5", `{"fxTolerancePct":"6","items":[` + item("x", "1", "1", "") + `]}`, api.DutyReconcile, http.StatusBadRequest, "fxTolerancePct"},
		{"bad amount", `{"items":[` + item("x", "ten", "1", "") + `]}`, api.DutyReconcile, http.StatusBadRequest, "items[0].amountA"},
		{"same entity", `{"items":[` + strings.Replace(item("x", "1", "1", ""), `"entityB":"B"`, `"entityB":"A"`, 1) + `]}`, api.DutyReconcile, http.StatusBadRequest, "items[0].entityB"},
		{"bad period", `{"items":[` + strings.Replace(item("x", "1", "1", ""), `"2026-11"`, `"Nov"`, 1) + `]}`, api.DutyReconcile, http.StatusBadRequest, "items[0].period"},
		{"post duty is not reconcile", `{"items":[` + item("x", "1", "1", "") + `]}`, api.DutyPost, http.StatusForbidden, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(router(api.New()), classifyPath, tenantID, c.duties, c.body)
			var p struct {
				Errors map[string]string `json:"errors"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &p)
			if rec.Code != c.status || (c.field != "" && p.Errors[c.field] == "") {
				t.Fatalf("status %d body %s; want %d with error on %s", rec.Code, rec.Body.String(), c.status, c.field)
			}
		})
	}
}
