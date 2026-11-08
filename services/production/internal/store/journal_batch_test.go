package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

// failOn installs a trigger that raises on the given statement of table when
// cond holds, so one write in the middle of a batch fails. It is dropped on cleanup.
func failOn(t *testing.T, pool *pgxpool.Pool, table, event, cond string) {
	t.Helper()
	ctx := context.Background()
	fn := "fail_" + table
	if _, err := pool.Exec(ctx, fmt.Sprintf(`create function %s() returns trigger language plpgsql as $$
		begin if %s then raise exception 'batch test: refused %s' using errcode = '23514'; end if; return new; end $$`, fn, cond, table)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`create trigger %s before %s on %s for each row execute function %s()`, fn, event, table, fn)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dropFail(t, pool, table) })
}

func dropFail(t *testing.T, pool *pgxpool.Pool, table string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), fmt.Sprintf(`drop trigger if exists fail_%s on %s; drop function if exists fail_%s()`, table, table, table)); err != nil {
		t.Fatal(err)
	}
}

func countRows(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A failure of one line in the middle of the batched line insert fails the whole
// draft: no journal, no lines and no journal number are left behind.
func TestJournals_BatchedLineInsertFailureRollsBack(t *testing.T) {
	pool, s := migrated(t)
	st := startedOrder(t, s, tenantCtx(testkit.TenantID(48)), flatOrder(t, "key-batch-insert", 9))
	failOn(t, pool, "production_journal_line", "insert", "new.line_seq = 5")
	_, err := st.journal(NewJournal{Type: domain.JournalPickingList})
	if err == nil || !strings.Contains(err.Error(), "batch test: refused") {
		t.Fatalf("want the line 5 failure, got %v", err)
	}
	if n := countRows(t, pool, `select count(*) from production_journal where order_id = $1`, st.orderID); n != 0 {
		t.Fatalf("%d journals left after the failed create", n)
	}
	if n := countRows(t, pool, `select count(*) from production_journal_line`); n != 0 {
		t.Fatalf("%d journal lines left after the failed create", n)
	}
	dropFail(t, pool, "production_journal_line")
	if j := st.mustJournal(NewJournal{Type: domain.JournalPickingList}); j.JournalNumber != "PJ-000001" || len(j.Lines) != 9 {
		t.Fatalf("retry made %s with %d lines, want PJ-000001 with 9", j.JournalNumber, len(j.Lines))
	}
}

// A failure of one BOM line's consumption in the middle of the post batch rolls
// back the whole post: the WIP postings (an earlier batch), the line values and
// every other line's consumption. A retry with the cause gone posts in full.
func TestJournals_BatchedPostFailureRollsBack(t *testing.T) {
	pool, s := migrated(t)
	st := startedOrder(t, s, tenantCtx(testkit.TenantID(49)), flatOrder(t, "key-batch-post", 9))
	j := st.mustJournal(NewJournal{Type: domain.JournalPickingList})
	failOn(t, pool, "production_bom_line", "update", "new.line_number = '50' and new.qty_consumed <> old.qty_consumed")
	_, err := st.post(j.ID, nil)
	if err == nil || !strings.Contains(err.Error(), "batch test: refused") {
		t.Fatalf("want the line 50 failure, got %v", err)
	}
	if n := countRows(t, pool, `select count(*) from production_wip_posting where journal_id = $1`, j.ID); n != 0 {
		t.Fatalf("%d WIP postings left after the failed post", n)
	}
	if n := countRows(t, pool, `select count(*) from production_bom_line where order_id = $1 and qty_consumed <> 0`, st.orderID); n != 0 {
		t.Fatalf("%d BOM lines kept consumption after the failed post", n)
	}
	if n := countRows(t, pool, `select count(*) from production_journal_line where journal_id = $1 and amount <> 0`, j.ID); n != 0 {
		t.Fatalf("%d journal lines kept a value after the failed post", n)
	}
	if got, err := s.GetJournal(st.ctx, j.ID); err != nil || got.Posted {
		t.Fatalf("journal posted=%v after the failed post (%v)", got.Posted, err)
	}
	dropFail(t, pool, "production_bom_line")
	if posted := st.mustPost(j.ID); !posted.Posted {
		t.Fatal("retry did not post")
	}
	for no, q := range consumedByLine(t, pool, st.orderID) {
		if !d(q).Equal(d("100")) {
			t.Fatalf("line %s consumed %s after the retry, want 100", no, q)
		}
	}
}

// Two lines of one journal on the same BOM line both land on its qty_consumed:
// the batched updates accumulate, and the over-consumption check sees the sum.
func TestJournals_BatchedDuplicateBOMLinesAccumulate(t *testing.T) {
	pool, s := migrated(t)
	st := startedOrder(t, s, tenantCtx(testkit.TenantID(50)), flatOrder(t, "key-batch-dup", 3))
	j := st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "20", Qty: d("30")}, {LineNo: "10", Qty: d("5")}}})
	// The API refuses a line listed twice; a second line on BOM line 20 is written directly.
	if _, err := pool.Exec(context.Background(), `insert into production_journal_line (tenant_id, journal_id, line_seq, kind, bom_line_number, item_id, qty, unit_cost)
		select tenant_id, journal_id, 3, kind, bom_line_number, item_id, 45, unit_cost from production_journal_line where journal_id = $1 and line_seq = 1`, j.ID); err != nil {
		t.Fatal(err)
	}
	posted := st.mustPost(j.ID)
	if len(posted.Lines) != 3 {
		t.Fatalf("posted %d lines, want 3", len(posted.Lines))
	}
	got := consumedByLine(t, pool, st.orderID)
	for no, want := range map[string]string{"10": "5", "20": "75", "30": "0"} {
		if !d(got[no]).Equal(d(want)) {
			t.Fatalf("line %s consumed %s, want %s", no, got[no], want)
		}
	}
	if net, n := wipOn(t, pool, j.ID, "1450"); !d(net).Equal(d("200")) || n != 6 {
		t.Fatalf("WIP 1450 net %s over %d postings, want 200 (80 x 2.50) over 6", net, n)
	}

	// Two lines that are each within the 10 % allowance but together over it are refused.
	j2 := st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: []IssueInput{{LineNo: "30", Qty: d("60")}}})
	if _, err := pool.Exec(context.Background(), `insert into production_journal_line (tenant_id, journal_id, line_seq, kind, bom_line_number, item_id, qty, unit_cost)
		select tenant_id, journal_id, 2, kind, bom_line_number, item_id, 60, unit_cost from production_journal_line where journal_id = $1 and line_seq = 1`, j2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.post(j2.ID, nil); err == nil {
		t.Fatal("120 on a 100 line posted without the over-consumption duty")
	}
	if got := consumedByLine(t, pool, st.orderID)["30"]; !d(got).IsZero() {
		t.Fatalf("line 30 consumed %s after the refused post, want 0", got)
	}
}

