package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

const (
	testTenant   = "0190b7a0-0000-7000-8000-000000000001"
	testLE       = "0190b7a0-0000-7000-8000-000000000002"
	testMainAcct = "0190b7a0-0000-7000-8000-000000000003"
	testIBAN     = "DE89370400440532013000"
	allDuties    = "treasury.view,treasury.bank-account.write,treasury.bank-account.approve,treasury.statement.import,treasury.forecast.write," +
		"treasury.check.write,treasury.facility.write,treasury.facility.override,treasury.loan.write,treasury.reconciliation.write," +
		"treasury.ebics.write,treasury.ebics.keys,treasury.ebics.approve"
)

// fakeStore is an in-memory store.Store for handler tests.
type fakeStore struct {
	mu           sync.Mutex
	accounts     map[string]store.BankAccount
	byIdem       map[string]string
	statements   map[string]store.Statement
	byHash       map[string]string
	lines        map[string][]store.StatementLine
	entries      []store.ForecastEntry
	forecastKeys []string
	importErr    error
	pingErr      error
	checks       map[string]store.Check
	nextNo       map[string]int64
	facilities   map[string]store.Facility
	instruments  map[string]store.Instrument
	loans        map[string]store.Loan
	lastAccount  store.NewBankAccount
	lastRule     store.NewReconRule
	lastRun      store.RunInput
	lastMatch    store.NewMatch
	matchErr     error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		accounts: map[string]store.BankAccount{}, byIdem: map[string]string{},
		statements: map[string]store.Statement{}, byHash: map[string]string{},
		lines:  map[string][]store.StatementLine{},
		checks: map[string]store.Check{}, nextNo: map[string]int64{},
		facilities: map[string]store.Facility{}, instruments: map[string]store.Instrument{},
		loans: map[string]store.Loan{},
	}
}

func (f *fakeStore) CreateBankAccount(_ context.Context, in store.NewBankAccount, key string) (store.BankAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.byIdem[key]; ok {
		return f.accounts[id], nil
	}
	f.lastAccount = in
	acct := store.BankAccount{
		ID: ids.New(), LegalEntityID: in.LegalEntityID, AccountCode: in.AccountCode,
		IBANMasked: domain.MaskIBAN(in.IBAN), IBANLast4: domain.Last4(in.IBAN), BIC: in.BIC,
		Currency: in.Currency, MainAccountID: in.MainAccountID, Status: "draft", Version: 1,
	}
	if in.StatementAccount != "" {
		last4 := domain.Last4(in.StatementAccount)
		acct.StatementAccountLast4 = &last4
	}
	f.accounts[acct.ID] = acct
	f.byIdem[key] = acct.ID
	return acct, nil
}

