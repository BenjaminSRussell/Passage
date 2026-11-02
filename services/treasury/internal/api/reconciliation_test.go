package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

const (
	testStatement = "0190b7a0-0000-7000-8000-0000000000a1"
	testRuleID    = "0190b7a0-0000-7000-8000-0000000000a2"
	testMatchID   = "0190b7a0-0000-7000-8000-0000000000a3"
)

func (f *fakeStore) CreateReconRule(_ context.Context, in store.NewReconRule, _ string) (store.ReconRule, error) {
	f.lastRule = in
	return store.ReconRule{ID: testRuleID, LegalEntityID: in.LegalEntityID, Name: in.Rule.Name, Priority: in.Rule.Priority,
		Weights: in.Rule.Weights, AmountTolerance: in.Rule.Tolerance.Amount.String(), Active: true, Version: 1}, nil
}

func (f *fakeStore) ListReconRules(context.Context, string, string) ([]store.ReconRule, error) {
	return []store.ReconRule{}, nil
}

func (f *fakeStore) GetReconRule(_ context.Context, id string) (store.ReconRule, error) {
	return store.ReconRule{ID: id, Active: true, Version: 1}, nil
}

func (f *fakeStore) DeactivateReconRule(_ context.Context, id string, version int) (store.ReconRule, error) {
	if version != 1 {
		return store.ReconRule{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
	}
	return store.ReconRule{ID: id, Version: 2}, nil
}

func (f *fakeStore) RunReconciliation(_ context.Context, in store.RunInput, _ string) (store.RunResult, error) {
	f.lastRun = in
	return store.RunResult{Matches: []store.Match{{ID: testMatchID}}, Suggestions: []store.RunSuggestion{}, LinesConsidered: 1, ItemsConsidered: len(in.Items)}, f.matchErr
}

func (f *fakeStore) CreateManualMatch(_ context.Context, in store.NewMatch, _ string) (store.Match, error) {
	f.lastMatch = in
	return store.Match{ID: testMatchID, Kind: "manual", Status: "matched", Version: 1}, f.matchErr
}

func (f *fakeStore) ListMatches(context.Context, string) ([]store.Match, error) {
	return []store.Match{}, nil
}

func (f *fakeStore) GetMatch(_ context.Context, id string) (store.Match, error) {
	return store.Match{ID: id, Version: 1}, nil
}

func (f *fakeStore) Unmatch(_ context.Context, id string, version int, _ string) (store.Match, error) {
	if version != 1 {
		return store.Match{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
	}
	return store.Match{ID: id, Status: "unmatched", Version: 2}, nil
}

func jsonBody(t *testing.T, v any) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewBuffer(b)
}

func ruleBody() map[string]any {
	return map[string]any{
		"legalEntityId": testLE, "name": "balanced", "priority": 1,
		"weights":         map[string]int{"amount": 40, "date": 20, "reference": 25, "counterparty": 15},
		"amountTolerance": "0.05", "dateWindowDays": 2,
	}
}

func runBody() map[string]any {
	return map[string]any{"statementId": testStatement, "items": []map[string]any{
		{"type": "ar", "id": "inv-1", "currency": "EUR", "amount": "120.00", "valueDate": "2026-10-22", "reference": "INV-1"},
	}}
}

func TestCreateReconRule_StoresRuleAndRejectsInvalid(t *testing.T) {
	h, fs := newTestServer(t)
	post := func(body map[string]any) int {
		return call(h, http.MethodPost, "/v1/treasury/reconciliation-rules", jsonBody(t, body), "application/json", map[string]string{idempotencyKey: "k1"}).Code
	}
	if code := post(ruleBody()); code != http.StatusCreated {
		t.Fatalf("status %d", code)
	}
	if fs.lastRule.Rule.Priority != 1 || fs.lastRule.Rule.Weights.Amount != 40 || fs.lastRule.Rule.Tolerance.DateWindowDays != 2 {
		t.Fatalf("rule not passed through: %+v", fs.lastRule)
	}
	mutate := map[string]func(map[string]any){
		"weights not 100": func(b map[string]any) {
			b["weights"] = map[string]int{"amount": 40, "date": 20, "reference": 25, "counterparty": 10}
		},
		"bad tolerance":       func(b map[string]any) { b["amountTolerance"] = "abc" },
		"negative tolerance":  func(b map[string]any) { b["amountTolerance"] = "-1" },
		"window too wide":     func(b map[string]any) { b["dateWindowDays"] = 99 },
		"priority zero":       func(b map[string]any) { b["priority"] = 0 },
		"bad legal entity":    func(b map[string]any) { b["legalEntityId"] = "x" },
		"bad bank account id": func(b map[string]any) { b["bankAccountId"] = "x" },
	}
	for name, m := range mutate {
		body := ruleBody()
		m(body)
		if code := post(body); code != http.StatusBadRequest {
			t.Errorf("%s: status %d; want 400", name, code)
		}
	}
}

func TestCreateReconRule_NeedsKeyDutyAndScope(t *testing.T) {
	h, _ := newTestServer(t)
	if rec := call(h, http.MethodPost, "/v1/treasury/reconciliation-rules", jsonBody(t, ruleBody()), "application/json", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: status %d", rec.Code)
	}
	noDuty := map[string]string{idempotencyKey: "k", tenant.HeaderDuties: "treasury.view"}
	if rec := call(h, http.MethodPost, "/v1/treasury/reconciliation-rules", jsonBody(t, ruleBody()), "application/json", noDuty); rec.Code != http.StatusForbidden {
		t.Fatalf("view duty only: status %d", rec.Code)
	}
	other := ruleBody()
	other["legalEntityId"] = "0190b7a0-0000-7000-8000-0000000000ff"
	if rec := call(h, http.MethodPost, "/v1/treasury/reconciliation-rules", jsonBody(t, other), "application/json", map[string]string{idempotencyKey: "k"}); rec.Code != http.StatusForbidden {
		t.Fatalf("other legal entity: status %d", rec.Code)
	}
}

func TestRunReconciliation_ValidatesItemsAndReturnsResult(t *testing.T) {
	h, fs := newTestServer(t)
	headers := map[string]string{idempotencyKey: "run-1"}
	rec := call(h, http.MethodPost, "/v1/treasury/reconciliation-runs", jsonBody(t, runBody()), "application/json", headers)
	if rec.Code != http.StatusCreated || fs.lastRun.StatementID != testStatement || len(fs.lastRun.Items) != 1 {
		t.Fatalf("status %d run %+v body %s", rec.Code, fs.lastRun, rec.Body)
	}
	item := func(patch map[string]any) map[string]any {
		it := runBody()["items"].([]map[string]any)[0]
		for k, v := range patch {
			it[k] = v
		}
		return map[string]any{"statementId": testStatement, "items": []map[string]any{it}}
	}
	dup := runBody()
	dup["items"] = append(dup["items"].([]map[string]any), dup["items"].([]map[string]any)[0])
	bad := map[string]map[string]any{
		"unknown type":   item(map[string]any{"type": "po"}),
		"zero amount":    item(map[string]any{"amount": "0"}),
		"bad amount":     item(map[string]any{"amount": "x"}),
		"bad currency":   item(map[string]any{"currency": "eur"}),
		"bad value date": item(map[string]any{"valueDate": "22.10.2026"}),
		"empty id":       item(map[string]any{"id": ""}),
		"duplicate item": dup,
		"no items":       {"statementId": testStatement, "items": []map[string]any{}},
		"bad statement":  {"statementId": "x", "items": runBody()["items"]},
	}
	for name, body := range bad {
		if rec := call(h, http.MethodPost, "/v1/treasury/reconciliation-runs", jsonBody(t, body), "application/json", headers); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d; want 400", name, rec.Code)
		}
	}
	if rec := call(h, http.MethodPost, "/v1/treasury/reconciliation-runs", jsonBody(t, runBody()), "application/json", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("missing Idempotency-Key: status %d", rec.Code)
	}
}

