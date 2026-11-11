package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/store"
)

func requisitionCheckBody(account, center, date string) string {
	return `{"legalEntityId":"` + docID + `","requisitionId":"` + docID + `","currency":"USD","lines":[{"lineId":"` + lineID + `","glAccount":"` + account + `","costCenter":"` + center + `","amount":"1200.50","requestedDate":"` + date + `"}]}`
}

func TestRequisitionCheck_ResolvesTheRuleDimensionAndSpan(t *testing.T) {
	f := &fakeStore{rule: store.Rule{ID: ruleID, Code: "DEPT", Currency: "USD"}, ruleFound: true}
	rec := serve(t, f, http.MethodPost, "/v1/budget/requisition-checks", requisitionCheckBody("6100", "CC-20", "2027-03-15"), "budget.requisition.check", "rc-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("check: %d %s", rec.Code, rec.Body)
	}
	got := f.checkIn
	if got.RuleVersionID != ruleID || got.DocumentType != "Requisition" || got.DocumentID != docID || len(got.Lines) != 1 ||
		got.Lines[0].DimensionKey != "6100|CC-20" || got.Lines[0].SpanKey != "2027-03" || got.Lines[0].Amount.String() != "1200.5" || f.checkOverride {
		t.Fatalf("check request = %+v (override %v); want rule %s, dimension 6100|CC-20, span 2027-03, no override", got, f.checkOverride, ruleID)
	}
}

func TestRequisitionCheck_NoRuleMeansNotControlledAndAWrongCurrencyIsRefused(t *testing.T) {
	f := &fakeStore{}
	rec := serve(t, f, http.MethodPost, "/v1/budget/requisition-checks", requisitionCheckBody("6100", "CC-20", "2027-03-15"), "budget.requisition.check", "rc-2")
	var out struct {
		Result string `json:"result"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || out.Result != ResultNotControlled || f.calls != 0 {
		t.Fatalf("no rule: %d %s (%d store calls); want 200 %s and nothing checked", rec.Code, rec.Body, f.calls, ResultNotControlled)
	}
	f = &fakeStore{rule: store.Rule{ID: ruleID, Code: "DEPT", Currency: "EUR"}, ruleFound: true}
	rec = serve(t, f, http.MethodPost, "/v1/budget/requisition-checks", requisitionCheckBody("6100", "CC-20", "2027-03-15"), "budget.requisition.check", "rc-3")
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != "BUD-0080" || f.calls != 0 {
		t.Fatalf("currency mismatch: %d %s", rec.Code, rec.Body)
	}
}

func TestRequisitionCheck_ValidatesAndNeedsTheServiceDuty(t *testing.T) {
	f := &fakeStore{rule: store.Rule{ID: ruleID, Currency: "USD"}, ruleFound: true}
	if rec := serve(t, f, http.MethodPost, "/v1/budget/requisition-checks", requisitionCheckBody("6100", "CC-20", "2027-03-15"), "budget.check", "rc-4"); rec.Code != http.StatusForbidden {
		t.Fatalf("with budget.check only: %d, want 403", rec.Code)
	}
	if rec := serve(t, f, http.MethodPost, "/v1/budget/requisition-checks", requisitionCheckBody("6100", "CC-20", "2027-03-15"), "budget.requisition.check", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("without Idempotency-Key: %d, want 400", rec.Code)
	}
	for name, c := range map[string]struct{ body, field string }{
		"bad account": {requisitionCheckBody("has space", "CC-20", "2027-03-15"), "lines[0].glAccount"},
		"no center":   {requisitionCheckBody("6100", "", "2027-03-15"), "lines[0].costCenter"},
		"bad date":    {requisitionCheckBody("6100", "CC-20", "soon"), "lines[0].requestedDate"},
		"no lines":    {`{"legalEntityId":"` + docID + `","requisitionId":"` + docID + `","currency":"USD","lines":[]}`, "lines"},
	} {
		rec := serve(t, f, http.MethodPost, "/v1/budget/requisition-checks", c.body, "budget.requisition.check", "rc-5")
		var p httpx.Problem
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if rec.Code != http.StatusBadRequest || p.Errors[c.field] == "" {
			t.Errorf("%s: status %d errors %v, want 400 naming %s", name, rec.Code, p.Errors, c.field)
		}
	}
}

func TestRequisitionLineRelease_GivesBackTheLinesPreEncumbrance(t *testing.T) {
	f := &fakeStore{}
	body := `{"requisitionId":"` + docID + `","requisitionLineId":"` + lineID + `"}`
	if rec := serve(t, f, http.MethodPost, "/v1/budget/requisition-checks:release-line", body, "budget.requisition.check", "rl-1"); rec.Code != http.StatusOK || f.releaseLine != lineID {
		t.Fatalf("release: %d %s (line %q)", rec.Code, rec.Body, f.releaseLine)
	}
	if rec := serve(t, f, http.MethodPost, "/v1/budget/requisition-checks:release-line", `{"requisitionId":"`+docID+`"}`, "budget.requisition.check", "rl-2"); rec.Code != http.StatusBadRequest {
		t.Fatalf("without the line: %d, want 400", rec.Code)
	}
}
