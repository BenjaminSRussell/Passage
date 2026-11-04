package store

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/migrations"
)

func postedEntry(ledger, entity string, seq int64, period, sourceType string, lines ...domain.SourceLine) domain.PostedEntry {
	return domain.PostedEntry{LedgerID: ledger, LegalEntityID: entity, EntryID: ids.New(), PostingSequence: seq, PostingDate: period + "-15",
		PeriodKey: period, Voucher: fmt.Sprintf("V%d", seq), SourceSystem: "ledger", SourceType: sourceType, Lines: lines}
}

func sl(n int, account, side, amount string, dims map[string]string) domain.SourceLine {
	return domain.SourceLine{LineNumber: n, Account: account, DebitCredit: side, AmountAcct: decimal.RequireFromString(amount),
		Dimensions: dims, SourceURI: fmt.Sprintf("passage://payables/invoices/%d", n), TraceID: "0123456789abcdef0123456789abcdef"}
}

func pair(amount string) []domain.SourceLine {
	return []domain.SourceLine{sl(1, "1000", "D", amount, nil), sl(2, "2000", "C", amount, nil)}
}

func applyFixture(t *testing.T) (*Postgres, context.Context, context.Context) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	all := scopedCtx(testkit.TenantID(1), "")
	if err := dbx.Migrate(all, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return New(app), all, scopedCtx(testkit.TenantID(2), "")
}

func mustApply(t *testing.T, st *Postgres, ctx context.Context, e domain.PostedEntry, want int) {
	t.Helper()
	if n, err := st.ApplyEntry(ctx, e); err != nil || n != want {
		t.Fatalf("entry %d: applied %d (want %d) err %v", e.PostingSequence, n, want, err)
	}
}