func (f *fakeStore) ListBankAccounts(_ context.Context, le string) ([]store.BankAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.BankAccount
	for _, a := range f.accounts {
		if a.LegalEntityID == le {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeStore) GetBankAccount(_ context.Context, id string) (store.BankAccount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.accounts[id]
	if !ok {
		return store.BankAccount{}, httpx.NewProblem(http.StatusNotFound, store.CodeAccountNotFound, "bank account not found")
	}
	return a, nil
}

func (f *fakeStore) TransitionBankAccount(ctx context.Context, id string, version int, to domain.AccountStatus) (store.BankAccount, error) {
	a, err := f.GetBankAccount(ctx, id)
	if err != nil {
		return a, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if a.Version != version {
		return a, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale version")
	}
	if !domain.CanTransition(domain.AccountStatus(a.Status), to) {
		return a, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeAccountState, "transition refused")
	}
	a.Status, a.Version = string(to), a.Version+1
	f.accounts[id] = a
	return a, nil
}

func (f *fakeStore) ImportStatement(_ context.Context, in store.StatementImport) (store.ImportResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.importErr != nil {
		return store.ImportResult{}, f.importErr
	}
	if id, ok := f.byHash[in.FileHash]; ok {
		return store.ImportResult{Statement: f.statements[id], Replayed: true}, nil
	}
	st := store.Statement{
		ID: ids.New(), BankAccountID: in.BankAccountID, StatementID: in.Parsed.StatementID, Format: in.Parsed.Format,
		Currency: in.Parsed.Currency, OpeningDate: in.Parsed.OpeningDate, OpeningBalance: in.Parsed.OpeningAmount.String(),
		ClosingDate: in.Parsed.ClosingDate, ClosingBalance: in.Parsed.ClosingAmount.String(), LineCount: len(in.Parsed.Lines),
	}
	for _, l := range in.Parsed.Lines {
		f.lines[st.ID] = append(f.lines[st.ID], store.StatementLine{
			LineNo: l.LineNo, BookingDate: l.BookingDate, ValueDate: l.ValueDate, Amount: l.Amount.String(),
			Currency: l.Currency, Status: "unmatched",
		})
	}
	f.statements[st.ID] = st
	f.byHash[in.FileHash] = st.ID
	return store.ImportResult{Statement: st}, nil
}

func (f *fakeStore) GetStatement(_ context.Context, id string) (store.Statement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.statements[id]
	if !ok {
		return store.Statement{}, httpx.NewProblem(http.StatusNotFound, store.CodeStatementNotFound, "statement not found")
	}
	return st, nil
}

func (f *fakeStore) ListStatements(_ context.Context, bankAccountID string) ([]store.Statement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Statement
	for _, st := range f.statements {
		if st.BankAccountID == bankAccountID {
			out = append(out, st)
		}
	}
	return out, nil
}

func (f *fakeStore) ListStatementLines(_ context.Context, statementID string, after, limit int) ([]store.StatementLine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.StatementLine
	for _, l := range f.lines[statementID] {
		if l.LineNo > after && len(out) < limit {
			out = append(out, l)
		}
	}
	return out, nil
}

func (f *fakeStore) CreateForecastEntry(_ context.Context, in store.ForecastEntry, key string) (store.ForecastEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forecastKeys = append(f.forecastKeys, key)
	in.ID = ids.New()
	f.entries = append(f.entries, in)
	return in, nil
}

func (f *fakeStore) ListForecastEntries(_ context.Context, le, currency, from, to string) ([]store.ForecastEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.ForecastEntry
	for _, e := range f.entries {
		if e.LegalEntityID == le && (currency == "" || e.Currency == currency) && e.ExpectedDate >= from && e.ExpectedDate <= to {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }

func newTestServer(t *testing.T) (http.Handler, *fakeStore) {
	t.Helper()
	fs := newFakeStore()
	r := httpx.NewRouter()
	New(fs).Register(r)
	return r, fs
}

func call(h http.Handler, method, path string, body *bytes.Buffer, contentType string, headers map[string]string) *httptest.ResponseRecorder {
	var rd *bytes.Buffer = body
	if rd == nil {
		rd = &bytes.Buffer{}
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set(tenant.HeaderTenant, testTenant)
	req.Header.Set(tenant.HeaderDuties, allDuties)
	req.Header.Set(tenant.HeaderLegalEntity, testLE)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func createAccountBody(t *testing.T) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"legalEntityId": testLE, "accountCode": "OPS-01", "iban": testIBAN, "currency": "EUR", "mainAccountId": testMainAcct,
	})
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewBuffer(b)
}

func createAccount(t *testing.T, h http.Handler, key string) store.BankAccount {
	t.Helper()
	rec := call(h, http.MethodPost, "/v1/treasury/bank-accounts", createAccountBody(t), "application/json",
		map[string]string{idempotencyKey: key})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create account: status %d body %s", rec.Code, rec.Body)
	}
	var acct store.BankAccount
	if err := json.Unmarshal(rec.Body.Bytes(), &acct); err != nil {
		t.Fatal(err)
	}
	return acct
}

func problemCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var p struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode problem: %v (%s)", err, rec.Body)
	}
	return p.Code
}