// Concurrent posts of different journals on one order serialize on the order
// row (BR-21.4, locked before any batched write): none deadlocks or loses an update.
func TestJournals_ConcurrentPostsOnOneOrder(t *testing.T) {
	pool, s := migrated(t)
	const lines, journals = 40, 4
	st := startedOrder(t, s, tenantCtx(testkit.TenantID(51)), flatOrder(t, "key-batch-concurrent", lines))
	var ids []string
	for k := 0; k < journals; k++ {
		var issues []IssueInput
		// Each journal names every line, half of them in reverse order, so a writer that did
		// not lock the order first would take the BOM rows in conflicting orders.
		for i := 0; i < lines; i++ {
			no := (i + 1) * 10
			if k%2 == 1 {
				no = (lines - i) * 10
			}
			issues = append(issues, IssueInput{LineNo: fmt.Sprint(no), Qty: d("2")})
		}
		ids = append(ids, st.mustJournal(NewJournal{Type: domain.JournalPickingList, Issues: issues}).ID)
	}
	var wg sync.WaitGroup
	errs := make([]error, journals)
	for k, id := range ids {
		wg.Add(1)
		go func(k int, id string) {
			defer wg.Done()
			_, _, errs[k] = s.PostJournal(st.ctx, PostRequest{JournalID: id, OverConsumptionPct: decimal.NewFromInt(10), AutoStart: true,
				Actor: "supervisor-1", TraceID: "trace-c", IdempotencyKey: id + "-post"})
		}(k, id)
	}
	wg.Wait()
	for k, err := range errs {
		if err != nil {
			t.Fatalf("post %d: %v", k, err)
		}
	}
	for no, q := range consumedByLine(t, pool, st.orderID) {
		if !d(q).Equal(d(fmt.Sprint(2 * journals))) {
			t.Fatalf("line %s consumed %s, want %d", no, q, 2*journals)
		}
	}
}
