package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
)

const closerUser, reopenerUser = "user-test", "user-reopener"

func ctxAs(n int, legalEntityID, user string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: legalEntityID, UserID: user, Duties: testkit.ScopeDuties(legalEntityID)})
}

func camtOn(id, date string, amounts ...string) []byte {
	closing := decimal.RequireFromString("1000.00")
	var entries strings.Builder
	for i, a := range amounts {
		closing = closing.Add(decimal.RequireFromString(a))
		fmt.Fprintf(&entries, `<Ntry><Amt Ccy="EUR">%s</Amt><CdtDbtInd>CRDT</CdtDbtInd><BookgDt><Dt>%s</Dt></BookgDt><NtryDtls><TxDtls><Refs><EndToEndId>%s-%d</EndToEndId></Refs></TxDtls></NtryDtls></Ntry>`, a, date, id, i)
	}
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.053.001.08"><BkToCstmrStmt><Stmt><Id>%s</Id>`+
		`<Acct><Id><IBAN>%s</IBAN></Id><Ccy>EUR</Ccy></Acct>`+
		`<Bal><Tp><CdOrPrtry><Cd>OPBD</Cd></CdOrPrtry></Tp><Amt Ccy="EUR">1000.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><Dt><Dt>%s</Dt></Dt></Bal>`+
		`<Bal><Tp><CdOrPrtry><Cd>CLBD</Cd></CdOrPrtry></Tp><Amt Ccy="EUR">%s</Amt><CdtDbtInd>CRDT</CdtDbtInd><Dt><Dt>%s</Dt></Dt></Bal>%s</Stmt></BkToCstmrStmt></Document>`,
		id, testIBAN, date, closing.StringFixed(2), date, entries.String()))
}

type bankFixture struct {
	*reconFixture
	rec Reconciliation
}

