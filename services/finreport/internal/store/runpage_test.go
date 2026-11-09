package store

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

// The run list pages by keyset on (started at, id): every run appears exactly
// once, newest first, a run stored between two pages does not shift the next
// page, and the ledger, period and definition filters narrow the pages.
func TestListRunsPage_KeysetAndFilters(t *testing.T) {
	st, ctx := migrated(t)
	for _, code := range []string{"RUN-A", "RUN-B"} {
		def := domain.Definition{Code: code, Name: code, Kind: domain.KindIncomeStatement,
			Rows:    []domain.Row{{Code: "REV", Kind: domain.RowAccountRange, Label: "Revenue", AccountFrom: "4000", AccountTo: "4999", NormalBalance: "credit"}},
			Columns: []domain.Column{{Code: "C1", Label: "Period", PeriodExpr: "P"}}}
		if _, _, err := st.SaveDraft(ctx, def, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Publish(ctx, code, "tester"); err != nil {
			t.Fatal(err)
		}
	}
	ledgerA, ledgerB := ids.New(), ids.New()
	seedBalance(t, ctx, st.pool, ledgerA, 2026, 3, "4000", "acct", "-10", 1)
	seedBalance(t, ctx, st.pool, ledgerB, 2026, 2, "4000", "acct", "-20", 1)
	var order []string
	run := func(key, code, ledger string, no int) {
		t.Helper()
		r, err := st.CreateRun(ctx, RunSpec{Code: code, LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: no,
			Calendar: storeFixtureCalendar(), IdempotencyKey: key, RequestHash: key})
		if err != nil {
			t.Fatal(err)
		}
		order = append([]string{r.ID}, order...) // newest first
	}
	run("k1", "RUN-A", ledgerA, 3)
	run("k2", "RUN-B", ledgerA, 3)
	run("k3", "RUN-A", ledgerB, 2)
	run("k4", "RUN-A", ledgerA, 3)
	run("k5", "RUN-B", ledgerB, 2)

	var got []string
	var cursor *domain.AuditCursor
	pages := 0
	for {
		page, err := st.ListRunsPage(ctx, RunQuery{}, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, r := range page.Items {
			got = append(got, r.ID)
		}
		if page.Next == nil {
			break
		}
		if len(page.Items) != 2 {
			t.Fatalf("a page with a next cursor holds %d items, want 2", len(page.Items))
		}
		cursor = page.Next
	}
	if pages != 3 || len(got) != 5 {
		t.Fatalf("pages %d items %d, want 3 and 5", pages, len(got))
	}
	for i := range order {
		if got[i] != order[i] {
			t.Fatalf("run %d = %s, want %s (newest first)", i, got[i], order[i])
		}
	}

	// A run stored after the first page was read does not move the second page.
	first, _ := st.ListRunsPage(ctx, RunQuery{}, nil, 2)
	run("k6", "RUN-A", ledgerA, 3)
	second, err := st.ListRunsPage(ctx, RunQuery{}, first.Next, 2)
	if err != nil || len(second.Items) != 2 || second.Items[0].ID != got[2] || second.Items[1].ID != got[3] {
		t.Fatalf("second page after a new run = %+v, %v", second.Items, err)
	}

	count := func(q RunQuery) int {
		p, err := st.ListRunsPage(ctx, q, nil, 200)
		if err != nil {
			t.Fatal(err)
		}
		return len(p.Items)
	}
	if n := count(RunQuery{Definition: "RUN-B"}); n != 2 {
		t.Fatalf("definition filter = %d, want 2", n)
	}
	if n := count(RunQuery{LedgerID: ledgerB}); n != 2 {
		t.Fatalf("ledger filter = %d, want 2", n)
	}
	if n := count(RunQuery{PeriodYear: 2026, PeriodNo: 3}); n != 4 {
		t.Fatalf("period filter = %d, want 4", n)
	}
	if n := count(RunQuery{Definition: "RUN-A", LedgerID: ledgerA, PeriodYear: 2026, PeriodNo: 3}); n != 3 {
		t.Fatalf("combined filter = %d, want 3", n)
	}
	if n := count(RunQuery{PeriodYear: 2025, PeriodNo: 1}); n != 0 {
		t.Fatalf("other period = %d, want 0", n)
	}

	// Runs that started at the same instant are ordered by id, descending, and
	// a page boundary between them neither skips nor repeats one.
	if _, err := st.pool.Exec(ctx, `update statement_run set started_at = '2026-03-01T10:00:00.123456Z'`); err != nil {
		t.Fatal(err)
	}
	var tied []string
	cursor = nil
	for {
		page, err := st.ListRunsPage(ctx, RunQuery{}, cursor, 4)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page.Items {
			tied = append(tied, r.ID)
		}
		if page.Next == nil {
			break
		}
		cursor = page.Next
	}
	if len(tied) != 6 {
		t.Fatalf("tied runs listed %d times, want 6: %v", len(tied), tied)
	}
	for i := 1; i < len(tied); i++ {
		if tied[i-1] <= tied[i] {
			t.Fatalf("tied runs not in descending id order: %v", tied)
		}
	}
}
