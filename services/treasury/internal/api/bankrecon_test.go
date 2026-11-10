package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

const (
	reconStatement = "0190b7a0-0000-7000-8000-000000000010"
	testLedger     = "0190b7a0-0000-7000-8000-000000000011"
	testBankAcct   = "0190b7a0-0000-7000-8000-000000000012"
	reconPath      = "/v1/treasury/bank-reconciliations/"
)

type fakeRecons struct {
	mu          sync.Mutex
	rec         store.Reconciliation
	amount      string
	txs         map[int]*store.LineTransaction
	finishCalls int
	reopenNote  string
	query       store.ReconciliationQuery
}

func newFakeRecons() *fakeRecons {
	return &fakeRecons{
		rec:    store.Reconciliation{ID: ids.New(), LegalEntityID: testLE, BankAccountID: testBankAcct, StatementID: reconStatement, Currency: "EUR", State: store.StateOpen, Version: 3},
		amount: "-12.50", txs: map[int]*store.LineTransaction{},
	}
}

func (f *fakeRecons) CreateReconciliation(context.Context, store.NewReconciliation, string) (store.Reconciliation, error) {
	return f.rec, nil
}

func (f *fakeRecons) GetReconciliation(context.Context, string) (store.Reconciliation, error) {
	return f.rec, nil
}

func (f *fakeRecons) ListReconciliations(_ context.Context, q store.ReconciliationQuery) (store.ReconciliationPage, error) {
	f.query = q
	return store.ReconciliationPage{Items: []store.Reconciliation{f.rec}, NextCursor: "next"}, nil
}

func (f *fakeRecons) SetReconciliationBalances(context.Context, string, int, decimal.Decimal, decimal.Decimal) (store.Reconciliation, error) {
	return f.rec, nil
}

func (f *fakeRecons) CloseReconciliation(context.Context, string, int) (store.Reconciliation, error) {
	f.rec.State = store.StateClosed
	return f.rec, nil
}

func (f *fakeRecons) ReopenReconciliation(_ context.Context, _ string, _ int, reason string) (store.Reconciliation, error) {
	f.reopenNote = reason
	f.rec.State = store.StateReopened
	return f.rec, nil
}

func (f *fakeRecons) ExcludeLine(context.Context, string, int, string) (store.StatementLine, error) {
	return store.StatementLine{LineNo: 1, Status: "excluded"}, nil
}

func (f *fakeRecons) IncludeLine(context.Context, string, int) (store.StatementLine, error) {
	return store.StatementLine{LineNo: 1, Status: "unmatched"}, nil
}

func (f *fakeRecons) BeginLineTransaction(_ context.Context, in store.NewLineTransaction) (store.LineTransaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.txs[in.LineNo]
	if !ok {
		t = &store.LineTransaction{ID: ids.New(), ReconciliationID: in.ReconciliationID, LegalEntityID: testLE, BankAccountID: testBankAcct, StatementID: reconStatement,
			LineNo: in.LineNo, Kind: in.Kind, Amount: f.amount, Currency: "EUR", PostingDate: "2026-10-22", Status: store.TransactionPending, Version: 1}
		f.txs[in.LineNo] = t
	}
	out := *t
	switch t.Status {
	case store.TransactionPosted:
		out.Replayed = true
	case store.TransactionRefused:
		t.Status, out.Status = store.TransactionPending, store.TransactionPending
	}
	return out, nil
}

func (f *fakeRecons) FinishLineTransaction(_ context.Context, id, entryID string) (store.LineTransaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.txs {
		if t.ID == id {
			if t.Status == store.TransactionPosted {
				out := *t
				out.Replayed = true
				return out, nil
			}
			match := ids.New()
			t.Status, t.LedgerEntryID, t.MatchID = store.TransactionPosted, &entryID, &match
			f.finishCalls++
			return *t, nil
		}
	}
	return store.LineTransaction{}, errors.New("unknown transaction")
}

func (f *fakeRecons) RefuseLineTransaction(_ context.Context, id, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.txs {
		if t.ID == id {
			t.Status = store.TransactionRefused
		}
	}
	return nil
}

func (f *fakeRecons) status(lineNo int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.txs[lineNo]; ok {
		return t.Status
	}
	return ""
}

func testProfile() map[string]GLProfile {
	return map[string]GLProfile{testLE: {LegalEntityID: testLE, LedgerID: testLedger, BankAccounts: map[string]string{testBankAcct: "1100"},
		FeeAccount: "6800", InterestAccount: "7100", SuspenseAccount: "2999"}}
}