func TestRunReconciliation_MapsStoreProblems(t *testing.T) {
	h, fs := newTestServer(t)
	fs.matchErr = httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeNoRules, "test")
	rec := call(h, http.MethodPost, "/v1/treasury/reconciliation-runs", jsonBody(t, runBody()), "application/json", map[string]string{idempotencyKey: "k"})
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != store.CodeNoRules {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}

func TestManualMatch_ValidatesAndCreates(t *testing.T) {
	h, fs := newTestServer(t)
	good := map[string]any{"statementId": testStatement, "lineNos": []int{3, 4}, "reason": "split remittance",
		"items": []map[string]any{{"type": "ar", "id": "inv-1", "currency": "EUR", "amount": "100.00"}}}
	headers := map[string]string{idempotencyKey: "m-1"}
	rec := call(h, http.MethodPost, "/v1/treasury/reconciliation-matches", jsonBody(t, good), "application/json", headers)
	if rec.Code != http.StatusCreated || len(fs.lastMatch.LineNos) != 2 || fs.lastMatch.Reason != "split remittance" || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("status %d match %+v etag %q", rec.Code, fs.lastMatch, rec.Header().Get("ETag"))
	}
	patch := func(k string, v any) map[string]any {
		b := map[string]any{}
		for key, val := range good {
			b[key] = val
		}
		b[k] = v
		return b
	}
	bad := map[string]map[string]any{
		"no reason":       patch("reason", " "),
		"long reason":     patch("reason", strings.Repeat("x", 201)),
		"no lines":        patch("lineNos", []int{}),
		"duplicate lines": patch("lineNos", []int{1, 1}),
		"line zero":       patch("lineNos", []int{0}),
		"no items":        patch("items", []map[string]any{}),
		"bad item type":   patch("items", []map[string]any{{"type": "x", "id": "a", "currency": "EUR", "amount": "1"}}),
		"bad statement":   patch("statementId", "x"),
	}
	for name, body := range bad {
		if rec := call(h, http.MethodPost, "/v1/treasury/reconciliation-matches", jsonBody(t, body), "application/json", headers); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d; want 400", name, rec.Code)
		}
	}
	fs.matchErr = httpx.NewProblem(http.StatusConflict, store.CodeLineMatched, "test")
	rec = call(h, http.MethodPost, "/v1/treasury/reconciliation-matches", jsonBody(t, good), "application/json", headers)
	if rec.Code != http.StatusConflict || problemCode(t, rec) != store.CodeLineMatched {
		t.Fatalf("already matched: status %d body %s", rec.Code, rec.Body)
	}
}

