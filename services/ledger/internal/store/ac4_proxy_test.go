package store

// Local proxy for issue #11 Proof AC4 (5,000 journal lines/s sustained on the
// dev cluster with p99 < 200 ms). AC4 itself is a live test on the deployed
// stack (#213); this one drives the store against whatever Postgres the test
// URLs name, so a regression in the posting or balance-read path shows on a
// laptop before stage. The numbers depend on the hardware and on the pool size
// (pool_max_conns in PASSAGE_TEST_APP_DATABASE_URL), so the test only reports
// them unless PASSAGE_LEDGER_BENCH_MIN_LPS or PASSAGE_LEDGER_BENCH_MAX_P99_MS
// set a floor. Run it with scripts/bench-local.sh (issue #476).

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

const (
	// envBench enables the benchmark; it is skipped otherwise, also in CI.
	envBench          = "PASSAGE_LEDGER_BENCH"
	envBenchSeconds   = "PASSAGE_LEDGER_BENCH_SECONDS"
	envBenchWarmup    = "PASSAGE_LEDGER_BENCH_WARMUP_SECONDS"
	envBenchWorkers   = "PASSAGE_LEDGER_BENCH_WORKERS"
	envBenchLines     = "PASSAGE_LEDGER_BENCH_LINES"
	envBenchReaders   = "PASSAGE_LEDGER_BENCH_READERS"
	envBenchLedgers   = "PASSAGE_LEDGER_BENCH_LEDGERS"
	envBenchReadEvery = "PASSAGE_LEDGER_BENCH_READ_INTERVAL_MS"
	envBenchMinLPS    = "PASSAGE_LEDGER_BENCH_MIN_LPS"
	envBenchMaxP99    = "PASSAGE_LEDGER_BENCH_MAX_P99_MS"
	envBenchOut       = "PASSAGE_LEDGER_BENCH_OUT"

	// AC4 target from issue #11, printed next to the measured numbers.
	ac4TargetLPS   = 5000
	ac4TargetP99Ms = 200
)

var benchAccounts = []string{"1000", "1200", "2100", "4000", "6010"}

