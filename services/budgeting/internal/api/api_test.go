package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/store"
)

// fakeStore records the last command and replays a fixed body.
type fakeStore struct {
	checkOverride bool
	relieveIn     store.RelieveRequest
	releaseIn     store.ReleaseRequest
	calls         int
	lastFilter    string
}

func (f *fakeStore) CreateRule(context.Context, string, string, store.NewRule) (int, []byte, error) {
	f.calls++
	return http.StatusCreated, []byte(`{"id":"r"}`), nil
}

func (f *fakeStore) CheckBudget(_ context.Context, _, _ string, _ store.CheckRequest, override bool) (int, []byte, error) {
	f.calls++
	f.checkOverride = override
	return http.StatusOK, []byte(`{"result":"pass"}`), nil
}

func (f *fakeStore) RelieveEncumbrance(_ context.Context, _, _ string, in store.RelieveRequest) (int, []byte, error) {
	f.calls++
	f.relieveIn = in
	return http.StatusOK, []byte(`{}`), nil
}

func (f *fakeStore) ConvertPreEncumbrance(_ context.Context, _, _ string, in store.ConvertRequest) (int, []byte, error) {
	return http.StatusOK, []byte(`{"encumbranceId":"` + in.EncumbranceID + `"}`), nil
}

func (f *fakeStore) ReleaseEncumbrance(_ context.Context, _, _ string, in store.ReleaseRequest) (int, []byte, error) {
	f.calls++
	f.releaseIn = in
	return http.StatusOK, []byte(`{"status":"Released"}`), nil
}

func (f *fakeStore) PostRegisterEntry(context.Context, string, string, store.RegisterRequest) (int, []byte, error) {
	f.calls++
	return http.StatusCreated, []byte(`{}`), nil
}

func (f *fakeStore) ListRules(_ context.Context, le string) ([]store.Rule, error) {
	f.calls++
	f.lastFilter = le
	return []store.Rule{{ID: ruleID, Code: "DEPT", Version: 2}}, nil
}

func (f *fakeStore) GetRule(_ context.Context, id string) (store.Rule, error) {
	f.calls++
	if id != ruleID {
		return store.Rule{}, httpx.NewProblem(http.StatusNotFound, store.CodeRuleNotFound, "budget control rule version not found")
	}
	return store.Rule{ID: ruleID, Code: "DEPT"}, nil
}

func (f *fakeStore) ListRegisterEntries(context.Context, string) ([]store.RegisterEntry, error) {
	f.calls++
	return []store.RegisterEntry{}, nil
}

func (f *fakeStore) Available(_ context.Context, rule, dim, span string) ([]store.BalanceView, error) {
	f.calls++
	f.lastFilter = rule + "|" + dim + "|" + span
	return []store.BalanceView{{ID: "b", Available: decimal.NewFromInt(10000)}}, nil
}

func (f *fakeStore) ListEncumbrances(_ context.Context, _, status string) ([]store.EncumbranceView, error) {
	f.calls++
	f.lastFilter = status
	return []store.EncumbranceView{}, nil
}

func (f *fakeStore) GetEncumbrance(context.Context, string) (store.EncumbranceView, error) {
	f.calls++
	return store.EncumbranceView{}, nil
}

func (f *fakeStore) ListChecks(context.Context, string, string) ([]store.CheckResponse, error) {
	f.calls++
	return []store.CheckResponse{}, nil
}

func (f *fakeStore) GetCheck(context.Context, string) (store.CheckResponse, error) {
	f.calls++
	return store.CheckResponse{}, nil
}

func (f *fakeStore) Ping(context.Context) error { return nil }

const (
	tenantID = "00000000-0000-0000-0000-000000000001"
	ruleID   = "0196a6a4-0000-7000-8000-000000000001"
	lineID   = "0196a6a4-0000-7000-8000-000000000002"
	docID    = "0196a6a4-0000-7000-8000-000000000003"
)

func serve(t *testing.T, st Store, method, path, body string, duties string, key string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	New(st).Register(r)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set(idempotencyKey, key)
	}
	// httpx.NewRouter's tenant middleware is skipped here; the principal is attached directly.
	req = req.WithContext(testkit.Ctx(tenantID, strings.Split(duties, ",")...))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func problemCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem: %v (%s)", err, rec.Body.String())
	}
	return p.Code
}

const checkBody = `{"ruleVersionId":"` + ruleID + `","documentId":"` + docID + `","documentType":"PurchaseOrder",
	"lines":[{"lineId":"` + lineID + `","dimensionKey":"dept-10","spanKey":"FY2026","amount":"12000.00"}]}`

