package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

const (
	testTenant = "00000000-0000-0000-0000-000000000001"
	testLedger = "0190a000-0000-7000-8000-000000000001"
	testDoc    = "0190a000-0000-7000-8000-000000000002"
	testEntry  = "0190a000-0000-7000-8000-000000000003"
	allDuties  = dutyLedgerWrite + "," + dutyAccountWrite + "," + dutyRead + "," + dutyBalanceRead + "," + dutyPost + "," + dutyReverse + "," + dutyRebuild + "," + dutyReasonWrite
)

// fakeStore records the inputs the handlers pass down and answers with the
// configured result or error.
type fakeStore struct {
	posted   []store.PostInput
	reversed []store.ReverseInput
	err      error
	replay   bool
	accounts []store.Account
	validate domain.Result
	reasons  []store.ReasonCode
	created  bool
	// putReasons records the reason codes the handlers wrote.
	putReasons []store.ReasonCode
	// structures and dimValues answer the structure reads; putStructures and
	// putValues record the writes with the ledger they were addressed to.
	structures    []domain.Structure
	putStructures []putStructureCall
	dimValues     []domain.DimensionValue
	putValues     []domain.DimensionValue
	// templates answer the description template reads; putTemplates records the writes.
	templates    []store.DescriptionTemplate
	putTemplates []store.DescriptionTemplate
	// journalNames answer the journal name reads; putNames records the writes.
	journalNames []store.JournalName
	putNames     []store.JournalName
	// batch answers every batch call; batchCalls records them in order.
	batch      store.Batch
	batchCalls []batchCall
	// linePage answers ListLines; lineFilters records each filter it received.
	linePage    store.LinePage
	lineFilters []store.LineFilter
}

// batchCall is one journal batch store call as the handler made it.
type batchCall struct {
	op      string
	id      string
	version int
	create  store.BatchInput
	patch   store.BatchPatch
	filter  store.BatchFilter
	post    store.PostBatchInput
}

type putStructureCall struct {
	ledgerID, id string
	in           domain.Structure
}

func (f *fakeStore) PutAccount(_ context.Context, chart, number string, in store.Account) (store.Account, bool, error) {
	in.ChartCode, in.Number = chart, number
	return in, true, f.err
}

func (f *fakeStore) ListAccounts(context.Context, string, string, int) ([]store.Account, error) {
	return f.accounts, f.err
}

func (f *fakeStore) CreateLedger(_ context.Context, in store.Ledger) (store.Ledger, error) {
	in.ID = testLedger
	return in, f.err
}

func (f *fakeStore) GetLedger(context.Context, string) (store.Ledger, error) {
	return store.Ledger{ID: testLedger}, f.err
}

func (f *fakeStore) PostEntry(_ context.Context, in store.PostInput) (store.EntryResult, error) {
	f.posted = append(f.posted, in)
	if f.err != nil {
		return store.EntryResult{}, f.err
	}
	return store.EntryResult{Entries: []store.Entry{{ID: testEntry}}, Idempotency: store.Idempotency{Key: in.Key, Replayed: f.replay}}, nil
}

func (f *fakeStore) ReverseEntry(_ context.Context, in store.ReverseInput) (store.EntryResult, error) {
	f.reversed = append(f.reversed, in)
	if f.err != nil {
		return store.EntryResult{}, f.err
	}
	return store.EntryResult{Entries: []store.Entry{{ID: testEntry}}, Idempotency: store.Idempotency{Key: in.Key, Replayed: f.replay}}, nil
}

func (f *fakeStore) GetEntry(context.Context, string) (store.Entry, error) {
	return store.Entry{}, f.err
}

func (f *fakeStore) ValidateEntry(_ context.Context, in store.PostInput) (domain.Result, error) {
	f.posted = append(f.posted, in)
	return f.validate, f.err
}

func (f *fakeStore) TrialBalance(context.Context, store.BalanceFilter) (store.TrialBalance, error) {
	return store.TrialBalance{}, f.err
}

func (f *fakeStore) RebuildBalances(context.Context, string) (store.RebuildResult, error) {
	return store.RebuildResult{}, f.err
}

func (f *fakeStore) ListReasonCodes(context.Context) ([]store.ReasonCode, error) {
	return f.reasons, f.err
}

func (f *fakeStore) GetReasonCode(_ context.Context, code string) (store.ReasonCode, error) {
	for _, r := range f.reasons {
		if r.Code == code {
			return r, f.err
		}
	}
	return store.ReasonCode{}, httpx.NewProblem(http.StatusNotFound, "LEDGER_REASON_NOT_FOUND", "reason code not found")
}