func TestCreateBankAccount_Validation(t *testing.T) {
	h, _ := newTestServer(t)
	cases := []struct {
		name   string
		key    string
		mutate func(map[string]any)
		status int
		code   string
	}{
		{name: "missing idempotency key", key: "", status: http.StatusBadRequest, code: "COM-0001"},
		{name: "IBAN fails mod-97", key: "k-iban", mutate: func(m map[string]any) { m["iban"] = "DE89370400440532013001" },
			status: http.StatusUnprocessableEntity, code: "TRS-0401"},
		{name: "bad currency", key: "k-cur", mutate: func(m map[string]any) { m["currency"] = "eu" },
			status: http.StatusBadRequest, code: "COM-0001"},
		{name: "bad statement account", key: "k-stmt-acct", mutate: func(m map[string]any) { m["statementAccount"] = "12#34" },
			status: http.StatusBadRequest, code: "COM-0001"},
		{name: "statement account too long", key: "k-stmt-long", mutate: func(m map[string]any) { m["statementAccount"] = strings.Repeat("1", 36) },
			status: http.StatusBadRequest, code: "COM-0001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := map[string]any{"legalEntityId": testLE, "accountCode": "OPS-01", "iban": testIBAN, "currency": "EUR", "mainAccountId": testMainAcct}
			if tc.mutate != nil {
				tc.mutate(m)
			}
			b, _ := json.Marshal(m)
			headers := map[string]string{}
			if tc.key != "" {
				headers[idempotencyKey] = tc.key
			}
			rec := call(h, http.MethodPost, "/v1/treasury/bank-accounts", bytes.NewBuffer(b), "application/json", headers)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tc.status, rec.Body)
			}
			if got := problemCode(t, rec); got != tc.code {
				t.Fatalf("code = %q, want %q", got, tc.code)
			}
		})
	}
}

