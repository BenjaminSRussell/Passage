package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/lean/internal/store"
)

var countPath = "/v1/lean/kanban-rules/" + ruleID + "/count-check"

func fixedRule(n, leadTimeH int, fixedQty string) *store.Rule {
	return &store.Rule{ID: ruleID, Code: "SAUCE-FIX", Replenishment: "fixed", NumberOfKanbans: ptr(n), LeadTimeH: leadTimeH, FixedQty: dec(fixedQty)}
}

// A fixed rule with 5 cards of 20, a 48 h lead time and a demand of 10 a day
// with 10 % safety needs ⌈10 × 2 × 1.1 / 20⌉ = 2 cards: 60 % away, reported.
func TestCountCheck_ReportsDeviationAboveTheLimit(t *testing.T) {
	fs := &fakeStore{rule: fixedRule(5, 48, "20")}
	rec := call{method: "GET", path: countPath + "?dailyDemand=10&safetyPct=10", duties: DutyRead}.do(newRouter(fs))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	var got CountCheck
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := CountCheck{RuleID: ruleID, RuleCode: "SAUCE-FIX", DailyDemand: "10", SafetyPct: "10", LeadTimeH: 48, FixedQty: "20",
		Configured: 5, Suggested: 2, DeviationPct: "60.00", LimitPct: 25, Reportable: true}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// Safety defaults to 0; a count 25 % away is at the limit, not above it.
func TestCountCheck_AtTheLimitIsNotReported(t *testing.T) {
	fs := &fakeStore{rule: fixedRule(4, 24, "10")}
	rec := call{method: "GET", path: countPath + "?dailyDemand=30", duties: DutyRead}.do(newRouter(fs))
	var got CountCheck
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if got.Suggested != 3 || got.DeviationPct != "25.00" || got.Reportable || got.SafetyPct != "0" {
		t.Fatalf("got %+v", got)
	}
}

func TestCountCheck_Refusals(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		duties string
		fs     *fakeStore
		status int
		code   string
	}{
		{"missing demand", countPath, DutyRead, &fakeStore{rule: fixedRule(2, 24, "10")}, http.StatusBadRequest, "COM-0001"},
		{"negative demand", countPath + "?dailyDemand=-1", DutyRead, &fakeStore{rule: fixedRule(2, 24, "10")}, http.StatusBadRequest, "COM-0001"},
		{"malformed safety", countPath + "?dailyDemand=1&safetyPct=x", DutyRead, &fakeStore{rule: fixedRule(2, 24, "10")}, http.StatusBadRequest, "COM-0001"},
		{"safety too large", countPath + "?dailyDemand=1&safetyPct=1000", DutyRead, &fakeStore{rule: fixedRule(2, 24, "10")}, http.StatusBadRequest, "COM-0001"},
		{"too many decimals", countPath + "?dailyDemand=1.0000001", DutyRead, &fakeStore{rule: fixedRule(2, 24, "10")}, http.StatusBadRequest, "COM-0001"},
		{"bad rule id", "/v1/lean/kanban-rules/nope/count-check?dailyDemand=1", DutyRead, &fakeStore{}, http.StatusBadRequest, "COM-0001"},
		{"unknown rule", countPath + "?dailyDemand=1", DutyRead,
			&fakeStore{err: httpx.NewProblem(http.StatusNotFound, store.CodeRuleNotFound, "no rule")}, http.StatusNotFound, "LEAN-0102"},
		{"event rule", countPath + "?dailyDemand=1", DutyRead,
			&fakeStore{rule: &store.Rule{ID: ruleID, Code: "SAUCE-EVT", Replenishment: "event", FixedQty: dec("20")}}, http.StatusUnprocessableEntity, CodeNotFixedRule},
		{"scheduled rule", countPath + "?dailyDemand=1", DutyRead,
			&fakeStore{rule: &store.Rule{ID: ruleID, Code: "SAUCE-SCH", Replenishment: "scheduled", FixedQty: dec("20")}}, http.StatusUnprocessableEntity, CodeNotFixedRule},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := call{method: "GET", path: tc.path, duties: tc.duties}.do(newRouter(tc.fs))
			if rec.Code != tc.status || problemOf(t, rec).Code != tc.code {
				t.Fatalf("got %d %s, want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
		})
	}
	if rec := (call{method: "GET", path: countPath + "?dailyDemand=1", duties: DutyKanbanWork}).do(newRouter(&fakeStore{rule: fixedRule(2, 24, "10")})); rec.Code != http.StatusForbidden {
		t.Fatalf("without lean.read: %d", rec.Code)
	}
	if rec := (call{method: "GET", path: countPath + "?dailyDemand=1", duties: DutyRead, noTenant: true}).do(newRouter(&fakeStore{rule: fixedRule(2, 24, "10")})); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without tenant: %d", rec.Code)
	}
}