func (f *fakeStore) PutReasonCode(_ context.Context, code string, in store.ReasonCode) (store.ReasonCode, bool, error) {
	in.Code = code
	f.putReasons = append(f.putReasons, in)
	return in, f.created, f.err
}

func (f *fakeStore) ListStructures(context.Context, string) ([]domain.Structure, error) {
	return f.structures, f.err
}

func (f *fakeStore) GetStructure(_ context.Context, _, id string) (domain.Structure, error) {
	for _, s := range f.structures {
		if s.ID == id {
			return s, f.err
		}
	}
	return domain.Structure{}, httpx.NewProblem(http.StatusNotFound, domain.CodeStructureNotFound, "account structure not found")
}

func (f *fakeStore) PutStructure(_ context.Context, ledgerID, id string, in domain.Structure) (domain.Structure, bool, error) {
	f.putStructures = append(f.putStructures, putStructureCall{ledgerID: ledgerID, id: id, in: in})
	in.ID = id
	return in, f.created, f.err
}

func (f *fakeStore) ListDimensionValues(context.Context, string) ([]domain.DimensionValue, error) {
	return f.dimValues, f.err
}

func (f *fakeStore) PutDimensionValue(_ context.Context, _ string, in domain.DimensionValue) (domain.DimensionValue, bool, error) {
	f.putValues = append(f.putValues, in)
	return in, f.created, f.err
}

func (f *fakeStore) ListDescriptionTemplates(context.Context) ([]store.DescriptionTemplate, error) {
	return f.templates, f.err
}

func (f *fakeStore) GetDescriptionTemplate(_ context.Context, id string) (store.DescriptionTemplate, error) {
	for _, d := range f.templates {
		if d.ID == id {
			return d, f.err
		}
	}
	return store.DescriptionTemplate{}, templateNotFound()
}

func (f *fakeStore) PutDescriptionTemplate(_ context.Context, id string, in store.DescriptionTemplate) (store.DescriptionTemplate, bool, error) {
	in.ID = id
	f.putTemplates = append(f.putTemplates, in)
	return in, f.created, f.err
}

func (f *fakeStore) ListJournalNames(context.Context) ([]store.JournalName, error) {
	return f.journalNames, f.err
}

func (f *fakeStore) GetJournalName(_ context.Context, code string) (store.JournalName, error) {
	for _, n := range f.journalNames {
		if n.Code == code {
			return n, f.err
		}
	}
	return store.JournalName{}, httpx.NewProblem(http.StatusNotFound, "LEDGER_JOURNAL_NAME_NOT_FOUND", "journal name not found")
}

func (f *fakeStore) PutJournalName(_ context.Context, code string, in store.JournalName) (store.JournalName, bool, error) {
	in.Code = code
	f.putNames = append(f.putNames, in)
	return in, f.created, f.err
}

func (f *fakeStore) batchResult(c batchCall) (store.Batch, error) {
	f.batchCalls = append(f.batchCalls, c)
	return f.batch, f.err
}

func (f *fakeStore) CreateBatch(_ context.Context, in store.BatchInput) (store.Batch, error) {
	return f.batchResult(batchCall{op: "create", create: in})
}

func (f *fakeStore) GetBatch(_ context.Context, id string) (store.Batch, error) {
	return f.batchResult(batchCall{op: "get", id: id})
}

func (f *fakeStore) ListBatches(_ context.Context, filter store.BatchFilter) ([]store.Batch, error) {
	b, err := f.batchResult(batchCall{op: "list", filter: filter})
	return []store.Batch{b}, err
}

func (f *fakeStore) UpdateBatch(_ context.Context, id string, version int, in store.BatchPatch) (store.Batch, error) {
	return f.batchResult(batchCall{op: "update", id: id, version: version, patch: in})
}

func (f *fakeStore) SubmitBatch(_ context.Context, id string, version int, _ time.Time) (store.Batch, error) {
	return f.batchResult(batchCall{op: "submit", id: id, version: version})
}

func (f *fakeStore) DecideBatch(_ context.Context, id string, version int, approve bool) (store.Batch, error) {
	op := "reject"
	if approve {
		op = "approve"
	}
	return f.batchResult(batchCall{op: op, id: id, version: version})
}