type reconServer struct {
	h      http.Handler
	recons *fakeRecons
	ledger *ledgerclient.Fake
	id     string
}

func newReconServer(t *testing.T, profiles map[string]GLProfile) *reconServer {
	t.Helper()
	recons, ledger := newFakeRecons(), ledgerclient.NewFake()
	r := httpx.NewRouter()
	New(newFakeStore()).WithReconciliations(recons).WithLedger(ledger, profiles).Register(r)
	return &reconServer{h: r, recons: recons, ledger: ledger, id: recons.rec.ID}
}

func (s *reconServer) post(path, body string, headers map[string]string) (code int, problem string, raw string) {
	rec := call(s.h, http.MethodPost, reconPath+s.id+path, bytes.NewBufferString(body), "application/json", headers)
	if rec.Code >= 400 {
		var p struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		problem = p.Code
	}
	return rec.Code, problem, rec.Body.String()
}

func TestCreateLineTransaction_PostsOnceAndAnswersARepeatWithTheSameTransaction(t *testing.T) {
	s := newReconServer(t, testProfile())
	code, _, body := s.post("/lines/4:create-transaction", `{"kind":"bank_fee"}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("status = %d %s; want 201", code, body)
	}
	entries := s.ledger.Entries()
	if len(entries) != 1 || entries[0].IdempotencyKey != "treasury-line-"+reconStatement+"-4" || entries[0].LedgerID != testLedger || entries[0].SourceType != "bank_statement_line" {
		t.Fatalf("ledger entries = %+v; want one entry keyed by the statement line", entries)
	}
	lines := entries[0].Lines
	if len(lines) != 2 || lines[0].Account != "1100" || !lines[0].Credit || !lines[0].Amount.Equal(decimal.RequireFromString("12.50")) ||
		lines[1].Account != "6800" || lines[1].Credit || !lines[1].Amount.Equal(decimal.RequireFromString("12.50")) {
		t.Fatalf("lines = %+v; want credit bank 1100 and debit fee 6800 of 12.50", lines)
	}
	code, _, body = s.post("/lines/4:create-transaction", `{"kind":"bank_fee"}`, nil)
	if code != http.StatusOK || len(s.ledger.Entries()) != 1 || s.recons.finishCalls != 1 {
		t.Fatalf("repeat = %d %s with %d ledger entries and %d finishes; want 200, one entry, one finish", code, body, len(s.ledger.Entries()), s.recons.finishCalls)
	}
	var out store.LineTransaction
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.Status != store.TransactionPosted || out.LedgerEntryID == nil || out.MatchID == nil {
		t.Fatalf("repeat body = %s (%v); want the posted transaction with its entry and match", body, err)
	}
}

func TestCreateLineTransaction_ALostLedgerAnswerIsRetriedWithoutPostingTwice(t *testing.T) {
	s := newReconServer(t, testProfile())
	s.ledger.LoseAnswers(errors.New("connection reset"))
	code, problem, body := s.post("/lines/4:create-transaction", `{"kind":"bank_fee"}`, nil)
	if code != http.StatusServiceUnavailable || problem != CodeLedgerDown || !strings.Contains(body, "safe to repeat") {
		t.Fatalf("lost answer = %d %s %s; want 503 %s saying it is safe to repeat", code, problem, body, CodeLedgerDown)
	}
	if s.recons.finishCalls != 0 || s.recons.status(4) != store.TransactionPending {
		t.Fatalf("after a lost answer: %d finishes, status %q; want nothing matched and the transaction pending", s.recons.finishCalls, s.recons.status(4))
	}
	s.ledger.LoseAnswers(nil)
	if code, _, body = s.post("/lines/4:create-transaction", `{"kind":"bank_fee"}`, nil); code != http.StatusCreated {
		t.Fatalf("retry = %d %s; want 201", code, body)
	}
	if len(s.ledger.Entries()) != 1 || s.recons.finishCalls != 1 {
		t.Fatalf("after the retry: %d ledger entries, %d finishes; want one of each", len(s.ledger.Entries()), s.recons.finishCalls)
	}
}

func TestCreateLineTransaction_ALedgerRefusalMatchesNothingAndExplainsInPlainLanguage(t *testing.T) {
	s := newReconServer(t, testProfile())
	s.ledger.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	code, problem, body := s.post("/lines/4:create-transaction", `{"kind":"bank_fee"}`, nil)
	if code != http.StatusUnprocessableEntity || problem != CodeLedgerRefused || !strings.Contains(body, ledgerclient.CodePeriodClosed) || !strings.Contains(body, "not matched") {
		t.Fatalf("refusal = %d %s %s; want 422 %s naming %s and that the line was not matched", code, problem, body, CodeLedgerRefused, ledgerclient.CodePeriodClosed)
	}
	if s.recons.finishCalls != 0 || s.recons.status(4) != store.TransactionRefused || len(s.ledger.Entries()) != 0 {
		t.Fatalf("after a refusal: %d finishes, status %q, %d entries; want nothing matched or posted and the transaction released", s.recons.finishCalls, s.recons.status(4), len(s.ledger.Entries()))
	}
	s.ledger.FailPosts(nil)
	if code, _, body = s.post("/lines/4:create-transaction", `{"kind":"bank_fee"}`, nil); code != http.StatusCreated || s.recons.finishCalls != 1 {
		t.Fatalf("retry after the cause is fixed = %d %s with %d finishes; want 201 and one finish", code, body, s.recons.finishCalls)
	}
}

func TestCreateLineTransaction_RefusesWithoutAProfileAMappedAccountOrAValidKind(t *testing.T) {
	noProfile := newReconServer(t, nil)
	if code, problem, _ := noProfile.post("/lines/4:create-transaction", `{"kind":"bank_fee"}`, nil); code != http.StatusUnprocessableEntity || problem != CodeLedgerNoProfile {
		t.Fatalf("no profile = %d %s; want 422 %s", code, problem, CodeLedgerNoProfile)
	}
	if len(noProfile.recons.txs) != 0 {
		t.Fatal("a transaction was begun without a posting profile")
	}
	unmapped := testProfile()
	p := unmapped[testLE]
	p.BankAccounts = map[string]string{ids.New(): "1100"}
	unmapped[testLE] = p
	s := newReconServer(t, unmapped)
	if code, problem, body := s.post("/lines/4:create-transaction", `{"kind":"bank_fee"}`, nil); code != http.StatusUnprocessableEntity || problem != CodeLedgerRefused || !strings.Contains(body, CodeLedgerNoProfile) {
		t.Fatalf("unmapped bank account = %d %s %s; want 422 %s naming %s", code, problem, body, CodeLedgerRefused, CodeLedgerNoProfile)
	}
	if len(s.ledger.Entries()) != 0 {
		t.Fatal("the ledger was called without a ledger account for the bank account")
	}
	good := newReconServer(t, testProfile())
	if code, problem, _ := good.post("/lines/4:create-transaction", `{"kind":"gift"}`, nil); code != http.StatusBadRequest || problem != "COM-0001" {
		t.Fatalf("bad kind = %d %s; want 400 COM-0001", code, problem)
	}
	if code, problem, _ := good.post("/lines/0:create-transaction", `{"kind":"bank_fee"}`, nil); code != http.StatusNotFound || problem != store.CodeLineNotFound {
		t.Fatalf("line 0 = %d %s; want 404 %s", code, problem, store.CodeLineNotFound)
	}
	rec := call(good.h, http.MethodPost, reconPath+"not-a-uuid/lines/4:create-transaction", bytes.NewBufferString(`{"kind":"bank_fee"}`), "application/json", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bad reconciliation id = %d; want 404", rec.Code)
	}
}

func TestCreateLineTransaction_WithoutALedgerClientAnswers503(t *testing.T) {
	r := httpx.NewRouter()
	recons := newFakeRecons()
	New(newFakeStore()).WithReconciliations(recons).Register(r)
	rec := call(r, http.MethodPost, reconPath+recons.rec.ID+"/lines/4:create-transaction", bytes.NewBufferString(`{"kind":"bank_fee"}`), "application/json", nil)
	if rec.Code != http.StatusServiceUnavailable || problemCode(t, rec) != CodeReconNotEnabled || len(recons.txs) != 0 {
		t.Fatalf("no ledger = %d %s; want 503 %s and nothing begun", rec.Code, rec.Body, CodeReconNotEnabled)
	}
}

func TestBankReconciliationRoutes_AnswerNotEnabledWithoutAStore(t *testing.T) {
	r := httpx.NewRouter()
	New(newFakeStore()).Register(r)
	rec := call(r, http.MethodGet, "/v1/treasury/bank-reconciliations", nil, "", nil)
	if rec.Code != http.StatusServiceUnavailable || problemCode(t, rec) != CodeReconNotEnabled {
		t.Fatalf("no store = %d %s; want 503 %s", rec.Code, rec.Body, CodeReconNotEnabled)
	}
}

func TestCloseAndReopen_NeedTheirOwnDutyIfMatchAndAReason(t *testing.T) {
	s := newReconServer(t, testProfile())
	if code, problem, _ := s.post(":close", ``, nil); code != http.StatusPreconditionRequired {
		t.Fatalf("close without If-Match = %d %s; want 428", code, problem)
	}
	code, _, body := s.post(":close", ``, map[string]string{"If-Match": `"3"`})
	if code != http.StatusOK || !strings.Contains(body, `"state":"closed"`) {
		t.Fatalf("close = %d %s; want 200 closed", code, body)
	}
	if code, problem, _ := s.post(":reopen", `{}`, map[string]string{"If-Match": `"4"`}); code != http.StatusBadRequest || problem != "COM-0001" {
		t.Fatalf("reopen without a reason = %d %s; want 400 COM-0001", code, problem)
	}
	if code, _, body = s.post(":reopen", `{"reason":"  late invoice  "}`, map[string]string{"If-Match": `"4"`}); code != http.StatusOK || s.recons.reopenNote != "late invoice" {
		t.Fatalf("reopen = %d %s with reason %q; want 200 and the trimmed reason", code, body, s.recons.reopenNote)
	}
}

func TestBankReconciliationDuties_AreSeparate(t *testing.T) {
	s := newReconServer(t, testProfile())
	cases := []struct {
		name, method, path, duty string
		want                     int
	}{
		{"close with view only", http.MethodPost, ":close", "treasury.reconciliation.view", http.StatusForbidden},
		{"close with reopen only", http.MethodPost, ":close", "treasury.reconciliation.reopen", http.StatusForbidden},
		{"reopen with close only", http.MethodPost, ":reopen", "treasury.reconciliation.close", http.StatusForbidden},
		{"reopen with write only", http.MethodPost, ":reopen", "treasury.reconciliation.write", http.StatusForbidden},
		{"create-transaction with write only", http.MethodPost, "/lines/4:create-transaction", "treasury.reconciliation.write", http.StatusForbidden},
		{"exclude with post only", http.MethodPost, "/lines/4:exclude", "treasury.reconciliation.post", http.StatusForbidden},
		{"get with the general view duty", http.MethodGet, "", "treasury.view", http.StatusForbidden},
		{"get with its own view duty", http.MethodGet, "", "treasury.reconciliation.view", http.StatusOK},
	}
	for _, c := range cases {
		rec := call(s.h, c.method, reconPath+s.id+c.path, bytes.NewBufferString(`{"reason":"x","kind":"bank_fee"}`), "application/json",
			map[string]string{tenant.HeaderDuties: c.duty, "If-Match": `"3"`})
		if rec.Code != c.want {
			t.Errorf("%s = %d %s; want %d", c.name, rec.Code, rec.Body, c.want)
		}
	}
	for _, c := range []struct{ path, duty string }{
		{":close", "treasury.reconciliation.close"}, {":reopen", "treasury.reconciliation.reopen"},
		{"/lines/4:create-transaction", "treasury.reconciliation.post"}, {"/lines/4:exclude", "treasury.reconciliation.write"},
	} {
		rec := call(s.h, http.MethodPost, reconPath+s.id+c.path, bytes.NewBufferString(`{"reason":"x","kind":"bank_fee"}`), "application/json",
			map[string]string{tenant.HeaderDuties: c.duty, "If-Match": `"3"`})
		if rec.Code == http.StatusForbidden {
			t.Errorf("%s with %s = 403; want it served", c.path, c.duty)
		}
	}
}

func TestListBankReconciliations_ValidatesFiltersAndPassesThemOn(t *testing.T) {
	s := newReconServer(t, testProfile())
	bad := []string{"state=archived", "periodFrom=2026-13-01", "limit=0", "limit=101", "bankAccountId=nope", "legalEntityId=nope"}
	for _, q := range bad {
		rec := call(s.h, http.MethodGet, "/v1/treasury/bank-reconciliations?"+q, nil, "", nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d; want 400", q, rec.Code)
		}
	}
	rec := call(s.h, http.MethodGet, "/v1/treasury/bank-reconciliations?state=closed&bankAccountId="+testBankAcct+"&periodFrom=2026-10-01&periodTo=2026-10-31&limit=5&cursor=abc", nil, "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"nextCursor":"next"`) {
		t.Fatalf("list = %d %s; want 200 with the next cursor", rec.Code, rec.Body)
	}
	q := s.recons.query
	if q.State != "closed" || q.BankAccountID != testBankAcct || q.PeriodFrom != "2026-10-01" || q.PeriodTo != "2026-10-31" || q.Limit != 5 || q.Cursor != "abc" {
		t.Fatalf("query = %+v; want every filter passed on", q)
	}
}

