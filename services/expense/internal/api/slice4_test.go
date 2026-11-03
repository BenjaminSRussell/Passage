package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/expense/internal/api"
	"github.com/BenjaminSRussell/passage/services/expense/internal/domain"
	"github.com/BenjaminSRussell/passage/services/expense/internal/store"
)

// fakeReports is an in-memory reportStore that records what the handlers pass it.
type fakeReports struct {
	byKey       map[string]store.Report
	hashes      map[string]string
	refuse      bool
	lastLine    domain.Line
	lastDate    time.Time
	lastFilter  store.ReportFilter
	lastAfter   string
	lastLimit   int
	lastIfMatch int
	lastHash    string
}

func newFakeReports() *fakeReports {
	return &fakeReports{byKey: map[string]store.Report{}, hashes: map[string]string{}}
}

func blank(id, status string, version int) store.Report {
	return store.Report{ID: id, Status: status, Version: version, Currency: "USD", Total: decimal.Zero, Reimbursable: decimal.Zero, CompanyPaid: decimal.Zero,
		CreatedAt: time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC)}
}

func (f *fakeReports) CreateReport(_ context.Context, key, hash string, in store.NewReport) (store.Report, bool, error) {
	if rep, ok := f.byKey[key]; ok {
		if f.hashes[key] != hash {
			return store.Report{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reused")
		}
		return rep, true, nil
	}
	rep := blank(ids.New(), "DRAFT", 1)
	rep.LegalEntityID, rep.WorkerID, rep.Purpose, rep.Currency = in.LegalEntityID, in.WorkerID, in.Purpose, in.Currency
	f.byKey[key], f.hashes[key] = rep, hash
	return rep, false, nil
}

func (f *fakeReports) GetReport(_ context.Context, id string) (store.Report, error) {
	rep := blank(id, "DRAFT", 3)
	rep.Lines = []store.ReportLine{{
		Line:    domain.Line{ID: ids.New(), Category: "MEALS", TxnDate: time.Date(2025, 3, 3, 0, 0, 0, 0, time.UTC), Amount: decimal.RequireFromString("30"), PaidBy: domain.PaidByEmployee},
		LineNo:  1,
		Results: []domain.Result{{RuleID: "EXP-RCPT-025", Outcome: domain.OutcomeRequireReceipt, Code: domain.CodeReceipt, Satisfied: false}},
	}}
	return rep, nil
}

func (f *fakeReports) ListReports(_ context.Context, filter store.ReportFilter, after string, limit int) ([]store.Report, string, error) {
	f.lastFilter, f.lastAfter, f.lastLimit = filter, after, limit
	return []store.Report{blank(ids.New(), "DRAFT", 1)}, "", nil
}

func (f *fakeReports) AddLine(_ context.Context, reportID, _, _ string, l domain.Line, commitDate time.Time) (store.Report, bool, error) {
	f.lastLine, f.lastDate = l, commitDate
	rep := blank(reportID, "DRAFT", 2)
	rep.Total, rep.Reimbursable = l.Amount, l.Amount
	rep.Lines = []store.ReportLine{{Line: l, LineNo: 1}}
	return rep, false, nil
}

func (f *fakeReports) DeleteLine(_ context.Context, reportID, _ string, expectedVersion int, _ time.Time) (store.Report, error) {
	f.lastIfMatch = expectedVersion
	return blank(reportID, "DRAFT", 5), nil
}

func (f *fakeReports) SubmitReport(_ context.Context, reportID, _, hash string, expectedVersion int, submitDate time.Time) (store.Report, bool, error) {
	f.lastIfMatch, f.lastHash, f.lastDate = expectedVersion, hash, submitDate
	if f.refuse {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeBlock, "report has unsatisfied policy results")
		p.Errors = map[string]string{"l1/EXP-DUP-001": "Duplicate of another line"}
		return store.Report{}, false, p
	}
	rep := blank(reportID, "SUBMITTED", 4)
	at := submitDate
	rep.SubmittedAt = &at
	return rep, false, nil
}

func (f *fakeReports) DecideReport(_ context.Context, id string, approve bool, decidedOn time.Time, c store.Command) (store.Report, bool, error) {
	f.lastDate, f.lastHash, f.lastIfMatch = decidedOn, c.Hash, c.ExpectedVersion
	status := "REJECTED"
	if approve {
		status = "APPROVED"
	}
	return blank(id, status, 5), false, nil
}

var clock = time.Date(2025, 3, 4, 23, 30, 0, 0, time.FixedZone("EST", -5*3600))