func newBankFixture(t *testing.T) *bankFixture {
	t.Helper()
	f := newReconFixture(t)
	rec, err := f.s.CreateReconciliation(f.ctx, NewReconciliation{StatementID: f.statement.ID, BookBalance: decimal.RequireFromString("1457.00")}, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	return &bankFixture{reconFixture: f, rec: rec}
}

func (f *bankFixture) matchLine(t *testing.T, lineNo int, amount string) Match {
	t.Helper()
	m, err := f.s.CreateManualMatch(f.ctx, NewMatch{StatementID: f.statement.ID, LegalEntityID: f.le, LineNos: []int{lineNo},
		Items: []MatchItem{{Type: "ledger", ID: fmt.Sprintf("entry-%d", lineNo), Currency: "EUR", Amount: amount}}, Reason: "test"}, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (f *bankFixture) matchLines123(t *testing.T) []Match {
	t.Helper()
	return []Match{f.matchLine(t, 1, "100.00"), f.matchLine(t, 2, "50.00"), f.matchLine(t, 3, "300.00")}
}

func (f *bankFixture) reload(t *testing.T) Reconciliation {
	t.Helper()
	rec, err := f.s.GetReconciliation(f.ctx, f.rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func (f *bankFixture) readyToClose(t *testing.T) Reconciliation {
	t.Helper()
	f.matchLines123(t)
	if _, err := f.s.ExcludeLine(f.ctx, f.rec.ID, 4, "bank rounding adjustment"); err != nil {
		t.Fatal(err)
	}
	rec, err := f.s.SetReconciliationBalances(f.ctx, f.rec.ID, f.reload(t).Version, decimal.RequireFromString("1450.00"), decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestCreateReconciliation_CarriesStatementBalancesAndReplays(t *testing.T) {
	f := newBankFixture(t)
	if f.rec.State != StateOpen || f.rec.OpeningBalance != "1000.00" || f.rec.ClosingBalance != "1457.00" || f.rec.BookBalance != "1457.00" {
		t.Fatalf("reconciliation = %+v; want an open one carrying the statement balances", f.rec)
	}
	if f.rec.Summary.Lines != 4 || f.rec.Summary.UnmatchedCount != 4 || f.rec.Summary.UnmatchedTotal != "457.00" || f.rec.Difference != "0.00" {
		t.Fatalf("summary = %+v difference %s; want 4 unmatched lines of 457.00", f.rec.Summary, f.rec.Difference)
	}
	_, err := f.s.CreateReconciliation(f.ctx, NewReconciliation{StatementID: f.statement.ID, BookBalance: decimal.RequireFromString("1457.00")}, ids.New())
	if got := problemCode(t, err); got != CodeReconExists {
		t.Fatalf("second reconciliation of a statement: code = %s; want %s", got, CodeReconExists)
	}
	_, err = f.s.CreateReconciliation(f.ctx, NewReconciliation{StatementID: f.statement.ID, BookBalance: decimal.RequireFromString("1457.005")}, ids.New())
	if got := problemCode(t, err); got != "COM-0001" {
		t.Fatalf("three decimal places on EUR: code = %s; want COM-0001", got)
	}
}

func TestCloseReconciliation_RefusesUnmatchedLinesAndANonZeroDifference(t *testing.T) {
	f := newBankFixture(t)
	if _, err := f.s.CloseReconciliation(f.ctx, f.rec.ID, f.rec.Version); problemCode(t, err) != CodeReconOpenLines {
		t.Fatalf("close with 4 unmatched lines: err = %v; want %s", err, CodeReconOpenLines)
	}
	f.matchLines123(t)
	if _, err := f.s.ExcludeLine(f.ctx, f.rec.ID, 4, "bank rounding adjustment"); err != nil {
		t.Fatal(err)
	}
	rec := f.reload(t)
	if rec.Summary.UnmatchedCount != 0 || rec.Summary.ExcludedCount != 1 || rec.Summary.MatchedCount != 3 || rec.Difference != "-7.00" {
		t.Fatalf("summary = %+v difference %s; want everything accounted for and a difference of -7.00", rec.Summary, rec.Difference)
	}
	if _, err := f.s.CloseReconciliation(f.ctx, f.rec.ID, rec.Version); problemCode(t, err) != domain.CodeReconcileDifference {
		t.Fatalf("close with a difference: err = %v; want %s", err, domain.CodeReconcileDifference)
	}
	if got := f.reload(t); got.State != StateOpen || got.ClosedBy != nil {
		t.Fatalf("a refused close left the reconciliation %+v", got)
	}
	rec, err := f.s.SetReconciliationBalances(f.ctx, f.rec.ID, rec.Version, decimal.RequireFromString("1450.00"), decimal.Zero)
	if err != nil || rec.Difference != "0.00" {
		t.Fatalf("set balances = %+v, %v; want a zero difference", rec, err)
	}
	if _, err := f.s.CloseReconciliation(f.ctx, f.rec.ID, rec.Version-1); problemCode(t, err) != CodeVersionConflict {
		t.Fatalf("close with a stale version: err = %v; want %s", err, CodeVersionConflict)
	}
	closed, err := f.s.CloseReconciliation(f.ctx, f.rec.ID, rec.Version)
	if err != nil || closed.State != StateClosed || closed.ClosedBy == nil || *closed.ClosedBy != closerUser || closed.ClosedAt == nil {
		t.Fatalf("close = %+v, %v; want closed by %s with a time", closed, err, closerUser)
	}
	again, err := f.s.CloseReconciliation(f.ctx, f.rec.ID, rec.Version)
	if err != nil || again.Version != closed.Version || !again.ClosedAt.Equal(*closed.ClosedAt) {
		t.Fatalf("second close = %+v, %v; want the closed reconciliation unchanged", again, err)
	}
	if n := count(t, f.admin, `select count(*) from bank_reconciliation_event where reconciliation_id = $1 and event = 'closed'`, f.rec.ID); n != 1 {
		t.Fatalf("closed events = %d; want 1", n)
	}
}

func TestCloseReconciliation_ConcurrentClosesCloseOnce(t *testing.T) {
	f := newBankFixture(t)
	rec := f.readyToClose(t)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	results := make([]Reconciliation, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = f.s.CloseReconciliation(f.ctx, f.rec.ID, rec.Version)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil || results[i].State != StateClosed || results[i].Version != rec.Version+1 {
			t.Fatalf("concurrent close %d = %+v, %v; want the same closed reconciliation", i, results[i], err)
		}
	}
	if n := count(t, f.admin, `select count(*) from bank_reconciliation_event where reconciliation_id = $1 and event = 'closed'`, f.rec.ID); n != 1 {
		t.Fatalf("closed events = %d; want 1", n)
	}
}

func TestClosedReconciliation_RefusesEveryChangeToItsLinesAndMatches(t *testing.T) {
	f := newBankFixture(t)
	matches := f.matchLines123(t)
	if _, err := f.s.ExcludeLine(f.ctx, f.rec.ID, 4, "bank rounding adjustment"); err != nil {
		t.Fatal(err)
	}
	rec, err := f.s.SetReconciliationBalances(f.ctx, f.rec.ID, f.reload(t).Version, decimal.RequireFromString("1450.00"), decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CloseReconciliation(f.ctx, f.rec.ID, rec.Version); err != nil {
		t.Fatal(err)
	}
	refused := func(name string, err error) {
		t.Helper()
		if got := problemCode(t, AsGuardError(err)); got != CodeReconClosed {
			t.Errorf("%s on a closed period: code = %s (%v); want %s", name, got, err, CodeReconClosed)
		}
	}
	_, err = f.s.Unmatch(f.ctx, matches[0].ID, matches[0].Version, "mistake")
	refused("unmatch", err)
	_, err = f.s.ExcludeLine(f.ctx, f.rec.ID, 4, "again")
	refused("exclude", err)
	_, err = f.s.IncludeLine(f.ctx, f.rec.ID, 4)
	refused("include", err)
	_, err = f.s.SetReconciliationBalances(f.ctx, f.rec.ID, f.reload(t).Version, decimal.RequireFromString("1.00"), decimal.Zero)
	refused("set balances", err)
	_, err = f.s.BeginLineTransaction(f.ctx, NewLineTransaction{ReconciliationID: f.rec.ID, LineNo: 4, Kind: domain.LineUnknownReceipt})
	refused("create transaction", err)
	_, err = f.admin.Exec(context.Background(), `update bank_statement_line set status = 'unmatched' where statement_id = $1 and line_no = 1`, f.statement.ID)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != ClosedSQLState {
		t.Errorf("direct update of a closed period's line: err = %v; want SQLSTATE %s", err, ClosedSQLState)
	}
	_, err = f.admin.Exec(context.Background(), `update reconciliation_match set status = 'unmatched' where id = $1`, matches[0].ID)
	if !errors.As(err, &pg) || pg.Code != ClosedSQLState {
		t.Errorf("direct update of a closed period's match: err = %v; want SQLSTATE %s", err, ClosedSQLState)
	}
	if got := lineStatuses(t, f.reconFixture); got[1] != "matched" || got[4] != "excluded" {
		t.Errorf("line statuses = %v; want line 1 matched and line 4 excluded", got)
	}
}

func TestReopenReconciliation_NeedsAnotherPersonAReasonAndNoLaterClosedPeriod(t *testing.T) {
	f := newBankFixture(t)
	rec := f.readyToClose(t)
	if _, err := f.s.ReopenReconciliation(ctxAs(1, f.le, reopenerUser), f.rec.ID, rec.Version, "not closed yet"); problemCode(t, err) != CodeReconNotClosed {
		t.Fatalf("reopen of an open reconciliation: err = %v; want %s", err, CodeReconNotClosed)
	}
	closed, err := f.s.CloseReconciliation(f.ctx, f.rec.ID, rec.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ReopenReconciliation(f.ctx, f.rec.ID, closed.Version, "late invoice"); problemCode(t, err) != CodeReopenFourEyes {
		t.Fatalf("reopen by the closer: err = %v; want %s", err, CodeReopenFourEyes)
	}

	later, err := f.s.ImportStatement(f.ctx, importInput(t, f.account.ID, camtOn("STMT-LATER", "2026-11-22", "5.00")))
	if err != nil {
		t.Fatal(err)
	}
	laterRec, err := f.s.CreateReconciliation(f.ctx, NewReconciliation{StatementID: later.Statement.ID, BookBalance: decimal.RequireFromString("1000.00")}, ids.New())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ExcludeLine(f.ctx, laterRec.ID, 1, "bank charge reversed"); err != nil {
		t.Fatal(err)
	}
	laterRec, err = f.s.GetReconciliation(f.ctx, laterRec.ID)
	if err != nil {
		t.Fatal(err)
	}
	laterClosed, err := f.s.CloseReconciliation(f.ctx, laterRec.ID, laterRec.Version)
	if err != nil {
		t.Fatal(err)
	}
	other := ctxAs(1, f.le, reopenerUser)
	if _, err := f.s.ReopenReconciliation(other, f.rec.ID, closed.Version, "late invoice"); problemCode(t, err) != CodeReopenLaterClosed {
		t.Fatalf("reopen under a closed later period: err = %v; want %s", err, CodeReopenLaterClosed)
	}
	if _, err := f.s.ReopenReconciliation(other, laterRec.ID, laterClosed.Version, "wrong exclusion"); err != nil {
		t.Fatalf("reopen of the latest period: %v", err)
	}
	reopened, err := f.s.ReopenReconciliation(other, f.rec.ID, closed.Version, "late invoice")
	if err != nil || reopened.State != StateReopened || reopened.ReopenedBy == nil || *reopened.ReopenedBy != reopenerUser || reopened.ReopenReason == nil || *reopened.ReopenReason != "late invoice" {
		t.Fatalf("reopen = %+v, %v; want reopened by %s with the reason", reopened, err, reopenerUser)
	}
	if n := count(t, f.admin, `select count(*) from bank_reconciliation_event where reconciliation_id = $1 and event = 'reopened' and actor = $2 and reason = 'late invoice'`, f.rec.ID, reopenerUser); n != 1 {
		t.Fatalf("reopened audit events = %d; want 1", n)
	}
	if _, err := f.s.ExcludeLine(f.ctx, f.rec.ID, 4, "changed after reopen"); err != nil {
		t.Fatalf("a reopened reconciliation must accept changes: %v", err)
	}
	recloseable, err := f.s.CloseReconciliation(f.ctx, f.rec.ID, f.reload(t).Version)
	if err != nil || recloseable.State != StateClosed {
		t.Fatalf("close after reopen = %+v, %v; want closed", recloseable, err)
	}
}

func TestCreateLineTransaction_IsOncePerLineAndMatchesTheLineWhenFinished(t *testing.T) {
	f := newBankFixture(t)
	f.matchLines123(t)
	rec, err := f.s.SetReconciliationBalances(f.ctx, f.rec.ID, f.reload(t).Version, decimal.RequireFromString("1450.00"), decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	begin := func(kind string) (LineTransaction, error) {
		return f.s.BeginLineTransaction(f.ctx, NewLineTransaction{ReconciliationID: f.rec.ID, LineNo: 4, Kind: kind})
	}
	if _, err := begin(domain.LineBankFee); problemCode(t, err) != CodeLineKind {
		t.Fatalf("a bank fee on money coming in: err = %v; want %s", err, CodeLineKind)
	}
	first, err := begin(domain.LineUnknownReceipt)
	if err != nil || first.Status != TransactionPending || first.Replayed || !decimal.RequireFromString(first.Amount).Equal(decimal.NewFromInt(7)) {
		t.Fatalf("begin = %+v, %v; want a pending transaction of 7", first, err)
	}
	again, err := begin(domain.LineUnknownReceipt)
	if err != nil || again.ID != first.ID || again.Status != TransactionPending {
		t.Fatalf("begin again = %+v, %v; want the same pending transaction", again, err)
	}
	if _, err := begin(domain.LineInterest); problemCode(t, err) != CodeLinePosting {
		t.Fatalf("another kind on the same line: err = %v; want %s", err, CodeLinePosting)
	}
	if _, err := f.s.ExcludeLine(f.ctx, f.rec.ID, 4, "should not"); problemCode(t, err) != CodeLinePosting {
		t.Fatalf("exclude of a line being posted: err = %v; want %s", err, CodeLinePosting)
	}
	if _, err := f.s.CloseReconciliation(f.ctx, f.rec.ID, rec.Version); problemCode(t, err) != CodePostingPending {
		t.Fatalf("close with a pending transaction: err = %v; want %s", err, CodePostingPending)
	}
	if err := f.s.RefuseLineTransaction(f.ctx, first.ID, "LED-0001"); err != nil {
		t.Fatal(err)
	}
	if n := count(t, f.admin, `select count(*) from bank_line_transaction where id = $1 and status = 'refused' and refusal_code = 'LED-0001'`, first.ID); n != 1 {
		t.Fatal("a refused transaction is not stored as refused")
	}
	if got := lineStatuses(t, f.reconFixture); got[4] != "unmatched" {
		t.Fatalf("line 4 is %s after a refusal; want unmatched", got[4])
	}
	retry, err := begin(domain.LineUnknownReceipt)
	if err != nil || retry.ID != first.ID || retry.Status != TransactionPending {
		t.Fatalf("begin after a refusal = %+v, %v; want the same transaction pending again", retry, err)
	}
	done, err := f.s.FinishLineTransaction(f.ctx, first.ID, "ENTRY-A1")
	if err != nil || done.Status != TransactionPosted || done.Replayed || done.LedgerEntryID == nil || *done.LedgerEntryID != "entry-a1" || done.MatchID == nil {
		t.Fatalf("finish = %+v, %v; want posted with the entry and a match", done, err)
	}
	replay, err := f.s.FinishLineTransaction(f.ctx, first.ID, "ENTRY-A1")
	if err != nil || !replay.Replayed {
		t.Fatalf("finish again = %+v, %v; want a replay", replay, err)
	}
	replayed, err := begin(domain.LineUnknownReceipt)
	if err != nil || !replayed.Replayed || replayed.ID != first.ID {
		t.Fatalf("begin after finish = %+v, %v; want the posted transaction as a replay", replayed, err)
	}
	after := f.reload(t)
	if after.BookBalance != "1457.00" || after.Difference != "0.00" || after.Summary.UnmatchedCount != 0 || after.Summary.MatchedCount != 4 {
		t.Fatalf("reconciliation = %+v; want book 1457.00 moved once, a zero difference and 4 matched lines", after)
	}
	if got := lineStatuses(t, f.reconFixture); got[4] != "matched" {
		t.Fatalf("line 4 is %s; want matched", got[4])
	}
	if n := count(t, f.admin, `select count(*) from reconciliation_match_item where item_id = 'entry-a1' and active`); n != 1 {
		t.Fatalf("active match items for the entry = %d; want 1", n)
	}
	if _, err := f.s.CloseReconciliation(f.ctx, f.rec.ID, after.Version); err != nil {
		t.Fatalf("close after the transaction: %v", err)
	}
}

func TestCreateLineTransaction_RefusesACurrencyOtherThanTheAccounts(t *testing.T) {
	f := newBankFixture(t)
	if _, err := f.admin.Exec(context.Background(), `alter table bank_statement_line disable trigger user`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.admin.Exec(context.Background(), `update bank_statement_line set currency = 'USD' where statement_id = $1 and line_no = 4`, f.statement.ID); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.BeginLineTransaction(f.ctx, NewLineTransaction{ReconciliationID: f.rec.ID, LineNo: 4, Kind: domain.LineUnknownReceipt})
	if got := problemCode(t, err); got != CodeLineKind {
		t.Fatalf("a USD line on a EUR account: code = %s; want %s", got, CodeLineKind)
	}
	if n := count(t, f.admin, `select count(*) from bank_line_transaction`); n != 0 {
		t.Fatalf("transactions stored = %d; want none", n)
	}
}

func TestBankReconciliation_AnotherLegalEntityOrTenantCannotSeeOrCloseIt(t *testing.T) {
	f := newBankFixture(t)
	rec := f.readyToClose(t)
	for name, ctx := range map[string]context.Context{
		"other legal entity": ctxAs(1, ids.New(), "user-x"),
		"other tenant":       ctxAs(2, f.le, "user-x"),
	} {
		if _, err := f.s.GetReconciliation(ctx, f.rec.ID); problemCode(t, err) != CodeReconNotFound {
			t.Errorf("%s get: err = %v; want %s", name, err, CodeReconNotFound)
		}
		if _, err := f.s.CloseReconciliation(ctx, f.rec.ID, rec.Version); problemCode(t, err) != CodeReconNotFound {
			t.Errorf("%s close: err = %v; want %s", name, err, CodeReconNotFound)
		}
		if _, err := f.s.ExcludeLine(ctx, f.rec.ID, 1, "no"); problemCode(t, err) != CodeReconNotFound {
			t.Errorf("%s exclude: err = %v; want %s", name, err, CodeReconNotFound)
		}
		if _, err := f.s.BeginLineTransaction(ctx, NewLineTransaction{ReconciliationID: f.rec.ID, LineNo: 4, Kind: domain.LineUnknownReceipt}); problemCode(t, err) != CodeReconNotFound {
			t.Errorf("%s create transaction: err = %v; want %s", name, err, CodeReconNotFound)
		}
		page, err := f.s.ListReconciliations(ctx, ReconciliationQuery{})
		if err != nil || len(page.Items) != 0 {
			t.Errorf("%s list = %+v, %v; want nothing", name, page, err)
		}
	}
	if got := f.reload(t); got.State != StateOpen {
		t.Fatalf("reconciliation is %s after foreign close attempts; want open", got.State)
	}
}

func TestListReconciliations_PagesByKeysetAndFilters(t *testing.T) {
	f := newBankFixture(t)
	var ids2 []string
	for i, date := range []string{"2026-11-22", "2026-12-22"} {
		st, err := f.s.ImportStatement(f.ctx, importInput(t, f.account.ID, camtOn(fmt.Sprintf("STMT-P%d", i), date, "5.00")))
		if err != nil {
			t.Fatal(err)
		}
		rec, err := f.s.CreateReconciliation(f.ctx, NewReconciliation{StatementID: st.Statement.ID, BookBalance: decimal.RequireFromString("1005.00")}, ids.New())
		if err != nil {
			t.Fatal(err)
		}
		ids2 = append(ids2, rec.ID)
	}
	want := []string{ids2[1], ids2[0], f.rec.ID}
	var got []string
	cursor := ""
	for range 5 {
		page, err := f.s.ListReconciliations(f.ctx, ReconciliationQuery{BankAccountID: f.account.ID, Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page.Items {
			got = append(got, r.ID)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("paged ids = %v; want newest period first %v", got, want)
	}
	page, err := f.s.ListReconciliations(f.ctx, ReconciliationQuery{State: StateClosed})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("closed filter = %+v, %v; want none", page, err)
	}
	page, err = f.s.ListReconciliations(f.ctx, ReconciliationQuery{PeriodFrom: "2026-11-01", PeriodTo: "2026-11-30"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != ids2[0] {
		t.Fatalf("period filter = %+v, %v; want the November reconciliation", page, err)
	}
	if _, err := f.s.ListReconciliations(f.ctx, ReconciliationQuery{Cursor: "garbage"}); problemCode(t, err) != "COM-0001" {
		t.Fatalf("bad cursor: err = %v; want COM-0001", err)
	}
}