func TestCreateBankReconciliation_ValidatesAmounts(t *testing.T) {
	s := newReconServer(t, testProfile())
	post := func(body string) *int {
		rec := call(s.h, http.MethodPost, "/v1/treasury/bank-reconciliations", bytes.NewBufferString(body), "application/json", map[string]string{idempotencyKey: "k-1"})
		return &rec.Code
	}
	if got := *post(`{"statementId":"` + reconStatement + `","bookBalance":"1450.00"}`); got != http.StatusCreated {
		t.Fatalf("valid create = %d; want 201", got)
	}
	for _, body := range []string{`{"statementId":"x","bookBalance":"1450.00"}`, `{"statementId":"` + reconStatement + `","bookBalance":"abc"}`, `{"statementId":"` + reconStatement + `","bookBalance":"1","outstandingAmount":"1e999"}`} {
		if got := *post(body); got != http.StatusBadRequest {
			t.Errorf("%s = %d; want 400", body, got)
		}
	}
	rec := call(s.h, http.MethodPost, "/v1/treasury/bank-reconciliations", bytes.NewBufferString(`{"statementId":"`+reconStatement+`","bookBalance":"1"}`), "application/json", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create without Idempotency-Key = %d; want 400", rec.Code)
	}
}

func TestLineEntry_BooksMoneyInAndOutOnTheRightSides(t *testing.T) {
	p := testProfile()[testLE]
	cases := []struct {
		kind, amount, debit, credit string
	}{
		{domain.LineBankFee, "-12.50", "6800", "1100"},
		{domain.LineInterest, "3.10", "1100", "7100"},
		{domain.LineInterest, "-3.10", "7100", "1100"},
		{domain.LineUnknownReceipt, "40.00", "1100", "2999"},
	}
	for _, c := range cases {
		e, code, detail := lineEntry(p, reconStatement, store.LineTransaction{BankAccountID: testBankAcct, LineNo: 2, Kind: c.kind, Amount: c.amount, Currency: "EUR", PostingDate: "2026-10-22"})
		if code != "" || len(e.Lines) != 2 {
			t.Fatalf("%s %s: %s %s %+v", c.kind, c.amount, code, detail, e)
		}
		debit, credit := e.Lines[0], e.Lines[1]
		if debit.Credit {
			debit, credit = credit, debit
		}
		if debit.Account != c.debit || credit.Account != c.credit || !debit.Amount.Equal(credit.Amount) || !debit.Amount.IsPositive() {
			t.Errorf("%s %s: debit %s credit %s; want debit %s credit %s", c.kind, c.amount, debit.Account, credit.Account, c.debit, c.credit)
		}
	}
	if _, code, _ := lineEntry(p, reconStatement, store.LineTransaction{BankAccountID: testBankAcct, Kind: domain.LineBankFee, Amount: "-12.505", Currency: "EUR"}); code != store.CodeLineKind {
		t.Errorf("a fee with three decimal places on EUR: code = %q; want %s", code, store.CodeLineKind)
	}
}

func TestParseGLProfiles_RequiresEveryAccountAndRefusesDuplicates(t *testing.T) {
	valid := `[{"legalEntityId":"` + testLE + `","ledgerId":"` + testLedger + `","bankAccounts":{"` + testBankAcct + `":"1100"},"feeAccount":"6800","interestAccount":"7100","suspenseAccount":"2999"}]`
	got, err := ParseGLProfiles(valid)
	if err != nil || got[testLE].BankAccounts[testBankAcct] != "1100" {
		t.Fatalf("valid = %+v, %v", got, err)
	}
	for name, raw := range map[string]string{
		"not json":             `nope`,
		"unknown field":        strings.Replace(valid, `"feeAccount"`, `"fee":"1","feeAccount"`, 1),
		"no suspense":          strings.Replace(valid, `"suspenseAccount":"2999"`, `"suspenseAccount":""`, 1),
		"bad bank account":     strings.Replace(valid, testBankAcct, "nope", 1),
		"bad ledger":           strings.Replace(valid, testLedger, "nope", 1),
		"duplicate entity":     valid[:len(valid)-1] + "," + valid[1:],
		"account with a space": strings.Replace(valid, `"6800"`, `"68 00"`, 1),
	} {
		if _, err := ParseGLProfiles(raw); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