func reportRouter(s *fakeReports) http.Handler {
	r := httpx.NewRouter()
	a := api.New(nil).WithClock(func() time.Time { return clock })
	if s != nil {
		a.WithReports(s)
	}
	a.Register(r)
	return r
}

type header struct{ name, value string }

func send(t *testing.T, h http.Handler, method, path, key string, body any, headers ...header) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderTenant, testkit.TenantID(1))
	req.Header.Set(tenant.HeaderDuties, dutyWrite)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	for _, hd := range headers {
		req.Header.Set(hd.name, hd.value)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type problemBody struct {
	Code   string            `json:"code"`
	Errors map[string]string `json:"errors"`
}

func problemOf(t *testing.T, rec *httptest.ResponseRecorder) problemBody {
	t.Helper()
	var p problemBody
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem %q: %v", rec.Body.String(), err)
	}
	return p
}

const dutyWrite = "expense.report.write"

func TestReportRoutesAnswer503WithoutAStore(t *testing.T) {
	rec := send(t, reportRouter(nil), http.MethodGet, "/v1/expense/reports", "", nil)
	if rec.Code != http.StatusServiceUnavailable || problemOf(t, rec).Code != "COM-0503" {
		t.Fatalf("status = %d %s; want 503 COM-0503 without a store", rec.Code, rec.Body.String())
	}
}

func TestReportRoutesNeedTheWriteDuty(t *testing.T) {
	h := reportRouter(newFakeReports())
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/expense/reports"},
		{http.MethodGet, "/v1/expense/reports/" + ids.New()},
		{http.MethodPost, "/v1/expense/reports"},
		{http.MethodDelete, "/v1/expense/reports/" + ids.New() + "/lines/" + ids.New()},
	} {
		rec := send(t, h, c.method, c.path, "k", map[string]string{}, header{tenant.HeaderDuties, "expense.card.import"})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s without %s = %d; want 403", c.method, c.path, dutyWrite, rec.Code)
		}
	}
}

func TestCreateReportValidatesKeyAndBody(t *testing.T) {
	h := reportRouter(newFakeReports())
	good := map[string]string{"legalEntityId": ids.New(), "workerId": ids.New(), "purpose": "Client visit", "currency": "USD"}
	rec := send(t, h, http.MethodPost, "/v1/expense/reports", "", good)
	if rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors["Idempotency-Key"] == "" {
		t.Fatalf("missing key = %d %s; want 400 naming Idempotency-Key", rec.Code, rec.Body.String())
	}
	bad := map[string]string{"legalEntityId": "not-a-uuid", "workerId": ids.New(), "purpose": "   ", "currency": "usd"}
	rec = send(t, h, http.MethodPost, "/v1/expense/reports", "k2", bad)
	p := problemOf(t, rec)
	if rec.Code != http.StatusBadRequest || p.Code != "COM-0001" {
		t.Fatalf("invalid body = %d %s; want 400 COM-0001", rec.Code, p.Code)
	}
	for _, field := range []string{"legalEntityId", "purpose", "currency"} {
		if p.Errors[field] == "" {
			t.Errorf("errors missing %s: %v", field, p.Errors)
		}
	}
	if p.Errors["workerId"] != "" {
		t.Errorf("a valid workerId was flagged: %v", p.Errors)
	}
}

func TestCreateReportReplaysAndRefusesAReusedKey(t *testing.T) {
	h := reportRouter(newFakeReports())
	body := map[string]string{"legalEntityId": ids.New(), "workerId": ids.New(), "purpose": "Client visit", "currency": "USD"}
	first := send(t, h, http.MethodPost, "/v1/expense/reports", "key-a", body)
	if first.Code != http.StatusCreated || first.Header().Get("ETag") != `"1"` {
		t.Fatalf("create = %d etag %q; want 201 with ETag \"1\"", first.Code, first.Header().Get("ETag"))
	}
	var created struct {
		ID, Status, Total, CreatedAt string
		Lines                        []any
	}
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil || created.Status != "DRAFT" || created.Total != "0.00" || created.Lines == nil || created.CreatedAt == "" {
		t.Fatalf("created = %s; want a DRAFT with total 0.00, an empty line list and createdAt", first.Body.String())
	}
	replay := send(t, h, http.MethodPost, "/v1/expense/reports", "key-a", body)
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay = %d header %q; want 200 with Idempotent-Replayed", replay.Code, replay.Header().Get("Idempotent-Replayed"))
	}
	body["purpose"] = "Another trip"
	if rec := send(t, h, http.MethodPost, "/v1/expense/reports", "key-a", body); rec.Code != http.StatusUnprocessableEntity || problemOf(t, rec).Code != store.CodeIdempotencyReuse {
		t.Fatalf("reused key = %d %s; want 422 %s", rec.Code, rec.Body.String(), store.CodeIdempotencyReuse)
	}
}