func (f *fakeStore) CancelBatch(_ context.Context, id string, version int) (store.Batch, error) {
	return f.batchResult(batchCall{op: "cancel", id: id, version: version})
}

func (f *fakeStore) PostBatch(_ context.Context, in store.PostBatchInput) (store.EntryResult, error) {
	f.batchCalls = append(f.batchCalls, batchCall{op: "post", id: in.ID, version: in.Version, post: in})
	if f.err != nil {
		return store.EntryResult{}, f.err
	}
	return store.EntryResult{Entries: []store.Entry{{ID: testEntry}}, Idempotency: store.Idempotency{Key: in.Key, Replayed: f.replay}}, nil
}

func (f *fakeStore) Ping(context.Context) error { return f.err }

func server(fs *fakeStore) http.Handler {
	r := httpx.NewRouter()
	a := New(fs)
	a.now = func() time.Time { return time.Date(2026, 3, 15, 13, 0, 0, 0, time.UTC) }
	a.Register(r)
	return r
}

func call(h http.Handler, method, path, body, duties string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Passage-Tenant", testTenant)
	req.Header.Set("X-Passage-User", "clerk-1")
	req.Header.Set("X-Passage-Duties", duties)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, rec.Body.String())
	}
	return m
}

const entryBody = `{"ledgerId":"` + testLedger + `","documentId":"` + testDoc + `","postingDate":"2026-03-31",
  "description":"Vendor invoice 7F3A",
  "source":{"system":"ap","type":"vendor_invoice","id":"7F3A","uri":"passage://ap/invoices/7F3A"},
  "lines":[{"account":"6010","dimensions":{"DEPT":"D100"},"debitCredit":"D","amount":{"currency":"USD","value":"1250.00"},"text":"Freight"},
           {"account":"2100","debitCredit":"C","amount":{"currency":"USD","value":"1250.00"}}]}`

func TestPostEntry_CreatedThenReplayed(t *testing.T) {
	fs := &fakeStore{}
	h := server(fs)
	rec := call(h, http.MethodPost, "/api/v1/ledger/journal-entries", entryBody, dutyPost, map[string]string{"Idempotency-Key": "  ap-7F3A  "})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(fs.posted) != 1 {
		t.Fatalf("store calls = %d", len(fs.posted))
	}
	in := fs.posted[0]
	if in.Key != "ap-7F3A" {
		t.Fatalf("Idempotency-Key must be trimmed (BR-11.6), got %q", in.Key)
	}
	if in.Actor != "clerk-1" || len(in.Hash) != 32 || len(in.TraceID) != 32 {
		t.Fatalf("actor/hash/trace not set: %+v", in)
	}
	if !in.Today.Equal(time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)) || in.PostingDate.Format(time.DateOnly) != "2026-03-31" {
		t.Fatalf("dates: today %s posting %s", in.Today, in.PostingDate)
	}
	if !in.Lines[0].Amount.Equal(decimal.RequireFromString("1250")) || in.Lines[0].Dimensions["DEPT"] != "D100" || in.Lines[1].Side != "C" {
		t.Fatalf("lines not mapped: %+v", in.Lines)
	}

	// Same payload, whitespace-different key: identical hash, 200 replay.
	fs.replay = true
	rec = call(h, http.MethodPost, "/api/v1/ledger/journal-entries", entryBody, dutyPost, map[string]string{"Idempotency-Key": "ap-7F3A"})
	if rec.Code != http.StatusOK {
		t.Fatalf("replay status %d: %s", rec.Code, rec.Body.String())
	}
	if string(fs.posted[1].Hash) != string(in.Hash) || fs.posted[1].Key != in.Key {
		t.Fatal("a replay must carry the same key and payload hash")
	}
	if m := decode(t, rec); m["idempotency"].(map[string]any)["replayed"] != true {
		t.Fatalf("replay body: %v", m)
	}
}

func TestPostEntry_TraceparentIsCopied(t *testing.T) {
	fs := &fakeStore{}
	trace := "4bf92f3577b34da6a3ce929d0e0e4736"
	rec := call(server(fs), http.MethodPost, "/api/v1/ledger/journal-entries", entryBody, dutyPost,
		map[string]string{"Idempotency-Key": "k1", "traceparent": "00-" + trace + "-00f067aa0ba902b7-01"})
	if rec.Code != http.StatusCreated || fs.posted[0].TraceID != trace {
		t.Fatalf("status %d, trace %q", rec.Code, fs.posted[0].TraceID)
	}
}