func TestCreateBankAccount_MasksIBANAndNeedsDuty(t *testing.T) {
	h, _ := newTestServer(t)
	rec := call(h, http.MethodPost, "/v1/treasury/bank-accounts", createAccountBody(t), "application/json",
		map[string]string{idempotencyKey: "k-mask", tenant.HeaderDuties: "treasury.view"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("view-only caller: status %d, want 403", rec.Code)
	}
	acct := createAccount(t, h, "k-mask-2")
	if acct.IBANLast4 != "3000" {
		t.Fatalf("ibanLast4 = %q", acct.IBANLast4)
	}
	if strings.Contains(acct.IBANMasked, "370400440532") {
		t.Fatalf("masked IBAN leaks digits: %q", acct.IBANMasked)
	}
}

func TestActivateBankAccount_RequiresIfMatchAndVersion(t *testing.T) {
	h, _ := newTestServer(t)
	acct := createAccount(t, h, "k-activate")
	path := "/v1/treasury/bank-accounts/" + acct.ID + ":activate"

	if rec := call(h, http.MethodPost, path, nil, "", nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("no If-Match: status %d, want 428", rec.Code)
	}
	if rec := call(h, http.MethodPost, path, nil, "", map[string]string{"If-Match": `"7"`}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: status %d, want 412", rec.Code)
	}
	rec := call(h, http.MethodPost, path, nil, "", map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusOK {
		t.Fatalf("activate: status %d body %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("ETag"); got != `"2"` {
		t.Fatalf("ETag after activate = %q", got)
	}
}

func multipartStatement(t *testing.T, accountID string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("bankAccountId", accountID); err != nil {
		t.Fatal(err)
	}
	part, err := w.CreateFormFile("file", "statement.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, w.FormDataContentType()
}

func TestImportStatement_ReplayAndLines(t *testing.T) {
	h, _ := newTestServer(t)
	acct := createAccount(t, h, "k-stmt")
	data, err := os.ReadFile("../../testdata/camt053/sample-08.xml")
	if err != nil {
		t.Fatal(err)
	}

	body, ct := multipartStatement(t, acct.ID, data)
	first := call(h, http.MethodPost, "/v1/treasury/statements/import", body, ct, nil)
	if first.Code != http.StatusCreated {
		t.Fatalf("first import: status %d body %s", first.Code, first.Body)
	}
	var st store.Statement
	if err := json.Unmarshal(first.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}

	body, ct = multipartStatement(t, acct.ID, data)
	second := call(h, http.MethodPost, "/v1/treasury/statements/import", body, ct, nil)
	if second.Code != http.StatusOK {
		t.Fatalf("replay: status %d, want 200", second.Code)
	}
	var again store.Statement
	if err := json.Unmarshal(second.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if again.ID != st.ID {
		t.Fatalf("replay returned %s, want %s", again.ID, st.ID)
	}

	lines := call(h, http.MethodGet, "/v1/treasury/statements/"+st.ID+"/lines", nil, "", nil)
	if lines.Code != http.StatusOK {
		t.Fatalf("lines: status %d", lines.Code)
	}
	var page struct {
		Items []store.StatementLine `json:"items"`
	}
	if err := json.Unmarshal(lines.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != st.LineCount || st.LineCount == 0 {
		t.Fatalf("lines = %d, statement lineCount = %d", len(page.Items), st.LineCount)
	}
}

func TestImportStatement_RejectsMalformedFile(t *testing.T) {
	h, _ := newTestServer(t)
	acct := createAccount(t, h, "k-bad")
	body, ct := multipartStatement(t, acct.ID, []byte("not xml"))
	rec := call(h, http.MethodPost, "/v1/treasury/statements/import", body, ct, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body %s", rec.Code, rec.Body)
	}
	if got := problemCode(t, rec); got != domain.CodeStatementFormat {
		t.Fatalf("code = %q, want %s", got, domain.CodeStatementFormat)
	}
}

func TestCashForecast_BucketsEqualContributions(t *testing.T) {
	h, _ := newTestServer(t)
	for _, e := range []string{
		`{"legalEntityId":"` + testLE + `","sourceType":"Manual","expectedDate":"2026-10-25","amount":"100.00","currency":"EUR"}`,
		`{"legalEntityId":"` + testLE + `","sourceType":"AP","expectedDate":"2026-10-25","amount":"-40.00","currency":"EUR"}`,
	} {
		if rec := call(h, http.MethodPost, "/v1/treasury/cash-forecast-entries", bytes.NewBufferString(e), "application/json", nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("entry without Idempotency-Key: status %d, want 400", rec.Code)
		}
		rec := call(h, http.MethodPost, "/v1/treasury/cash-forecast-entries", bytes.NewBufferString(e), "application/json", map[string]string{"Idempotency-Key": ids.New()})
		if rec.Code != http.StatusCreated {
			t.Fatalf("create entry: status %d body %s", rec.Code, rec.Body)
		}
	}
	rec := call(h, http.MethodGet, "/v1/treasury/cash-forecast?legalEntityId="+testLE+"&asOf=2026-10-20", nil, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("forecast: status %d body %s", rec.Code, rec.Body)
	}
	var fc domain.Forecast
	if err := json.Unmarshal(rec.Body.Bytes(), &fc); err != nil {
		t.Fatal(err)
	}
	if len(fc.Buckets) != 1 || fc.Buckets[0].Key != "2026-10-25" {
		t.Fatalf("buckets = %+v", fc.Buckets)
	}
	if !fc.Buckets[0].Net.Equal(decimal.RequireFromString("60")) || !fc.Buckets[0].Inflow.Equal(decimal.RequireFromString("100")) {
		t.Fatalf("bucket = %+v", fc.Buckets[0])
	}
	if bad := call(h, http.MethodGet, "/v1/treasury/cash-forecast?legalEntityId="+testLE+"&layout=hourly", nil, "", nil); bad.Code != http.StatusBadRequest {
		t.Fatalf("bad layout: status %d", bad.Code)
	}
}

func TestReady_ReflectsPostgres(t *testing.T) {
	fs := newFakeStore()
	a := New(fs)
	rec := httptest.NewRecorder()
	a.Ready()(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("ready: status %d", rec.Code)
	}
	fs.pingErr = errors.New("db down")
	rec = httptest.NewRecorder()
	a.Ready()(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready with db down: status %d", rec.Code)
	}
}

func TestBankAccountLifecycle_DeactivateAndClose(t *testing.T) {
	h, _ := newTestServer(t)
	acct := createAccount(t, h, "k-life")
	base := "/v1/treasury/bank-accounts/" + acct.ID
	step := func(action, etag string, want int) *httptest.ResponseRecorder {
		t.Helper()
		rec := call(h, http.MethodPost, base+":"+action, nil, "", map[string]string{"If-Match": etag})
		if rec.Code != want {
			t.Fatalf("%s with %s: status %d, want %d; body %s", action, etag, rec.Code, want, rec.Body)
		}
		return rec
	}
	if got := problemCode(t, step("close", `"1"`, http.StatusUnprocessableEntity)); got != store.CodeAccountState {
		t.Fatalf("closing a draft account: code %q, want %s", got, store.CodeAccountState)
	}
	step("activate", `"1"`, http.StatusOK)
	step("deactivate", `"2"`, http.StatusOK)
	rec := step("close", `"3"`, http.StatusOK)
	var closed store.BankAccount
	if err := json.Unmarshal(rec.Body.Bytes(), &closed); err != nil {
		t.Fatal(err)
	}
	if closed.Status != "closed" || closed.Version != 4 {
		t.Fatalf("after close: %+v", closed)
	}
	step("activate", `"4"`, http.StatusUnprocessableEntity)
}

func TestBankAccountTransitions_NeedApproveDuty(t *testing.T) {
	h, _ := newTestServer(t)
	acct := createAccount(t, h, "k-duty")
	for _, action := range []string{"activate", "deactivate", "close"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/treasury/bank-accounts/"+acct.ID+":"+action, nil)
		req.Header.Set(tenant.HeaderTenant, testTenant)
		req.Header.Set(tenant.HeaderDuties, "treasury.view,treasury.bank-account.write")
		req.Header.Set("If-Match", `"1"`)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s without approve duty: status %d, want 403", action, rec.Code)
		}
	}
}

func TestImportStatement_ReturnsWarningsAndMapsStoreErrors(t *testing.T) {
	h, fs := newTestServer(t)
	acct := createAccount(t, h, "k-warn")
	data, err := os.ReadFile("../../testdata/camt053/sample-08.xml")
	if err != nil {
		t.Fatal(err)
	}
	fs.importErr = &domain.Error{Code: domain.CodeStatementConflict, Detail: "different closing balance"}
	body, ct := multipartStatement(t, acct.ID, data)
	rec := call(h, http.MethodPost, "/v1/treasury/statements/import", body, ct, nil)
	if rec.Code != http.StatusConflict || problemCode(t, rec) != domain.CodeStatementConflict {
		t.Fatalf("conflict: status %d body %s, want 409 %s", rec.Code, rec.Body, domain.CodeStatementConflict)
	}
	fs.importErr = &domain.Error{Code: domain.CodeStatementCurrency, Detail: "currency differs"}
	body, ct = multipartStatement(t, acct.ID, data)
	if rec := call(h, http.MethodPost, "/v1/treasury/statements/import", body, ct, nil); rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeStatementCurrency {
		t.Fatalf("currency: status %d body %s", rec.Code, rec.Body)
	}
	fs.importErr = nil
	body, ct = multipartStatement(t, acct.ID, data)
	rec = call(h, http.MethodPost, "/v1/treasury/statements/import", body, ct, nil)
	var resp struct {
		ID       string          `json:"id"`
		Warnings []store.Warning `json:"warnings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusCreated || resp.ID == "" || resp.Warnings == nil {
		t.Fatalf("import: status %d body %s; want 201 with an id and a warnings array", rec.Code, rec.Body)
	}
}

func TestImportStatement_RejectsOversizeAndMissingParts(t *testing.T) {
	fs := newFakeStore()
	a, err := New(fs).WithMaxFileMB(1)
	if err != nil {
		t.Fatal(err)
	}
	r := httpx.NewRouter()
	a.Register(r)
	big, ct := multipartStatement(t, ids.New(), bytes.Repeat([]byte("x"), 2<<20))
	if rec := call(r, http.MethodPost, "/v1/treasury/statements/import", big, ct, nil); rec.Code != http.StatusRequestEntityTooLarge || problemCode(t, rec) != CodeFileTooLarge {
		t.Fatalf("oversize: status %d body %s", rec.Code, rec.Body)
	}
	noAccount, ct := multipartStatement(t, "not-a-uuid", []byte("<x/>"))
	if rec := call(r, http.MethodPost, "/v1/treasury/statements/import", noAccount, ct, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad bankAccountId: status %d", rec.Code)
	}
	if _, err := New(fs).WithMaxFileMB(0); err == nil {
		t.Fatal("WithMaxFileMB(0) accepted; want an error")
	}
}

func TestStatementLines_ZeroLimitMeansFullPageWithCursor(t *testing.T) {
	h, fs := newTestServer(t)
	stID := ids.New()
	fs.statements[stID] = store.Statement{ID: stID}
	for i := 1; i <= store.MaxLinesPage+1; i++ {
		fs.lines[stID] = append(fs.lines[stID], store.StatementLine{LineNo: i, Amount: "1.00", Currency: "EUR", Status: "unmatched"})
	}
	rec := call(h, http.MethodGet, "/v1/treasury/statements/"+stID+"/lines?limit=0", nil, "", nil)
	var page struct {
		Items []store.StatementLine `json:"items"`
		Next  *int                  `json:"nextAfterLineNo"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != store.MaxLinesPage || page.Next == nil || *page.Next != store.MaxLinesPage {
		t.Fatalf("limit=0: %d items, next %v; want %d items and a cursor", len(page.Items), page.Next, store.MaxLinesPage)
	}
}

func TestCreateBankAccount_StatementAccountIsNormalizedAndMasked(t *testing.T) {
	h, fs := newTestServer(t)
	b, _ := json.Marshal(map[string]any{
		"legalEntityId": testLE, "accountCode": "US-01", "iban": testIBAN, "currency": "USD", "mainAccountId": testMainAcct,
		"statementAccount": " 1234 56789 ",
	})
	rec := call(h, http.MethodPost, "/v1/treasury/bank-accounts", bytes.NewBuffer(b), "application/json", map[string]string{idempotencyKey: "k-us"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if fs.lastAccount.StatementAccount != "123456789" {
		t.Fatalf("store received %q, want the normalized number", fs.lastAccount.StatementAccount)
	}
	if strings.Contains(rec.Body.String(), "123456789") || !strings.Contains(rec.Body.String(), `"statementAccountLast4":"6789"`) {
		t.Fatalf("response must carry only the last four characters: %s", rec.Body)
	}
}

func TestImportStatement_DetectsBAI2AndMT940(t *testing.T) {
	bai2 := "01,BANK,CO,241001,1200,1,,,2/\n02,CO,BANK,1,241001,1200,USD,2/\n03,123456789,USD,010,100000,,,015,95000,,/\n" +
		"16,475,5000,Z,BREF,CHK1001,CHECK PAID/\n49,195000,3/\n98,195000,1,5/\n99,195000,1,7/\n"
	mt940 := ":20:REF1\n:25:DE89370400440532013000\n:28C:00007/001\n:60F:C241001EUR100,00\n" +
		":61:2410011001C25,00NTRFINV-1//B1\n:86:INVOICE 1\n:62F:C241001EUR125,00\n"
	for _, tc := range []struct{ name, file, format, statementID string }{
		{"BAI2", bai2, domain.FormatBAI2, "123456789-2024-10-01"},
		{"MT940", mt940, domain.FormatMT940, "REF1/DE89370400440532013000/00007/001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newTestServer(t)
			acct := createAccount(t, h, "k-"+tc.name)
			body, ct := multipartStatement(t, acct.ID, []byte(tc.file))
			rec := call(h, http.MethodPost, "/v1/treasury/statements/import", body, ct, nil)
			if rec.Code != http.StatusCreated {
				t.Fatalf("status %d body %s", rec.Code, rec.Body)
			}
			var st store.Statement
			if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
				t.Fatal(err)
			}
			if st.Format != tc.format || st.StatementID != tc.statementID || st.LineCount != 1 {
				t.Fatalf("statement = %+v", st)
			}
		})
	}
}

func TestImportStatement_RefusesMultiStatementFiles(t *testing.T) {
	h, _ := newTestServer(t)
	acct := createAccount(t, h, "k-multi")
	two := ":20:A\n:25:X\n:60F:C241001EUR1,00\n:62F:C241001EUR1,00\n:20:B\n:25:X\n:60F:C241002EUR1,00\n:62F:C241002EUR1,00\n"
	body, ct := multipartStatement(t, acct.ID, []byte(two))
	rec := call(h, http.MethodPost, "/v1/treasury/statements/import", body, ct, nil)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeStatementFormat {
		t.Fatalf("status %d body %s, want 422 TRS-0493", rec.Code, rec.Body)
	}
}

func TestImportStatement_ParseErrorReportsLine(t *testing.T) {
	h, _ := newTestServer(t)
	acct := createAccount(t, h, "k-line")
	bad := ":20:A\n:25:X\n:60F:C241001EUR1,00\n:61:NOT-AN-ENTRY\n:62F:C241001EUR1,00\n"
	body, ct := multipartStatement(t, acct.ID, []byte(bad))
	rec := call(h, http.MethodPost, "/v1/treasury/statements/import", body, ct, nil)
	var p struct {
		Code   string            `json:"code"`
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusUnprocessableEntity || p.Code != domain.CodeStatementFormat || p.Errors["line"] != "4" {
		t.Fatalf("status %d problem %+v", rec.Code, p)
	}
}

func TestImportStatement_CamtParseErrorReportsEntry(t *testing.T) {
	h, _ := newTestServer(t)
	acct := createAccount(t, h, "k-entry")
	bad := `<Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.053.001.08"><BkToCstmrStmt><Stmt><Id>S</Id>` +
		`<Acct><Id><IBAN>` + testIBAN + `</IBAN></Id><Ccy>EUR</Ccy></Acct>` +
		`<Bal><Tp><CdOrPrtry><Cd>OPBD</Cd></CdOrPrtry></Tp><Amt Ccy="EUR">1</Amt><CdtDbtInd>CRDT</CdtDbtInd><Dt><Dt>2026-10-22</Dt></Dt></Bal>` +
		`<Bal><Tp><CdOrPrtry><Cd>CLBD</Cd></CdOrPrtry></Tp><Amt Ccy="EUR">1</Amt><CdtDbtInd>CRDT</CdtDbtInd><Dt><Dt>2026-10-22</Dt></Dt></Bal>` +
		`<Ntry><Amt Ccy="EUR">1</Amt><CdtDbtInd>XXXX</CdtDbtInd><BookgDt><Dt>2026-10-22</Dt></BookgDt></Ntry></Stmt></BkToCstmrStmt></Document>`
	body, ct := multipartStatement(t, acct.ID, []byte(bad))
	rec := call(h, http.MethodPost, "/v1/treasury/statements/import", body, ct, nil)
	var p struct {
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusUnprocessableEntity || p.Errors["entry"] != "1" || p.Errors["line"] != "" {
		t.Fatalf("status %d errors %v, want entry 1", rec.Code, p.Errors)
	}
}

func TestCreateBankAccount_RefusesCallerWithoutLegalEntityScope(t *testing.T) {
	h, _ := newTestServer(t)
	rec := call(h, http.MethodPost, "/v1/treasury/bank-accounts", createAccountBody(t), "application/json",
		map[string]string{idempotencyKey: "k-noscope", tenant.HeaderLegalEntity: ""})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body %s", rec.Code, rec.Body)
	}
}