func TestListReportsPassesFiltersAndValidatesThem(t *testing.T) {
	fake := newFakeReports()
	h := reportRouter(fake)
	worker, cursor := ids.New(), ids.New()
	rec := send(t, h, http.MethodGet, "/v1/expense/reports?status=SUBMITTED&workerId="+worker+"&cursor="+cursor+"&limit=20", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	if fake.lastFilter.Status != "SUBMITTED" || fake.lastFilter.WorkerID != worker || fake.lastAfter != cursor || fake.lastLimit != 20 {
		t.Fatalf("store got filter %+v after %q limit %d", fake.lastFilter, fake.lastAfter, fake.lastLimit)
	}
	var page struct {
		Items      []map[string]any `json:"items"`
		NextCursor *string          `json:"nextCursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.NextCursor == nil {
		t.Fatalf("page = %s; want one item and a nextCursor field", rec.Body.String())
	}
	rec = send(t, h, http.MethodGet, "/v1/expense/reports?status=OPEN&limit=0&cursor=x&workerId=y", "", nil)
	p := problemOf(t, rec)
	if rec.Code != http.StatusBadRequest || p.Errors["status"] == "" || p.Errors["limit"] == "" || p.Errors["cursor"] == "" || p.Errors["workerId"] == "" {
		t.Fatalf("bad query = %d %v; want 400 naming status, limit, cursor and workerId", rec.Code, p.Errors)
	}
}

func TestAddLineAssignsAnIDDatesTheCommitAndRejectsBadInput(t *testing.T) {
	fake := newFakeReports()
	h := reportRouter(fake)
	path := "/v1/expense/reports/" + ids.New() + "/lines"
	body := map[string]any{"category": "MEALS", "txnDate": "2025-03-03", "amount": "42.50", "merchant": "Cafe", "paidBy": "EMPLOYEE"}
	rec := send(t, h, http.MethodPost, path, "line-1", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add line = %d; want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if !ids.Valid(fake.lastLine.ID) || !fake.lastLine.Amount.Equal(decimal.RequireFromString("42.50")) {
		t.Fatalf("line = %+v; want a generated UUID and amount 42.50", fake.lastLine)
	}
	if want := time.Date(2025, 3, 5, 0, 0, 0, 0, time.UTC); !fake.lastDate.Equal(want) {
		t.Fatalf("commit date = %s; want the UTC day of the clock, %s", fake.lastDate, want)
	}
	var out struct {
		Lines []struct {
			Amount string `json:"amount"`
			LineNo int    `json:"lineNo"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Lines) != 1 || out.Lines[0].Amount != "42.50" || out.Lines[0].LineNo != 1 {
		t.Fatalf("response = %s; want line 1 at 42.50", rec.Body.String())
	}
	for name, change := range map[string]map[string]any{
		"amount":  {"amount": "-1"},
		"id":      {"id": "line-one"},
		"txnDate": {"txnDate": "03/03/2025"},
		"paidBy":  {"paidBy": "CASH"},
	} {
		bad := map[string]any{}
		for k, v := range body {
			bad[k] = v
		}
		for k, v := range change {
			bad[k] = v
		}
		rec := send(t, h, http.MethodPost, path, "line-bad-"+name, bad)
		if rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors[name] == "" {
			t.Errorf("bad %s = %d %s; want 400 naming %s", name, rec.Code, rec.Body.String(), name)
		}
	}
	if rec := send(t, h, http.MethodPost, path, "", body); rec.Code != http.StatusBadRequest {
		t.Errorf("line without Idempotency-Key = %d; want 400", rec.Code)
	}
}

func TestGetReportShowsResultsAndSubmittable(t *testing.T) {
	h := reportRouter(newFakeReports())
	if rec := send(t, h, http.MethodGet, "/v1/expense/reports/not-a-uuid", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed id = %d; want 400", rec.Code)
	}
	rec := send(t, h, http.MethodGet, "/v1/expense/reports/"+ids.New(), "", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("get = %d etag %q; want 200 with ETag \"3\"", rec.Code, rec.Header().Get("ETag"))
	}
	var out struct {
		Submittable bool `json:"submittable"`
		Lines       []struct {
			PolicyResults []domain.Result `json:"policyResults"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Submittable || len(out.Lines) != 1 || len(out.Lines[0].PolicyResults) != 1 {
		t.Fatalf("report = %s; want one line with its receipt result and submittable false", rec.Body.String())
	}
}

func TestSubmitPassesIfMatchAndDatesTheSubmit(t *testing.T) {
	fake := newFakeReports()
	h := reportRouter(fake)
	id := ids.New()
	rec := send(t, h, http.MethodPost, "/v1/expense/reports/"+id+"/submit", "submit-1", nil, header{"If-Match", `"3"`})
	if rec.Code != http.StatusOK || fake.lastIfMatch != 3 || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("submit = %d if-match %d etag %q; want 200, 3 passed through and ETag \"4\"", rec.Code, fake.lastIfMatch, rec.Header().Get("ETag"))
	}
	first := fake.lastHash
	send(t, h, http.MethodPost, "/v1/expense/reports/"+id+"/submit", "submit-1", nil, header{"If-Match", `"2"`})
	if fake.lastHash == first {
		t.Fatalf("the request hash must cover If-Match, so a key reused with another version is refused")
	}
	if rec := send(t, h, http.MethodPost, "/v1/expense/reports/"+id+"/submit", "submit-2", nil, header{"If-Match", "three"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed If-Match = %d; want 400", rec.Code)
	}
	if rec := send(t, h, http.MethodPost, "/v1/expense/reports/"+id+"/submit", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("submit without Idempotency-Key = %d; want 400", rec.Code)
	}
}

func TestSubmitRefusalIsA422WithPerLineErrors(t *testing.T) {
	fake := newFakeReports()
	fake.refuse = true
	rec := send(t, reportRouter(fake), http.MethodPost, "/v1/expense/reports/"+ids.New()+"/submit", "submit-1", nil)
	p := problemOf(t, rec)
	if rec.Code != http.StatusUnprocessableEntity || p.Code != domain.CodeBlock || p.Errors["l1/EXP-DUP-001"] == "" {
		t.Fatalf("refused submit = %d %+v; want 422 %s with the line error", rec.Code, p, domain.CodeBlock)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "problem+json") {
		t.Fatalf("content type = %q; want application/problem+json", rec.Header().Get("Content-Type"))
	}
}

func TestDeleteLineValidatesIDsAndPassesIfMatch(t *testing.T) {
	fake := newFakeReports()
	h := reportRouter(fake)
	report := "/v1/expense/reports/" + ids.New()
	if rec := send(t, h, http.MethodDelete, report+"/lines/not-a-uuid", "", nil); rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors["lineId"] == "" {
		t.Fatalf("malformed line id = %d; want 400 naming lineId", rec.Code)
	}
	rec := send(t, h, http.MethodDelete, report+"/lines/"+ids.New(), "", nil, header{"If-Match", `W/"4"`})
	if rec.Code != http.StatusOK || fake.lastIfMatch != 4 || rec.Header().Get("ETag") != `"5"` {
		t.Fatalf("delete = %d if-match %d etag %q; want 200, 4 and ETag \"5\"", rec.Code, fake.lastIfMatch, rec.Header().Get("ETag"))
	}
}

func TestDecideReportNeedsTheApproveDutyAndAValidDecision(t *testing.T) {
	h := reportRouter(newFakeReports())
	path := "/v1/expense/reports/" + ids.New() + "/decide"
	if rec := send(t, h, http.MethodPost, path, "k", map[string]string{"decision": "APPROVE"}); rec.Code != http.StatusForbidden {
		t.Fatalf("decide with only %s = %d; want 403", dutyWrite, rec.Code)
	}
	approver := header{tenant.HeaderDuties, "expense.approve"}
	if rec := send(t, h, http.MethodPost, path, "k", map[string]string{"decision": "MAYBE"}, approver); rec.Code != http.StatusBadRequest {
		t.Fatalf("decision MAYBE = %d; want 400", rec.Code)
	}
	if rec := send(t, h, http.MethodPost, path, "", map[string]string{"decision": "APPROVE"}, approver); rec.Code != http.StatusBadRequest {
		t.Fatalf("decide without Idempotency-Key = %d; want 400", rec.Code)
	}
	fake := newFakeReports()
	rec := send(t, reportRouter(fake), http.MethodPost, path, "k", map[string]string{"decision": "APPROVE"}, approver, header{"If-Match", `"4"`})
	if rec.Code != http.StatusOK || fake.lastIfMatch != 4 || !fake.lastDate.Equal(time.Date(2025, 3, 5, 0, 0, 0, 0, time.UTC)) || !strings.Contains(rec.Body.String(), `"status":"APPROVED"`) {
		t.Fatalf("approve = %d %s ifMatch=%d date=%v; want 200 APPROVED at version 4 on the UTC day", rec.Code, rec.Body.String(), fake.lastIfMatch, fake.lastDate)
	}
}