func queryText(t *testing.T, st *Postgres, ctx context.Context, query string, args ...any) string {
	t.Helper()
	var out string
	if err := dbx.WithTenantTx(ctx, st.pool, func(tx pgx.Tx) error { return tx.QueryRow(ctx, query, args...).Scan(&out) }); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestApplyEntry_BuildsReadModelAndStatementsTieOut applies entries in order and
// runs a trial balance over what the consumer wrote.
func TestApplyEntry_BuildsReadModelAndStatementsTieOut(t *testing.T) {
	st, all, _ := applyFixture(t)
	ledger, entity := ids.New(), ids.New()
	ctx := scopedCtx(testkit.TenantID(1), entity)
	entries := []domain.PostedEntry{
		postedEntry(ledger, entity, 1, "2026-01", "", sl(1, "1000", "D", "100.50", nil), sl(2, "4000", "C", "100.50", nil)),
		postedEntry(ledger, entity, 2, "2026-01", "", sl(1, "1000", "D", "9.50", nil), sl(2, "4000", "C", "9.50", nil)),
		postedEntry(ledger, entity, 3, "2026-02", "", sl(1, "1001", "D", "20", map[string]string{"DEPT": "D1"}), sl(2, "1000", "C", "20", nil)),
	}
	for _, e := range entries {
		mustApply(t, st, ctx, e, 1)
	}
	lag, err := st.Lag(ctx)
	if err != nil || len(lag) != 1 || lag[0].PostingSequence != 3 || lag[0].EntriesApplied != 3 || lag[0].EntriesPending != 0 {
		t.Fatalf("lag %+v err %v", lag, err)
	}
	if _, _, err := st.SaveDraft(all, storeFixtureDefinition("APPLY"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(all, "APPLY", "tester"); err != nil {
		t.Fatal(err)
	}
	run, err := st.CreateRun(ctx, RunSpec{Code: "APPLY", LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 2,
		Calendar: storeFixtureCalendar(), RequestedBy: "tester", IdempotencyKey: "k1", RequestHash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if run.HWMByLedger[ledger] != 3 {
		t.Fatalf("run hwm %v", run.HWMByLedger)
	}
	cells, err := st.ListCells(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"R001": "90", "R002": "20"} // balances carry: cash 110 - 20, receivables 20
	for _, c := range cells {
		if w, ok := want[c.Row]; ok && !c.Value.Equal(decimal.RequireFromString(w)) {
			t.Errorf("%s = %s, want %s", c.Row, c.Value, w)
		}
	}
	// Drill level 2 lists the consumer's lines of the January key, links them
	// to the Passage route of their passage:// source, and reconciles them.
	page, err := st.DrillLines(ctx, run.ID, "R001", "C01", LineQuery{Key: 0, Limit: 100})
	if err != nil || !page.Reconciliation.Reconciled() || len(page.Items) != 2 || page.Items[0].SourceURI != "/payables/invoices/1" ||
		!page.SumOfLines.Equal(decimal.RequireFromString("110")) {
		t.Fatalf("drill %+v err %v", page, err)
	}
}

// TestApplyEntry_OutOfOrderWaitsThenDrains: the ledger's entries reach the
// consumer through several partitions, so later entries arrive first. They wait
// without moving the mark (BR-15.6: the read model holds exactly 1..HWM) and
// are applied the moment the gap closes; replays change nothing.
func TestApplyEntry_OutOfOrderWaitsThenDrains(t *testing.T) {
	st, _, _ := applyFixture(t)
	ledger, entity := ids.New(), ids.New()
	ctx := scopedCtx(testkit.TenantID(1), entity)
	e1, e2, e3 := postedEntry(ledger, entity, 1, "2026-03", "", pair("10")...), postedEntry(ledger, entity, 2, "2026-03", "", pair("1")...),
		postedEntry(ledger, entity, 3, "2026-03", "", pair("5")...)
	mustApply(t, st, ctx, e3, 0)
	mustApply(t, st, ctx, e2, 0)
	mustApply(t, st, ctx, e3, 0) // a replay of a waiting entry
	lag, err := st.Lag(ctx)
	if err != nil || len(lag) != 1 || lag[0].PostingSequence != 0 || lag[0].EntriesPending != 2 || lag[0].EntriesApplied != 0 {
		t.Fatalf("waiting lag %+v err %v", lag, err)
	}
	if n := queryText(t, st, ctx, `select count(*)::text from reporting_balance`); n != "0" {
		t.Fatalf("entries beyond the mark reached the balances: %s rows", n)
	}
	mustApply(t, st, ctx, e1, 3)
	mustApply(t, st, ctx, e2, 0) // a replay of an applied entry
	lag, _ = st.Lag(ctx)
	if lag[0].PostingSequence != 3 || lag[0].EntriesApplied != 3 || lag[0].EntriesPending != 0 {
		t.Fatalf("drained lag %+v", lag)
	}
	if net := queryText(t, st, ctx, `select net::text from reporting_balance where account='1000' and period_no=3`); net != "16.000000" {
		t.Fatalf("net %s", net)
	}
	if n := queryText(t, st, ctx, `select count(*)::text from reporting_journal_line`); n != "6" {
		t.Fatalf("lines %s", n)
	}
}

// TestApplyEntry_ConcurrentShuffledDeliveryConverges: entries delivered in any
// order by concurrent consumers end with every entry applied exactly once.
func TestApplyEntry_ConcurrentShuffledDeliveryConverges(t *testing.T) {
	st, _, _ := applyFixture(t)
	ledger, entity := ids.New(), ids.New()
	ctx := scopedCtx(testkit.TenantID(1), entity)
	const n = 24
	var entries []domain.PostedEntry
	for i := int64(1); i <= n; i++ {
		entries = append(entries, postedEntry(ledger, entity, i, "2026-04", "", pair("1")...))
	}
	order := rand.New(rand.NewSource(7)).Perm(n)
	var wg sync.WaitGroup
	errs := make(chan error, 2*n)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := w; i < len(order); i += 4 {
				for range 2 { // every event is delivered twice
					if _, err := st.ApplyEntry(ctx, entries[order[i]]); err != nil {
						errs <- err
					}
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	lag, _ := st.Lag(ctx)
	if len(lag) != 1 || lag[0].PostingSequence != n || lag[0].EntriesApplied != n || lag[0].EntriesPending != 0 {
		t.Fatalf("lag %+v", lag)
	}
	if net := queryText(t, st, ctx, `select net::text from reporting_balance where account='1000'`); net != "24.000000" {
		t.Fatalf("net %s", net)
	}
}

// TestApplyEntry_ClosingAndItsReversal: a year-end closing posted by the close
// service fills closing_net; the year-end re-run reverses it (a ledger
// reversal, source type "reversal") and takes it back out.
func TestApplyEntry_ClosingAndItsReversal(t *testing.T) {
	st, _, _ := applyFixture(t)
	ledger, entity := ids.New(), ids.New()
	ctx := scopedCtx(testkit.TenantID(1), entity)
	closing := postedEntry(ledger, entity, 1, "2026-12", domain.YearEndClosingSource, sl(1, "4000", "D", "10", nil), sl(2, "3900", "C", "10", nil))
	closing.SourceSystem = domain.CloseSourceSystem
	mustApply(t, st, ctx, closing, 1)
	if got := queryText(t, st, ctx, `select closing_net::text from reporting_balance where account='4000'`); got != "10.000000" {
		t.Fatalf("closing_net %s", got)
	}
	reversal := postedEntry(ledger, entity, 2, "2026-12", "reversal", sl(1, "4000", "C", "10", nil), sl(2, "3900", "D", "10", nil))
	reversal.ReversalOfEntryID = closing.EntryID
	mustApply(t, st, ctx, reversal, 1)
	if got := queryText(t, st, ctx, `select net::text || '/' || closing_net::text from reporting_balance where account='4000'`); got != "0.000000/0.000000" {
		t.Fatalf("after reversal net/closing_net %s", got)
	}
	if got := queryText(t, st, ctx, `select bool_and(closing_entry)::text from reporting_journal_line where posting_sequence=2`); got != "true" {
		t.Fatalf("reversal lines closing %s", got)
	}
	// A reversal of an ordinary entry stays ordinary.
	plain := postedEntry(ledger, entity, 3, "2026-12", "", pair("4")...)
	mustApply(t, st, ctx, plain, 1)
	undo := postedEntry(ledger, entity, 4, "2026-12", "reversal", sl(1, "1000", "C", "4", nil), sl(2, "2000", "D", "4", nil))
	undo.ReversalOfEntryID = plain.EntryID
	mustApply(t, st, ctx, undo, 1)
	if got := queryText(t, st, ctx, `select count(*)::text from reporting_journal_line where closing_entry and posting_sequence > 2`); got != "0" {
		t.Fatalf("ordinary reversal marked closing: %s lines", got)
	}
}

func TestApplyEntry_Refusals(t *testing.T) {
	st, all, otherTenant := applyFixture(t)
	ledger, entity := ids.New(), ids.New()
	mustApply(t, st, all, postedEntry(ledger, entity, 1, "2026-12", "", pair("10")...), 1)
	if _, err := st.ApplyEntry(all, postedEntry(ledger, ids.New(), 2, "2026-12", "", pair("1")...)); problemCode(err) != CodeLedgerEntity {
		t.Fatalf("other entity: %v", err)
	}
	if _, err := st.ApplyEntry(all, postedEntry(ledger, "", 2, "2026-12", "", pair("1")...)); problemCode(err) != domain.CodeLineInvalid {
		t.Fatalf("no entity: %v", err)
	}
	unbalanced := postedEntry(ledger, entity, 2, "2026-12", "", sl(1, "1", "D", "1", nil))
	if _, err := st.ApplyEntry(all, unbalanced); err == nil {
		t.Fatal("unbalanced entry accepted")
	}
	noID := postedEntry(ledger, entity, 2, "2026-12", "", pair("1")...)
	noID.EntryID = ""
	if _, err := st.ApplyEntry(all, noID); problemCode(err) != domain.CodeLineInvalid {
		t.Fatalf("entry without an id: %v", err)
	}
	dup := postedEntry(ledger, entity, 2, "2026-12", "", pair("1")...)
	dup.EntryID = queryText(t, st, all, `select entry_id::text from reporting_entry_applied where posting_sequence=1`)
	if _, err := st.ApplyEntry(all, dup); problemCode(err) != domain.CodeLineInvalid {
		t.Fatalf("entry applied twice under two sequences: %v", err)
	}
	if lag, _ := st.Lag(all); len(lag) != 1 || lag[0].PostingSequence != 1 || lag[0].EntriesPending != 0 {
		t.Fatalf("refusals moved the mark: %+v", lag)
	}
	// Another tenant sees none of it, and its own ledger starts at 1.
	if lag, err := st.Lag(otherTenant); err != nil || len(lag) != 0 {
		t.Fatalf("other tenant lag %+v err %v", lag, err)
	}
	if n := countAs(t, st.pool, otherTenant, `select count(*) from reporting_entry_applied`); n != 0 {
		t.Fatalf("other tenant sees %d applied entries", n)
	}
}

// TestApplyEntry_EntityScope: the consumer runs scoped to the entry's legal
// entity; it cannot write another entity's entry nor a ledger another entity owns.
func TestApplyEntry_EntityScope(t *testing.T) {
	st, all, _ := applyFixture(t)
	ledger, entityA, entityB := ids.New(), ids.New(), ids.New()
	ctxA, ctxB := scopedCtx(testkit.TenantID(1), entityA), scopedCtx(testkit.TenantID(1), entityB)
	mustApply(t, st, ctxA, postedEntry(ledger, entityA, 1, "2026-01", "", pair("3")...), 1)
	mustApply(t, st, ctxA, postedEntry(ledger, entityA, 3, "2026-01", "", pair("3")...), 0)
	for table, want := range map[string]int{"reporting_entry_applied": 0, "reporting_entry_pending": 0, "reporting_hwm": 0} {
		if n := countAs(t, st.pool, ctxB, `select count(*) from `+table); n != want {
			t.Fatalf("entity B sees %d rows of %s", n, table)
		}
	}
	if n := countAs(t, st.pool, all, `select count(*) from reporting_entry_pending`); n != 1 {
		t.Fatalf("all-scope sees %d pending", n)
	}
	if _, err := st.ApplyEntry(ctxB, postedEntry(ledger, entityA, 2, "2026-01", "", pair("3")...)); problemCode(err) != CodeLedgerEntity {
		t.Fatalf("entity B wrote an entry of entity A: %v", err)
	}
	if _, err := st.ApplyEntry(ctxB, postedEntry(ledger, entityB, 2, "2026-01", "", pair("3")...)); problemCode(err) != CodeLedgerEntity {
		t.Fatalf("entity B wrote to the ledger of entity A: %v", err)
	}
	if lag, _ := st.Lag(ctxA); len(lag) != 1 || lag[0].PostingSequence != 1 || lag[0].EntriesPending != 1 {
		t.Fatalf("lag after refusals %+v", lag)
	}
}