func TestUnmatch_NeedsIfMatchAndReason(t *testing.T) {
	h, _ := newTestServer(t)
	path := "/v1/treasury/reconciliation-matches/" + testMatchID + ":unmatch"
	reason := jsonBody(t, map[string]string{"reason": "wrong invoice"})
	if rec := call(h, http.MethodPost, path, reason, "application/json", nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: status %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, path, jsonBody(t, map[string]string{"reason": ""}), "application/json", map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no reason: status %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, path, jsonBody(t, map[string]string{"reason": "wrong invoice"}), "application/json", map[string]string{"If-Match": `"7"`}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale: status %d", rec.Code)
	}
	rec := call(h, http.MethodPost, path, jsonBody(t, map[string]string{"reason": "wrong invoice"}), "application/json", map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("status %d etag %q body %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
}

func TestReconciliationReads_NeedValidIDsAndQueries(t *testing.T) {
	h, _ := newTestServer(t)
	for path, want := range map[string]int{
		"/v1/treasury/reconciliation-rules":                                              http.StatusBadRequest,
		"/v1/treasury/reconciliation-rules?legalEntityId=" + testLE:                      http.StatusOK,
		"/v1/treasury/reconciliation-rules?legalEntityId=" + testLE + "&bankAccountId=x": http.StatusBadRequest,
		"/v1/treasury/reconciliation-rules/not-a-uuid":                                   http.StatusNotFound,
		"/v1/treasury/reconciliation-rules/" + testRuleID:                                http.StatusOK,
		"/v1/treasury/reconciliation-matches":                                            http.StatusBadRequest,
		"/v1/treasury/reconciliation-matches?statementId=" + testStatement:               http.StatusOK,
		"/v1/treasury/reconciliation-matches/not-a-uuid":                                 http.StatusNotFound,
		"/v1/treasury/reconciliation-matches/" + testMatchID:                             http.StatusOK,
	} {
		if rec := call(h, http.MethodGet, path, nil, "", nil); rec.Code != want {
			t.Errorf("GET %s: status %d; want %d", path, rec.Code, want)
		}
	}
	if rec := call(h, http.MethodPost, "/v1/treasury/reconciliation-rules/"+testRuleID+":deactivate", nil, "", map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusOK {
		t.Errorf("deactivate: status %d", rec.Code)
	}
	if rec := call(h, http.MethodPost, "/v1/treasury/reconciliation-rules/"+testRuleID+":deactivate", nil, "", nil); rec.Code != http.StatusPreconditionRequired {
		t.Errorf("deactivate without If-Match: status %d", rec.Code)
	}
}