// benchEntry builds a balanced entry of n lines (n/2 debits, the rest credits
// splitting the same total) over random accounts, dimensions and periods.
func benchEntry(r *rand.Rand, ledgerID, key string, n int) PostInput {
	nd := max(1, n/2)
	nc := max(1, n-nd)
	dims := func() map[string]string {
		return map[string]string{"DEPT": ac1Depts[1+r.IntN(len(ac1Depts)-1)], "CC": ac1CCs[1+r.IntN(len(ac1CCs)-1)]}
	}
	lines := make([]domain.LineInput, 0, nd+nc)
	var total int64
	for range nd {
		c := int64(100 + r.IntN(999_900))
		total += c
		lines = append(lines, domain.LineInput{Account: benchAccounts[r.IntN(len(benchAccounts))], Side: domain.SideDebit,
			Amount: decimal.New(c, -2), Currency: "USD", Dimensions: dims()})
	}
	remaining := total
	for k := range nc {
		c := remaining
		if still := int64(nc - 1 - k); still > 0 {
			c = remaining / int64(nc-k)
		}
		remaining -= c
		lines = append(lines, domain.LineInput{Account: benchAccounts[r.IntN(len(benchAccounts))], Side: domain.SideCredit,
			Amount: decimal.New(c, -2), Currency: "USD", Dimensions: dims()})
	}
	hash, _ := domain.HashPayload(map[string]any{"bench": key})
	return PostInput{
		LedgerID: ledgerID, DocumentID: ids.New(), PostingDate: ac1Dates[r.IntN(len(ac1Dates))], Description: "AC4 proxy " + key,
		Source: ledgerevents.Source{System: "ap", Type: "vendor_invoice", ID: key, URI: "passage://ap/invoices/" + key},
		Lines:  lines, Key: key, Hash: hash, Actor: "test-user", TraceID: testTrace, Today: testToday,
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

type benchReport struct {
	Seconds         float64 `json:"seconds"`
	Workers         int     `json:"workers"`
	LinesPerEntry   int     `json:"linesPerEntry"`
	Readers         int     `json:"readers"`
	Ledgers         int     `json:"ledgers"`
	Entries         int     `json:"entries"`
	Lines           int64   `json:"lines"`
	LinesPerSec     float64 `json:"linesPerSec"`
	MinWindowLPS    float64 `json:"minOneSecondLinesPerSec"`
	PostP50Ms       float64 `json:"postP50Ms"`
	PostP95Ms       float64 `json:"postP95Ms"`
	PostP99Ms       float64 `json:"postP99Ms"`
	PostMaxMs       float64 `json:"postMaxMs"`
	Reads           int     `json:"reads"`
	ReadP50Ms       float64 `json:"readP50Ms"`
	ReadP99Ms       float64 `json:"readP99Ms"`
	JournalLines    int64   `json:"journalLinesAtEnd"`
	TargetLPS       int     `json:"ac4TargetLinesPerSec"`
	TargetP99Ms     int     `json:"ac4TargetP99Ms"`
	PostgresVersion string  `json:"postgresVersion"`
	PoolMaxConns    int32   `json:"poolMaxConns"`
	CPUs            int     `json:"cpus"`
}

// TestPostgres_AC4Proxy_SustainedPostingAndBalanceReads posts balanced
// entries from W workers for the configured seconds while R readers read the
// trial balance, then reports lines/s (overall and the slowest one-second
// window), posting latency percentiles and read latency percentiles. Posts
// during the warm-up are not counted. Afterwards the trial balance must net
// to zero and a rebuild must find no differing projection rows.
func TestPostgres_AC4Proxy_SustainedPostingAndBalanceReads(t *testing.T) {
	if os.Getenv(envBench) == "" {
		t.Skipf("%s not set; skipping the AC4 proxy benchmark (run scripts/bench-local.sh)", envBench)
	}
	e, _ := setup(t)
	for num, a := range map[string]Account{
		"1000": {Name: "Cash", Type: "asset", Postable: true},
		"1200": {Name: "Prepayments", Type: "asset", Postable: true},
		"4000": {Name: "Revenue", Type: "revenue", Postable: true},
	} {
		if _, _, err := e.store.PutAccount(e.ctx, "STD", num, 0, a); err != nil {
			t.Fatalf("put account %s: %v", num, err)
		}
	}
	seconds := envInt(t, envBenchSeconds, 30)
	warmup := envInt(t, envBenchWarmup, 5)
	workers := envInt(t, envBenchWorkers, 8)
	linesPer := envInt(t, envBenchLines, 10)
	readers := envInt(t, envBenchReaders, 2)
	readEvery := time.Duration(envInt(t, envBenchReadEvery, 250)) * time.Millisecond
	// Posting serializes per ledger (gap-free posting sequence): workers are
	// spread over the ledgers round robin, and one ledger is the worst case.
	ledgers := []Ledger{e.ledger}
	for n := 1; n < envInt(t, envBenchLedgers, 4); n++ {
		l, err := e.store.CreateLedger(e.ctx, Ledger{LegalEntityID: e.le, Code: fmt.Sprintf("GL%d", n), Name: fmt.Sprintf("Ledger %d", n),
			ChartCode: "STD", Currency: "USD", DimensionAttributes: []string{"DEPT", "CC"}, FuturePostingDays: 366})
		if err != nil {
			t.Fatalf("create ledger %d: %v", n, err)
		}
		ledgers = append(ledgers, l)
	}

	var (
		measuring atomic.Bool
		stop      atomic.Bool
		seq       atomic.Int64
		mu        sync.Mutex
		postLat   []time.Duration
		readLat   []time.Duration
		lines     int64
		perSecond = make([]int64, seconds+1)
		firstErr  error
		start     time.Time
	)
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
		stop.Store(true)
	}
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(w), 476))
			var lat []time.Duration
			var n int64
			bucket := make([]int64, len(perSecond))
			for !stop.Load() {
				in := benchEntry(r, ledgers[w%len(ledgers)].ID, fmt.Sprintf("AC4P-%d", seq.Add(1)), linesPer)
				t0 := time.Now()
				if _, err := e.store.PostEntry(e.ctx, in); err != nil {
					fail(fmt.Errorf("post %s: %w", in.Key, err))
					return
				}
				if measuring.Load() {
					lat = append(lat, time.Since(t0))
					n += int64(len(in.Lines))
					if s := int(time.Since(start) / time.Second); s < len(bucket) {
						bucket[s] += int64(len(in.Lines))
					}
				}
			}
			mu.Lock()
			postLat, lines = append(postLat, lat...), lines+n
			for i, v := range bucket {
				perSecond[i] += v
			}
			mu.Unlock()
		}()
	}
	for rd := range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var lat []time.Duration
			for i := rd; !stop.Load(); i++ {
				t0 := time.Now()
				tb, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: ledgers[i%len(ledgers)].ID})
				if err != nil {
					fail(fmt.Errorf("trial balance: %w", err))
					return
				}
				if !tb.Balanced {
					fail(fmt.Errorf("trial balance unbalanced mid-load: debit %s credit %s", tb.Debit, tb.Credit))
					return
				}
				if measuring.Load() {
					lat = append(lat, time.Since(t0))
				}
				time.Sleep(readEvery)
			}
			mu.Lock()
			readLat = append(readLat, lat...)
			mu.Unlock()
		}()
	}
	time.Sleep(time.Duration(warmup) * time.Second)
	start = time.Now()
	measuring.Store(true)
	time.Sleep(time.Duration(seconds) * time.Second)
	measuring.Store(false)
	elapsed := time.Since(start)
	stop.Store(true)
	wg.Wait()
	if firstErr != nil {
		t.Fatal(firstErr)
	}

	minWindow := slices.Min(perSecond[:seconds])
	rep := benchReport{
		Seconds: elapsed.Seconds(), Workers: workers, LinesPerEntry: linesPer, Readers: readers, Ledgers: len(ledgers),
		Entries: len(postLat), Lines: lines, LinesPerSec: float64(lines) / elapsed.Seconds(), MinWindowLPS: float64(minWindow),
		PostP50Ms: ms(percentile(postLat, 0.50)), PostP95Ms: ms(percentile(postLat, 0.95)), PostP99Ms: ms(percentile(postLat, 0.99)),
		PostMaxMs: ms(percentile(postLat, 1)), Reads: len(readLat), ReadP50Ms: ms(percentile(readLat, 0.50)), ReadP99Ms: ms(percentile(readLat, 0.99)),
		TargetLPS: ac4TargetLPS, TargetP99Ms: ac4TargetP99Ms, PoolMaxConns: e.pool.Config().MaxConns, CPUs: runtime.NumCPU(),
	}
	_ = e.admin.QueryRow(context.Background(), `show server_version`).Scan(&rep.PostgresVersion)
	_ = e.admin.QueryRow(context.Background(), `select count(*) from journal_line`).Scan(&rep.JournalLines)

	// Correctness after the load: the projection nets to zero and equals the journal.
	for _, l := range ledgers {
		tb, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: l.ID})
		if err != nil || !tb.Balanced {
			t.Fatalf("ledger %s trial balance after load: balanced=%v err=%v", l.Code, tb.Balanced, err)
		}
		rb, err := e.store.RebuildBalances(e.ctx, l.ID)
		if err != nil || rb.RowsDiffering != 0 {
			t.Fatalf("ledger %s rebuild after load: %+v %v", l.Code, rb, err)
		}
	}

	t.Logf("AC4 proxy (local, not AC4): %d workers x %d-line entries over %d ledger(s), %d readers, %.1fs measured on PG %s, pool %d conns, %d CPUs",
		workers, linesPer, len(ledgers), readers, rep.Seconds, rep.PostgresVersion, rep.PoolMaxConns, rep.CPUs)
	t.Logf("  posting: %d entries, %d lines = %.0f lines/s (slowest 1 s window %.0f); latency p50 %.1f ms, p95 %.1f ms, p99 %.1f ms, max %.1f ms",
		rep.Entries, rep.Lines, rep.LinesPerSec, rep.MinWindowLPS, rep.PostP50Ms, rep.PostP95Ms, rep.PostP99Ms, rep.PostMaxMs)
	t.Logf("  trial balance reads: %d, p50 %.1f ms, p99 %.1f ms (journal holds %d lines at the end)", rep.Reads, rep.ReadP50Ms, rep.ReadP99Ms, rep.JournalLines)
	t.Logf("  AC4 target on the dev cluster: %d lines/s sustained, p99 < %d ms", ac4TargetLPS, ac4TargetP99Ms)

	if out := os.Getenv(envBenchOut); out != "" {
		b, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(out, append(b, '\n'), 0o644); err != nil {
			t.Fatalf("write %s: %v", out, err)
		}
	}
	if floor := os.Getenv(envBenchMinLPS); floor != "" {
		if want := envInt(t, envBenchMinLPS, 0); rep.MinWindowLPS < float64(want) {
			t.Errorf("slowest one-second window %.0f lines/s, below the floor %d", rep.MinWindowLPS, want)
		}
	}
	if ceiling := os.Getenv(envBenchMaxP99); ceiling != "" {
		if want := envInt(t, envBenchMaxP99, 0); rep.PostP99Ms > float64(want) {
			t.Errorf("posting p99 %.1f ms, above the ceiling %d ms", rep.PostP99Ms, want)
		}
	}
}
