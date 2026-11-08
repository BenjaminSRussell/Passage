package store

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

// journalBudget is the PRD 4.21 performance target: posting a 200-line picking
// list has a p95 under 2 s (docs/prd/phase-4-manufacturing.md, non-functional table).
const journalBudget = 2 * time.Second

// envPerfTest turns on the p95 assertion. Without it the test still posts the
// 200-line journals and checks what they booked, and logs the timings, but no
// wall-clock budget is asserted, since `go test ./...` runs packages in parallel.
const envPerfTest = "PASSAGE_PERF_TEST"

// flatOrder is sampleOrder with n flat BOM lines 10, 20, ... of 1 per unit at 2.50.
func flatOrder(t *testing.T, key string, n int) NewOrder {
	t.Helper()
	in := sampleOrder(t, key, ids.New())
	bom := make([]domain.BOMComponent, 0, n)
	for i := 0; i < n; i++ {
		bom = append(bom, domain.BOMComponent{LineNo: fmt.Sprint((i + 1) * 10), ItemID: ids.New(), QtyPer: d("1"), UnitCost: d("2.50")})
	}
	lines, err := domain.Explode(bom, in.Quantity)
	if err != nil {
		t.Fatal(err)
	}
	in.Lines = lines
	return in
}

// consumedByLine reads qty_consumed of every BOM line of the order.
func consumedByLine(t *testing.T, pool *pgxpool.Pool, orderID string) map[string]string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select line_number, qty_consumed::text from production_bom_line where order_id = $1`, orderID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var no, q string
		if err := rows.Scan(&no, &q); err != nil {
			t.Fatal(err)
		}
		out[no] = q
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// wipOn is Σ dr − Σ cr of the journal's WIP postings on account, and the journal's posting count.
func wipOn(t *testing.T, pool *pgxpool.Pool, journalID, account string) (string, int) {
	t.Helper()
	var net string
	var n int
	if err := pool.QueryRow(context.Background(), `select coalesce(sum(amount_dr - amount_cr) filter (where ledger_account = $2), 0)::text, count(*)
		from production_wip_posting where journal_id = $1`, journalID, account).Scan(&net, &n); err != nil {
		t.Fatal(err)
	}
	return net, n
}

// TestJournals_PostLatency200Lines posts picking lists of 200 issue lines, one
// per order, and checks that the batched writes booked every line. With
// PASSAGE_PERF_TEST set it also holds the p95 of the 20 posts under the PRD
// budget. The runs are sequential, so this measures the store path (round
// trips and the write set), not load.
func TestJournals_PostLatency200Lines(t *testing.T) {
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(47))
	const lineCount, runs = 200, 20
	var took []time.Duration
	for r := 0; r < runs; r++ {
		st := startedOrder(t, s, ctx, flatOrder(t, fmt.Sprintf("key-latency-%d", r), lineCount))
		j := st.mustJournal(NewJournal{Type: domain.JournalPickingList})
		if len(j.Lines) != lineCount {
			t.Fatalf("picking list has %d lines, want %d", len(j.Lines), lineCount)
		}
		start := time.Now()
		posted := st.mustPost(j.ID)
		took = append(took, time.Since(start))
		if !posted.Posted || len(posted.Lines) != lineCount {
			t.Fatalf("posted %v with %d lines", posted.Posted, len(posted.Lines))
		}
		if r > 0 {
			continue // the booked state is checked once; the other runs are for timing
		}
		for _, l := range posted.Lines {
			if !l.Amount.Equal(d("250")) {
				t.Fatalf("line %s valued %s, want 250 (100 x 2.50)", *l.LineNo, l.Amount)
			}
		}
		for no, q := range consumedByLine(t, pool, st.orderID) {
			if !d(q).Equal(d("100")) {
				t.Fatalf("line %s consumed %s, want 100", no, q)
			}
		}
		if net, n := wipOn(t, pool, j.ID, "1450"); !d(net).Equal(d("50000")) || n != 2*lineCount {
			t.Fatalf("WIP 1450 net %s over %d postings, want 50000 over %d", net, n, 2*lineCount)
		}
	}
	sort.Slice(took, func(a, b int) bool { return took[a] < took[b] })
	p95 := took[(len(took)*95+99)/100-1]
	t.Logf("post of %d lines: min %v median %v p95 %v max %v", lineCount, took[0], took[len(took)/2], p95, took[len(took)-1])
	if os.Getenv(envPerfTest) == "" {
		t.Logf("p95 budget %v not asserted; set %s=1 to assert it", journalBudget, envPerfTest)
		return
	}
	if p95 >= journalBudget {
		t.Fatalf("p95 %v is not under the %v budget", p95, journalBudget)
	}
}