func TestPostEntry_RequestValidation(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		key       string
		wantField string
	}{
		{"missing key", entryBody, "", "Idempotency-Key"},
		{"blank key", entryBody, "   ", "Idempotency-Key"},
		{"key too long", entryBody, strings.Repeat("k", 129), "Idempotency-Key"},
		{"bad ledger id", strings.Replace(entryBody, testLedger, "nope", 1), "k", "ledgerId"},
		{"bad posting date", strings.Replace(entryBody, "2026-03-31", "31/03/2026", 1), "k", "postingDate"},
		{"negative amount", strings.Replace(entryBody, `"value":"1250.00"}}]`, `"value":"-1250.00"}}]`, 1), "k", "lines[1].amount.value"},
		{"bad side", strings.Replace(entryBody, `"debitCredit":"C"`, `"debitCredit":"X"`, 1), "k", "lines[1].debitCredit"},
		{"missing source id", strings.Replace(entryBody, `"id":"7F3A",`, ``, 1), "k", "source.id"},
		{"no lines", `{"ledgerId":"` + testLedger + `","documentId":"` + testDoc + `","postingDate":"2026-03-31","source":{"system":"a","type":"b","id":"c"},"lines":[]}`, "k", "lines"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := &fakeStore{}
			headers := map[string]string{}
			if tc.key != "" {
				headers["Idempotency-Key"] = tc.key
			}
			rec := call(server(fs), http.MethodPost, "/api/v1/ledger/journal-entries", tc.body, dutyPost, headers)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			errs, _ := decode(t, rec)["errors"].(map[string]any)
			if _, ok := errs[tc.wantField]; !ok {
				t.Fatalf("want field error %q, got %v", tc.wantField, errs)
			}
			if len(fs.posted) != 0 {
				t.Fatal("an invalid request must not reach the store")
			}
		})
	}
}

func TestPostEntry_RuleRejectionIs422WithEveryViolation(t *testing.T) {
	idx := 1
	fs := &fakeStore{err: &domain.RejectedError{Violations: []domain.Violation{
		{Code: "LEDGER_UNBALANCED", Field: "lines", Message: "debits 100.00, credits 99.99"},
		{Code: "LEDGER_ACCOUNT_NOT_FOUND", Field: "lines.account", LineIndex: &idx, Message: "account 9999 does not exist"},
	}}}
	rec := call(server(fs), http.MethodPost, "/api/v1/ledger/journal-entries", entryBody, dutyPost, map[string]string{"Idempotency-Key": "k"})
	if rec.Code != http.StatusUnprocessableEntity || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status %d, type %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	m := decode(t, rec)
	if m["code"] != "LEDGER_UNBALANCED" {
		t.Fatalf("problem code = %v", m["code"])
	}
	v := m["violations"].([]any)
	if len(v) != 2 {
		t.Fatalf("want 2 violations, got %v", v)
	}
	second := v[1].(map[string]any)
	if second["code"] != "LEDGER_ACCOUNT_NOT_FOUND" || second["lineIndex"] != float64(1) {
		t.Fatalf("second violation: %v", second)
	}
	if _, ok := v[0].(map[string]any)["lineIndex"]; ok {
		t.Fatal("an entry-level violation must not carry lineIndex")
	}
}

