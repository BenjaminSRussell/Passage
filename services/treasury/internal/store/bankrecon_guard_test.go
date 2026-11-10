package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
)

func (f *bankFixture) asApp(t *testing.T, sql string, args ...any) error {
	t.Helper()
	return dbx.WithTenantTx(f.ctx, f.s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), sql, args...)
		return err
	})
}

func wantGuard(t *testing.T, name string, err error) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != ClosedSQLState {
		t.Errorf("%s as the application role: err = %v; want SQLSTATE %s", name, err, ClosedSQLState)
	}
}

func TestClosedReconciliation_TheApplicationRoleCannotChangeItDirectly(t *testing.T) {
	f := newBankFixture(t)
	matches := f.matchLines123(t)
	if _, err := f.s.ExcludeLine(f.ctx, f.rec.ID, 4, "bank rounding adjustment"); err != nil {
		t.Fatal(err)
	}
	rec, err := f.s.SetReconciliationBalances(f.ctx, f.rec.ID, f.reload(t).Version, decimal.RequireFromString("1450.00"), decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CloseReconciliation(f.closer, f.rec.ID, rec.Version); err != nil {
		t.Fatal(err)
	}
	wantGuard(t, "changing the book balance", f.asApp(t, `update bank_reconciliation set book_balance = 1 where id = $1`, f.rec.ID))
	wantGuard(t, "setting the state back to open", f.asApp(t, `update bank_reconciliation set state = 'open' where id = $1`, f.rec.ID))
	wantGuard(t, "reopening as the closer", f.asApp(t, `update bank_reconciliation set state = 'reopened', reopened_by = closed_by, reopen_reason = 'x' where id = $1`, f.rec.ID))
	wantGuard(t, "reopening without a reason", f.asApp(t, `update bank_reconciliation set state = 'reopened', reopened_by = 'someone-else' where id = $1`, f.rec.ID))
	wantGuard(t, "inserting a statement line", f.asApp(t, `insert into bank_statement_line (tenant_id, statement_id, line_no, legal_entity_id, bank_account_id, booking_date, value_date, amount, currency, raw_fragment)
		select tenant_id, statement_id, 99, legal_entity_id, bank_account_id, booking_date, value_date, amount, currency, raw_fragment from bank_statement_line where statement_id = $1 and line_no = 1`, f.statement.ID))
	wantGuard(t, "deactivating a match item", f.asApp(t, `update reconciliation_match_item set active = false where match_id = $1`, matches[0].ID))
	wantGuard(t, "inserting a match item", f.asApp(t, `insert into reconciliation_match_item (tenant_id, match_id, legal_entity_id, item_type, item_id, currency, amount)
		select tenant_id, match_id, legal_entity_id, 'ledger', 'extra', currency, amount from reconciliation_match_item where match_id = $1`, matches[0].ID))
	if got := f.reload(t); got.State != StateClosed || got.BookBalance != "1450.00" {
		t.Fatalf("reconciliation = %+v after direct attempts; want it closed and unchanged", got)
	}
	if err := f.asApp(t, `update bank_reconciliation set state = 'reopened', reopened_by = 'someone-else', reopen_reason = 'late invoice' where id = $1`, f.rec.ID); err != nil {
		t.Fatalf("reopen by another person with a reason: %v", err)
	}
}

func TestCloseReconciliation_TheBalanceStaterCannotCloseEvenDirectly(t *testing.T) {
	f := newBankFixture(t)
	f.readyToClose(t)
	wantGuard(t, "closing as the person who stated the balance", f.asApp(t, `update bank_reconciliation set state = 'closed', closed_by = book_balance_stated_by, closed_at = now() where id = $1`, f.rec.ID))
}

func TestSetReconciliationBalances_IsAuditedAndNamesTheStatedSource(t *testing.T) {
	f := newBankFixture(t)
	if f.rec.BookBalanceSource != "stated" || f.rec.BookBalanceStatedBy != "user-test" {
		t.Fatalf("created = %+v; want a stated book balance by the creator", f.rec)
	}
	rec, err := f.s.SetReconciliationBalances(ctxAs(1, f.le, "user-second"), f.rec.ID, f.rec.Version, decimal.RequireFromString("1450.00"), decimal.Zero)
	if err != nil || rec.BookBalanceStatedBy != "user-second" {
		t.Fatalf("set balances = %+v, %v; want the second person recorded as stater", rec, err)
	}
	if n := count(t, f.admin, `select count(*) from bank_reconciliation_event where reconciliation_id = $1 and event = 'balances_set' and actor = 'user-second'
		and detail->>'oldBookBalance' = '1457' and detail->>'newBookBalance' = '1450' and detail->>'previouslyStatedBy' = 'user-test'`, f.rec.ID); n != 1 {
		t.Fatalf("balances_set events with old and new values = %d; want 1", n)
	}
	if _, err := f.s.CloseReconciliation(ctxAs(1, f.le, "user-second"), f.rec.ID, rec.Version); problemCode(t, err) != CodeBalanceStater {
		t.Fatalf("close by the stater: err = %v; want %s", err, CodeBalanceStater)
	}
}

func TestUnmatch_RefusesTheMatchCreatedFromALineTransaction(t *testing.T) {
	f := newBankFixture(t)
	tx, err := f.s.BeginLineTransaction(f.ctx, NewLineTransaction{ReconciliationID: f.rec.ID, LineNo: 4, Kind: domain.LineUnknownReceipt})
	if err != nil {
		t.Fatal(err)
	}
	done, err := f.s.FinishLineTransaction(f.ctx, tx.ID, "entry-z9")
	if err != nil || done.MatchID == nil {
		t.Fatalf("finish = %+v, %v", done, err)
	}
	match, err := f.s.GetMatch(f.ctx, *done.MatchID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Unmatch(f.ctx, match.ID, match.Version, "mistake"); problemCode(t, err) != CodeMatchFromTransaction {
		t.Fatalf("unmatch of a match made from a transaction: err = %v; want %s", err, CodeMatchFromTransaction)
	}
	if got := lineStatuses(t, f.reconFixture); got[4] != "matched" {
		t.Fatalf("line 4 is %s; want it still matched", got[4])
	}
}

func TestLineTransaction_AnUnansweredAttemptThenHeldNeedsAPersonAndCanBeAbandoned(t *testing.T) {
	f := newBankFixture(t)
	f.matchLines123(t)
	rec, err := f.s.SetReconciliationBalances(f.ctx, f.rec.ID, f.reload(t).Version, decimal.RequireFromString("1450.00"), decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	begin := func() (LineTransaction, error) {
		return f.s.BeginLineTransaction(f.ctx, NewLineTransaction{ReconciliationID: f.rec.ID, LineNo: 4, Kind: domain.LineUnknownReceipt})
	}
	first, err := begin()
	if err != nil || first.AttemptUnanswered {
		t.Fatalf("first begin = %+v, %v; want a fresh attempt", first, err)
	}
	if err := f.s.MarkLineAttempt(f.ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	again, err := begin()
	if err != nil || !again.AttemptUnanswered {
		t.Fatalf("begin after an unanswered attempt = %+v, %v; want the unanswered flag", again, err)
	}
	if err := f.s.HoldLineTransaction(f.ctx, first.ID, "LEDGER_PERIOD_CLOSED"); err != nil {
		t.Fatal(err)
	}
	if _, err := begin(); problemCode(t, err) != CodeLineConflict {
		t.Fatalf("begin of a held line: err = %v; want %s", err, CodeLineConflict)
	}
	if _, err := f.s.ExcludeLine(f.ctx, f.rec.ID, 4, "should not"); problemCode(t, err) != CodeLinePosting {
		t.Fatalf("exclude of a held line: err = %v; want %s", err, CodeLinePosting)
	}
	_, err = f.s.CloseReconciliation(f.closer, f.rec.ID, rec.Version)
	var blocked *httpx.Problem
	if !errors.As(err, &blocked) || blocked.Code != CodePostingPending || blocked.Errors["pendingLines"] != "4" {
		t.Fatalf("close with a held line: err = %v; want %s naming line 4", err, CodePostingPending)
	}
	if _, err := f.s.AbandonLineTransaction(f.ctx, f.rec.ID, 4, "my own"); problemCode(t, err) != CodeAbandonOwn {
		t.Fatalf("abandon by the person who started it: err = %v; want %s", err, CodeAbandonOwn)
	}
	abandoned, err := f.s.AbandonLineTransaction(f.closer, f.rec.ID, 4, "the ledger will never accept it")
	if err != nil || abandoned.Status != TransactionAbandon {
		t.Fatalf("abandon = %+v, %v; want abandoned", abandoned, err)
	}
	if n := count(t, f.admin, `select count(*) from bank_reconciliation_event where reconciliation_id = $1 and event = 'line_abandoned' and reason = 'the ledger will never accept it'`, f.rec.ID); n != 1 {
		t.Fatalf("line_abandoned events = %d; want 1", n)
	}
	if _, err := f.s.FinishLineTransaction(f.ctx, first.ID, "entry-late"); problemCode(t, err) != CodeLineConflict {
		t.Fatalf("finish after an abandon: err = %v; want %s", err, CodeLineConflict)
	}
	if _, err := f.s.AbandonLineTransaction(f.closer, f.rec.ID, 4, "again"); problemCode(t, err) != CodeNothingToAbandon {
		t.Fatalf("second abandon: err = %v; want %s", err, CodeNothingToAbandon)
	}
	if _, err := f.s.AbandonLineTransaction(f.ctx, f.rec.ID, 1, "no transaction"); problemCode(t, err) != CodeNothingToAbandon {
		t.Fatalf("abandon of a line without a transaction: err = %v; want %s", err, CodeNothingToAbandon)
	}
	if _, err := f.s.ExcludeLine(f.ctx, f.rec.ID, 4, "checked the ledger by hand"); err != nil {
		t.Fatalf("exclude after abandon: %v", err)
	}
}

// A transaction whose ledger call never started may be posting right now, so it is not abandoned (#560).
func TestLineTransaction_AFreshPendingTransactionCannotBeAbandoned(t *testing.T) {
	f := newBankFixture(t)
	f.matchLines123(t)
	first, err := f.s.BeginLineTransaction(f.ctx, NewLineTransaction{ReconciliationID: f.rec.ID, LineNo: 4, Kind: domain.LineUnknownReceipt})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.AbandonLineTransaction(f.closer, f.rec.ID, 4, "too early"); problemCode(t, err) != CodeLinePosting {
		t.Fatalf("abandon of a fresh pending transaction: err = %v; want %s", err, CodeLinePosting)
	}
	if err := f.s.MarkLineAttempt(f.ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.AbandonLineTransaction(f.closer, f.rec.ID, 4, "no answer"); err != nil {
		t.Fatalf("abandon after an unanswered attempt: %v", err)
	}
}

func TestCloseReconciliation_RacingAnIncludeOrUnmatchNeverLeavesAnUnmatchedLineClosed(t *testing.T) {
	for _, racer := range []string{"include", "unmatch"} {
		for round := range 6 {
			f := newBankFixture(t)
			matches := f.matchLines123(t)
			if _, err := f.s.ExcludeLine(f.ctx, f.rec.ID, 4, "bank rounding adjustment"); err != nil {
				t.Fatal(err)
			}
			rec, err := f.s.SetReconciliationBalances(f.ctx, f.rec.ID, f.reload(t).Version, decimal.RequireFromString("1450.00"), decimal.Zero)
			if err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_, _ = f.s.CloseReconciliation(f.closer, f.rec.ID, rec.Version)
			}()
			go func() {
				defer wg.Done()
				<-start
				if racer == "include" {
					_, _ = f.s.IncludeLine(f.ctx, f.rec.ID, 4)
					return
				}
				_, _ = f.s.Unmatch(f.ctx, matches[0].ID, matches[0].Version, "race")
			}()
			close(start)
			wg.Wait()
			got := f.reload(t)
			if got.State == StateClosed && (got.Summary.UnmatchedCount != 0 || got.Difference != "0.00") {
				t.Fatalf("%s round %d: closed reconciliation with %d unmatched lines and difference %s", racer, round, got.Summary.UnmatchedCount, got.Difference)
			}
		}
	}
}