func TestCheck_RequiresIdempotencyKey(t *testing.T) {
	rec := serve(t, &fakeStore{}, http.MethodPost, "/v1/budget/checks", checkBody, "budget.check", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCheck_RejectsBadLines(t *testing.T) {
	body := `{"ruleVersionId":"nope","documentId":"` + docID + `","documentType":"Bogus","lines":[{"lineId":"x","dimensionKey":"","spanKey":"FY","amount":"-1"}]}`
	rec := serve(t, &fakeStore{}, http.MethodPost, "/v1/budget/checks", body, "budget.check", "k1")
	if rec.Code != http.StatusBadRequest || problemCode(t, rec) != "COM-0001" {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var p httpx.Problem
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	for _, field := range []string{"ruleVersionId", "documentType", "lines[0].lineId", "lines[0].dimensionKey", "lines[0].amount"} {
		if _, ok := p.Errors[field]; !ok {
			t.Errorf("missing field error %s in %v", field, p.Errors)
		}
	}
}

func TestCheck_NeedsDutyAndPassesOverride(t *testing.T) {
	st := &fakeStore{}
	if rec := serve(t, st, http.MethodPost, "/v1/budget/checks", checkBody, "budget.read", "k1"); rec.Code != http.StatusForbidden {
		t.Fatalf("without budget.check status = %d, want 403", rec.Code)
	}
	rec := serve(t, st, http.MethodPost, "/v1/budget/checks", checkBody, "budget.check,budget.override", "k2")
	if rec.Code != http.StatusOK || rec.Body.String() != `{"result":"pass"}` {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if !st.checkOverride {
		t.Fatal("override duty was not passed to the store")
	}
}

func TestRelieve_MatchedShareIsQuantityTimesPrice(t *testing.T) {
	st := &fakeStore{}
	body := `{"invoiceId":"` + docID + `","invoiceAmount":"520.00","invoiceQuantity":"10","poUnitPrice":"50.00"}`
	rec := serve(t, st, http.MethodPost, "/v1/budget/encumbrances/"+lineID+":relieve", body, "budget.check", "k3")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if !st.relieveIn.MatchedShare.Equal(decimal.NewFromInt(500)) || !st.relieveIn.InvoiceAmount.Equal(decimal.RequireFromString("520")) {
		t.Fatalf("store input = %+v", st.relieveIn)
	}
}

// FR-38.5: a PO line cancellation or close names the purchase order and the
// reason; the route needs budget.check and an Idempotency-Key.
func TestRelease_ValidatesAndNeedsDuty(t *testing.T) {
	path := "/v1/budget/encumbrances/" + lineID + ":release"
	good := `{"documentId":"` + docID + `","reason":"Cancelled"}`
	st := &fakeStore{}
	rec := serve(t, st, http.MethodPost, path, good, "budget.check", "rel-1")
	if rec.Code != http.StatusOK || st.releaseIn.EncumbranceID != lineID || st.releaseIn.Reason != store.ReleaseCancelled || st.releaseIn.DocumentID != docID {
		t.Fatalf("status = %d input = %+v body = %s", rec.Code, st.releaseIn, rec.Body.String())
	}
	cases := map[string]struct{ body, field string }{
		"unknown reason": {`{"documentId":"` + docID + `","reason":"Expired"}`, "reason"},
		"no document":    {`{"reason":"Closed"}`, "documentId"},
	}
	for name, c := range cases {
		st := &fakeStore{}
		rec := serve(t, st, http.MethodPost, path, c.body, "budget.check", "rel-2")
		var p httpx.Problem
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if rec.Code != http.StatusBadRequest || p.Errors[c.field] == "" || st.calls != 0 {
			t.Errorf("%s: status = %d errors = %v calls = %d", name, rec.Code, p.Errors, st.calls)
		}
	}
	if rec := serve(t, &fakeStore{}, http.MethodPost, path, good, "budget.check", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("without Idempotency-Key: status = %d, want 400", rec.Code)
	}
	if rec := serve(t, &fakeStore{}, http.MethodPost, path, good, "budget.view", "rel-3"); rec.Code != http.StatusForbidden {
		t.Errorf("without budget.check: status = %d, want 403", rec.Code)
	}
	st = &fakeStore{}
	rec = serve(t, st, http.MethodPost, "/v1/budget/encumbrances/not-a-uuid:release", good, "budget.check", "rel-4")
	if rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodeEncumbranceNotFound || st.calls != 0 {
		t.Errorf("bad id: status = %d calls = %d", rec.Code, st.calls)
	}
	st = &fakeStore{}
	if rec := serve(t, st, http.MethodGet, "/v1/budget/encumbrances?rule="+ruleID+"&status=Released", "", "budget.view", ""); rec.Code != http.StatusOK || st.lastFilter != "Released" {
		t.Errorf("Released filter: status = %d filter = %q", rec.Code, st.lastFilter)
	}
}

func TestCreateRule_ValidatesThresholds(t *testing.T) {
	body := `{"legalEntityId":"` + tenantID + `","code":"DEPT","warnPct":"10","blockPct":"5","currency":"USD","effectiveFrom":"2026-01-01"}`
	rec := serve(t, &fakeStore{}, http.MethodPost, "/v1/budget/control-rules", body, "budget.configure", "k4")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var p httpx.Problem
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if p.Errors["blockPct"] == "" {
		t.Fatalf("blockPct below warnPct not reported: %v", p.Errors)
	}
}

func TestPostRegisterEntry_UnknownTypeRejected(t *testing.T) {
	body := `{"ruleVersionId":"` + ruleID + `","entryType":"Encumbrance","lines":[{"dimensionKey":"d","spanKey":"s","amount":"1"}]}`
	rec := serve(t, &fakeStore{}, http.MethodPost, "/v1/budget/register-entries", body, "budget.maintain", "k5")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// The prior reservation is read from the stored encumbrance; a caller cannot
// supply one to shrink the net change it is checked for.
func TestCheck_RejectsCallerSuppliedPrior(t *testing.T) {
	st := &fakeStore{}
	body := `{"ruleVersionId":"` + ruleID + `","documentId":"` + docID + `","documentType":"PurchaseOrder",
	"lines":[{"lineId":"` + lineID + `","dimensionKey":"dept-10","spanKey":"FY2026","amount":"12000.00","prior":"12000.00"}]}`
	rec := serve(t, st, http.MethodPost, "/v1/budget/checks", body, "budget.check", "k6")
	if rec.Code != http.StatusBadRequest || problemCode(t, rec) != "COM-0001" || st.calls != 0 {
		t.Fatalf("status = %d calls = %d body = %s", rec.Code, st.calls, rec.Body.String())
	}
}

func TestCheck_RejectsDuplicateLineIDs(t *testing.T) {
	st := &fakeStore{}
	line := `{"lineId":"` + lineID + `","dimensionKey":"dept-10","spanKey":"FY2026","amount":"1"}`
	body := `{"ruleVersionId":"` + ruleID + `","documentId":"` + docID + `","documentType":"PurchaseOrder","lines":[` + line + `,` + line + `]}`
	rec := serve(t, st, http.MethodPost, "/v1/budget/checks", body, "budget.check", "k7")
	var p httpx.Problem
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != http.StatusBadRequest || p.Errors["lines[1].lineId"] == "" || st.calls != 0 {
		t.Fatalf("status = %d errors = %v", rec.Code, p.Errors)
	}
}

func TestReads_NeedViewDutyAndRule(t *testing.T) {
	st := &fakeStore{}
	if rec := serve(t, st, http.MethodGet, "/v1/budget/available?rule="+ruleID, "", "budget.check", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("without budget.view status = %d, want 403", rec.Code)
	}
	rec := serve(t, st, http.MethodGet, "/v1/budget/available", "", "budget.view", "")
	if rec.Code != http.StatusBadRequest || problemCode(t, rec) != "COM-0001" {
		t.Fatalf("missing rule: status = %d body = %s", rec.Code, rec.Body.String())
	}
	rec = serve(t, st, http.MethodGet, "/v1/budget/available?rule="+ruleID+"&dims=dept-10&span=FY2026", "", "budget.view", "")
	if rec.Code != http.StatusOK || st.lastFilter != ruleID+"|dept-10|FY2026" {
		t.Fatalf("status = %d filter = %q body = %s", rec.Code, st.lastFilter, rec.Body.String())
	}
	var body struct {
		Items []store.BalanceView `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Items) != 1 || !body.Items[0].Available.Equal(decimal.NewFromInt(10000)) {
		t.Fatalf("items = %+v (%v)", body.Items, err)
	}
}

func TestReads_RejectBadFiltersAndIDs(t *testing.T) {
	st := &fakeStore{}
	if rec := serve(t, st, http.MethodGet, "/v1/budget/encumbrances?rule="+ruleID+"&status=Bogus", "", "budget.view", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status filter: %d", rec.Code)
	}
	if rec := serve(t, st, http.MethodGet, "/v1/budget/control-rules?legalEntityId=nope", "", "budget.view", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad legal entity filter: %d", rec.Code)
	}
	if rec := serve(t, st, http.MethodGet, "/v1/budget/control-rules/not-a-uuid", "", "budget.view", ""); rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodeRuleNotFound {
		t.Fatalf("bad rule id: %d", rec.Code)
	}
	if rec := serve(t, st, http.MethodGet, "/v1/budget/control-rules/"+lineID, "", "budget.view", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown rule: %d", rec.Code)
	}
	if st.calls != 1 {
		t.Fatalf("store calls = %d, want 1 (only the well-formed unknown id reaches the store)", st.calls)
	}
}