func TestPostEntry_IdempotencyErrors(t *testing.T) {
	fs := &fakeStore{err: httpx.NewProblem(http.StatusConflict, "LEDGER_IDEMPOTENCY_CONFLICT", "different body")}
	rec := call(server(fs), http.MethodPost, "/api/v1/ledger/journal-entries", entryBody, dutyPost, map[string]string{"Idempotency-Key": "k"})
	if rec.Code != http.StatusConflict || decode(t, rec)["code"] != "LEDGER_IDEMPOTENCY_CONFLICT" || rec.Header().Get("Retry-After") != "" {
		t.Fatalf("conflict: status %d retry %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	fs.err = httpx.NewProblem(http.StatusConflict, store.CodeIdempotencyInProgress, "in flight")
	rec = call(server(fs), http.MethodPost, "/api/v1/ledger/journal-entries", entryBody, dutyPost, map[string]string{"Idempotency-Key": "k"})
	if rec.Code != http.StatusConflict || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("in progress: status %d retry %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestRoutes_RequireDuties(t *testing.T) {
	routes := []struct {
		method, path, body, duty string
	}{
		{http.MethodPost, "/api/v1/ledger/journal-entries", entryBody, dutyPost},
		{http.MethodPost, "/api/v1/ledger/journal-entries/" + testEntry + "/reverse", `{}`, dutyReverse},
		{http.MethodGet, "/api/v1/ledger/journal-entries/" + testEntry, "", dutyRead},
		{http.MethodPost, "/api/v1/ledger/validate", entryBody, dutyPost},
		{http.MethodGet, "/api/v1/ledger/balances?ledger=" + testLedger, "", dutyBalanceRead},
		{http.MethodPost, "/api/v1/ledger/balances:rebuild", `{"ledgerId":"` + testLedger + `"}`, dutyRebuild},
		{http.MethodPost, "/api/v1/ledger/ledgers", `{}`, dutyLedgerWrite},
		{http.MethodPut, "/api/v1/ledger/charts/STD/accounts/6010", `{}`, dutyAccountWrite},
		{http.MethodGet, "/api/v1/ledger/reason-codes", "", dutyRead},
		{http.MethodGet, "/api/v1/ledger/reason-codes/ERR01", "", dutyRead},
		{http.MethodPut, "/api/v1/ledger/reason-codes/ERR01", `{"name":"x","appliesTo":["ledger"]}`, dutyReasonWrite},
	}
	for _, rt := range routes {
		others := strings.ReplaceAll(allDuties, rt.duty, "")
		rec := call(server(&fakeStore{}), rt.method, rt.path, rt.body, others, map[string]string{"Idempotency-Key": "k"})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s without %s: status %d", rt.method, rt.path, rt.duty, rec.Code)
		}
	}
}

func TestReverseEntry_PassesDateAndKey(t *testing.T) {
	fs := &fakeStore{}
	h := server(fs)
	rec := call(h, http.MethodPost, "/api/v1/ledger/journal-entries/"+testEntry+"/reverse", `{"postingDate":"2026-04-01","description":"wrong vendor"}`,
		dutyReverse, map[string]string{"Idempotency-Key": " rev-1 "})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	in := fs.reversed[0]
	if in.EntryID != testEntry || in.Key != "rev-1" || in.PostingDate == nil || in.PostingDate.Format(time.DateOnly) != "2026-04-01" || in.Description != "wrong vendor" {
		t.Fatalf("reverse input: %+v", in)
	}
	// No body at all is allowed (date defaults to the original).
	rec = call(h, http.MethodPost, "/api/v1/ledger/journal-entries/"+testEntry+"/reverse", "", dutyReverse, map[string]string{"Idempotency-Key": "rev-2"})
	if rec.Code != http.StatusCreated || fs.reversed[1].PostingDate != nil {
		t.Fatalf("empty body: status %d, date %v", rec.Code, fs.reversed[1].PostingDate)
	}
	// A different body under another key hashes differently from the first.
	if string(fs.reversed[0].Hash) == string(fs.reversed[1].Hash) {
		t.Fatal("different reversal requests must hash differently")
	}
	rec = call(h, http.MethodPost, "/api/v1/ledger/journal-entries/"+testEntry+"/reverse", `{"postingDate":"tomorrow"}`, dutyReverse, map[string]string{"Idempotency-Key": "rev-3"})
	if rec.Code != http.StatusBadRequest || len(fs.reversed) != 2 {
		t.Fatalf("bad date: status %d", rec.Code)
	}
	rec = call(h, http.MethodPost, "/api/v1/ledger/journal-entries/not-a-uuid/reverse", `{}`, dutyReverse, map[string]string{"Idempotency-Key": "rev-4"})
	if rec.Code != http.StatusNotFound || decode(t, rec)["code"] != "LEDGER_ENTRY_NOT_FOUND" {
		t.Fatalf("bad id: status %d", rec.Code)
	}
}

func TestValidateEntry_ReturnsViolationsAndScaledTotals(t *testing.T) {
	fs := &fakeStore{validate: domain.Result{Currency: "USD", Debit: decimal.RequireFromString("100"), Credit: decimal.RequireFromString("99.99"),
		Violations: []domain.Violation{{Code: "LEDGER_UNBALANCED", Message: "x"}}}}
	rec := call(server(fs), http.MethodPost, "/api/v1/ledger/validate", entryBody, dutyPost, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	m := decode(t, rec)
	if m["valid"] != false || m["debit"] != "100.00" || m["credit"] != "99.99" || m["currency"] != "USD" {
		t.Fatalf("validate body: %v", m)
	}
	if v := m["violations"].([]any); len(v) != 1 {
		t.Fatalf("violations: %v", v)
	}
}

func TestPutAccount_Validation(t *testing.T) {
	h := server(&fakeStore{})
	rec := call(h, http.MethodPut, "/api/v1/ledger/charts/STD/accounts/6010", `{"name":"Freight","accountType":"expense"}`, dutyAccountWrite, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if m := decode(t, rec); m["isPostable"] != true || m["reconciliationAccountType"] != "none" {
		t.Fatalf("defaults: %v", m)
	}
	rec = call(h, http.MethodPut, "/api/v1/ledger/charts/STD/accounts/6000", `{"name":"Expenses","accountType":"profit_and_loss_total"}`, dutyAccountWrite, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a postable total account must be refused, got %d", rec.Code)
	}
	rec = call(h, http.MethodPut, "/api/v1/ledger/charts/std/accounts/6010", `{"name":"x","accountType":"expense"}`, dutyAccountWrite, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("lowercase chart must be refused, got %d", rec.Code)
	}
}

func TestListAccounts_PagesWithCursor(t *testing.T) {
	fs := &fakeStore{accounts: []store.Account{{Number: "1000"}, {Number: "2000"}}}
	rec := call(server(fs), http.MethodGet, "/api/v1/ledger/charts/STD/accounts?limit=2", "", dutyRead, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	m := decode(t, rec)
	if m["nextAfter"] != "2000" || len(m["items"].([]any)) != 2 {
		t.Fatalf("page: %v", m)
	}
	rec = call(server(fs), http.MethodGet, "/api/v1/ledger/charts/STD/accounts?limit=0", "", dutyRead, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("limit=0 must be refused, got %d", rec.Code)
	}
}

func TestCreateLedger_ScopeAndValidation(t *testing.T) {
	h := server(&fakeStore{})
	body := `{"legalEntityId":"` + testDoc + `","code":"GL","name":"General ledger","chartCode":"STD","currency":"USD","dimensionAttributes":["DEPT","CC"]}`
	rec := call(h, http.MethodPost, "/api/v1/ledger/ledgers", body, dutyLedgerWrite, nil)
	if rec.Code != http.StatusCreated || decode(t, rec)["futurePostingDays"] != float64(366) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	rec = call(h, http.MethodPost, "/api/v1/ledger/ledgers", body, dutyLedgerWrite, map[string]string{"X-Passage-Legal-Entity": testLedger})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a caller scoped to another legal entity must get 403, got %d", rec.Code)
	}
	dup := strings.Replace(body, `["DEPT","CC"]`, `["DEPT","DEPT"]`, 1)
	if rec = call(h, http.MethodPost, "/api/v1/ledger/ledgers", dup, dutyLedgerWrite, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate attributes must be refused, got %d", rec.Code)
	}
}

func TestTrialBalance_RequiresLedgerAndPeriodFormat(t *testing.T) {
	h := server(&fakeStore{})
	if rec := call(h, http.MethodGet, "/api/v1/ledger/balances", "", dutyBalanceRead, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing ledger: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/api/v1/ledger/balances?ledger="+testLedger+"&period_from=2026-3", "", dutyBalanceRead, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad period: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/api/v1/ledger/balances?ledger="+testLedger+"&period_from=2026-01&period_to=2026-03", "", dutyBalanceRead, nil); rec.Code != http.StatusOK {
		t.Fatalf("valid query: %d", rec.Code)
	}
}

func TestAdmin_ReadinessAndMetrics(t *testing.T) {
	fs := &fakeStore{}
	a := New(fs)
	rec := httptest.NewRecorder()
	a.Admin().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("ready: %d", rec.Code)
	}
	fs.err = context.DeadlineExceeded
	rec = httptest.NewRecorder()
	a.Admin().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("not ready: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	a.Admin().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "ledger_entries_posted_total 0") {
		t.Fatalf("metrics: %s", rec.Body.String())
	}
}

func TestRoutes_RejectMalformedTenant(t *testing.T) {
	h := server(&fakeStore{})
	for _, path := range []string{"/api/v1/ledger/journal-entries/" + testEntry, "/api/v1/ledger/balances?ledger=" + testLedger,
		"/api/v1/ledger/ledgers/" + testLedger, "/api/v1/ledger/charts/STD/accounts"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Passage-Tenant", "not-a-uuid")
		req.Header.Set("X-Passage-Duties", allDuties)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with a malformed tenant: status %d, want 401", path, rec.Code)
		}
	}
}
