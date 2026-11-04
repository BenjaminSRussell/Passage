package store

// Wave 11 Proof (issue #11). AC1 is the property test at scale; AC3 (duplicate
// keys racing) and AC5 (exact negation, linked) are checked inside the same
// concurrent load; AC2 is the table-driven negative test at the bottom.
//
// Expected values never come from the engine: the generator keeps its own
// integer-cent books per (ledger, account, period), and the journal
// re-aggregation is SQL written here, compared with the balance projection row
// by row. RebuildBalances is checked as a third opinion, not as the reference.

import (
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

const (
	// envAC1Entries raises the entry count; the issue's full run is 1000000.
	envAC1Entries = "PASSAGE_LEDGER_AC1_ENTRIES"
	// envAC1Seed replays a failing run with the seed it logged.
	envAC1Seed       = "PASSAGE_LEDGER_AC1_SEED"
	ac1DefaultCount  = 2000
	ac1DefaultSeed   = 20261031
	ac1Workers       = 64
	ac1Ledgers       = 4
	ac1DuplicateMod  = 20 // every 20th entry is sent twice, back to back, by two workers
	ac1ReversalMod   = 50 // every 50th entry is reversed (twice, same key) after the load
	ac1DuplicateSlot = 7
	ac1ReversalSlot  = 3
)

var (
	ac1Accounts = []string{"1000", "1200", "2100", "2110", "4000", "6010"}
	ac1Dates    = []time.Time{
		time.Date(2026, 1, 12, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 10, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC),
	}
	ac1Depts = []string{"", "D100", "D200", "D300"}
	ac1CCs   = []string{"", "C10", "C20"}
)

type ac1Line struct {
	account, side string
	cents         int64
	dims          map[string]string
}

type ac1Entry struct {
	ledger int
	date   time.Time
	lines  []ac1Line
}

// ac1Key is one cell of the generator's books.
type ac1Key struct {
	ledger          int
	account, period string
}

// ac1Cell is debit cents, credit cents and line count.
type ac1Cell struct{ debit, credit, lines int64 }

type ac1Books map[ac1Key]ac1Cell

func (b ac1Books) add(ledger int, period string, l ac1Line, swap bool) {
	k := ac1Key{ledger, l.account, period}
	c := b[k]
	debit := l.side == domain.SideDebit
	if swap {
		debit = !debit
	}
	if debit {
		c.debit += l.cents
	} else {
		c.credit += l.cents
	}
	c.lines++
	b[k] = c
}

func (b ac1Books) merge(o ac1Books) {
	for k, v := range o {
		c := b[k]
		c.debit, c.credit, c.lines = c.debit+v.debit, c.credit+v.credit, c.lines+v.lines
		b[k] = c
	}
}

// ac1Generate derives entry i from (seed, i) alone, so workers build entries on
// the fly and a million entries never sit in memory. 1–3 debit lines of random
// cents, then 1–3 credit lines that split the same total, then shuffled.
func ac1Generate(seed uint64, i int) ac1Entry {
	r := rand.New(rand.NewPCG(seed, uint64(i)))
	e := ac1Entry{ledger: r.IntN(ac1Ledgers), date: ac1Dates[r.IntN(len(ac1Dates))]}
	dims := func() map[string]string {
		d := map[string]string{}
		if v := ac1Depts[r.IntN(len(ac1Depts))]; v != "" {
			d["DEPT"] = v
		}
		if v := ac1CCs[r.IntN(len(ac1CCs))]; v != "" {
			d["CC"] = v
		}
		return d
	}
	nd, nc := 1+r.IntN(3), 1+r.IntN(3)
	var total int64
	for range nd {
		c := 3 + r.Int64N(999_997) // 0.03 .. 9,999.99
		total += c
		e.lines = append(e.lines, ac1Line{ac1Accounts[r.IntN(len(ac1Accounts))], domain.SideDebit, c, dims()})
	}
	remaining := total
	for k := range nc {
		c := remaining
		if still := int64(nc - 1 - k); still > 0 {
			c = 1 + r.Int64N(remaining-still) // leaves at least one cent per credit still to come
		}
		remaining -= c
		e.lines = append(e.lines, ac1Line{ac1Accounts[r.IntN(len(ac1Accounts))], domain.SideCredit, c, dims()})
	}
	r.Shuffle(len(e.lines), func(a, b int) { e.lines[a], e.lines[b] = e.lines[b], e.lines[a] })
	return e
}

func cents(c int64) string { return fmt.Sprintf("%d.%02d", c/100, c%100) }

func signedCents(c int64) string {
	if c < 0 {
		return "-" + cents(-c)
	}
	return cents(c)
}

func ac1Input(seed uint64, i int, g ac1Entry, ledgerID string) PostInput {
	key := fmt.Sprintf("AC1-%d", i)
	hash, _ := domain.HashPayload(map[string]any{"ac1": i, "seed": seed})
	lines := make([]domain.LineInput, 0, len(g.lines))
	for _, l := range g.lines {
		lines = append(lines, domain.LineInput{Account: l.account, Side: l.side, Amount: decimal.New(l.cents, -2), Currency: "USD", Dimensions: l.dims})
	}
	return PostInput{
		LedgerID: ledgerID, DocumentID: ids.New(), PostingDate: g.date, Description: "AC1 " + key,
		Source: ledgerevents.Source{System: "ap", Type: "vendor_invoice", ID: key, URI: "passage://ap/invoices/" + key},
		Lines:  lines, Key: key, Hash: hash, Actor: "test-user", TraceID: testTrace, Today: testToday,
	}
}

func envInt(t *testing.T, name string, def int) int {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		t.Fatalf("%s=%q: want a positive integer", name, v)
	}
	return n
}

func percentile(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := slices.Clone(d)
	slices.Sort(s)
	return s[min(len(s)-1, int(float64(len(s))*p))]
}

// TestPostgres_AC1_RandomConcurrentEntriesBalanceAndProjectionEqualsJournal is
// issue #11 Proof AC1: random balanced entries posted by 64 workers over four
// ledgers; every ledger's trial balance nets to zero and equals the generator's
// books; the balance projection equals a re-aggregation of journal_line row by
// row. Run with PASSAGE_LEDGER_AC1_ENTRIES=1000000 for the full count.
func TestPostgres_AC1_RandomConcurrentEntriesBalanceAndProjectionEqualsJournal(t *testing.T) {
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
	ledgers := []Ledger{e.ledger}
	for n := 1; n < ac1Ledgers; n++ {
		l, err := e.store.CreateLedger(e.ctx, Ledger{LegalEntityID: e.le, Code: fmt.Sprintf("GL%d", n), Name: fmt.Sprintf("Ledger %d", n),
			ChartCode: "STD", Currency: "USD", DimensionAttributes: []string{"DEPT", "CC"}, FuturePostingDays: 366})
		if err != nil {
			t.Fatalf("create ledger %d: %v", n, err)
		}
		ledgers = append(ledgers, l)
	}
	count := envInt(t, envAC1Entries, ac1DefaultCount)
	seed := uint64(envInt(t, envAC1Seed, ac1DefaultSeed))
	t.Logf("AC1: %d entries, seed %d (%s=%d replays this run)", count, seed, envAC1Seed, seed)

	// Phase 1: post. A duplicate job is queued right behind its original so two
	// workers race the same key (AC3).
	type result struct {
		entryID  string
		replayed bool
	}
	var (
		mu         sync.Mutex
		books      = ac1Books{}
		latencies  []time.Duration
		lineTotal  int64
		failures   []error
		dupResults = map[int][]result{}
		originals  = map[int]Entry{}
	)
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if len(failures) < 10 {
			failures = append(failures, err)
		}
	}
	jobs := make(chan int, 4*ac1Workers)
	started := time.Now()
	var wg sync.WaitGroup
	for range ac1Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local, lat, lines := ac1Books{}, make([]time.Duration, 0, count/ac1Workers+8), int64(0)
			for job := range jobs {
				i, duplicate := job, false
				if job < 0 {
					i, duplicate = -job-1, true
				}
				g := ac1Generate(seed, i)
				t0 := time.Now()
				res, err := e.store.PostEntry(e.ctx, ac1Input(seed, i, g, ledgers[g.ledger].ID))
				lat = append(lat, time.Since(t0))
				if err != nil {
					fail(fmt.Errorf("post %d (duplicate=%v): %w", i, duplicate, err))
					continue
				}
				lines += int64(len(g.lines))
				if !duplicate { // the books count the entry once, whichever copy wins
					for _, l := range g.lines {
						local.add(g.ledger, g.date.Format("2006-01"), l, false)
					}
				}
				if i%ac1DuplicateMod == ac1DuplicateSlot || i%ac1ReversalMod == ac1ReversalSlot {
					mu.Lock()
					if i%ac1DuplicateMod == ac1DuplicateSlot {
						dupResults[i] = append(dupResults[i], result{res.Entries[0].ID, res.Idempotency.Replayed})
					}
					if i%ac1ReversalMod == ac1ReversalSlot && !res.Idempotency.Replayed {
						originals[i] = res.Entries[0]
					}
					mu.Unlock()
				}
			}
			mu.Lock()
			books.merge(local)
			latencies = append(latencies, lat...)
			lineTotal += lines
			mu.Unlock()
		}()
	}
	duplicates := 0
	for i := range count {
		jobs <- i
		if i%ac1DuplicateMod == ac1DuplicateSlot {
			jobs <- -i - 1
			duplicates++
		}
	}
	close(jobs)
	wg.Wait()
	elapsed := time.Since(started)
	if len(failures) > 0 {
		t.Fatalf("posting failed: %v", errors.Join(failures...))
	}
	t.Logf("AC1 load (local, informational; AC4 is measured on the dev cluster): %d posts, %d lines in %s = %.0f lines/s, p50 %s, p99 %s",
		len(latencies), lineTotal, elapsed.Round(time.Millisecond), float64(lineTotal)/elapsed.Seconds(),
		percentile(latencies, 0.50), percentile(latencies, 0.99))

	// AC3: each duplicated key made one entry; exactly one of its two posts was a replay of it.
	if len(dupResults) != duplicates {
		t.Fatalf("AC3: %d duplicated keys recorded, want %d", len(dupResults), duplicates)
	}
	for i, rs := range dupResults {
		if len(rs) != 2 || rs[0].entryID != rs[1].entryID || rs[0].replayed == rs[1].replayed {
			t.Fatalf("AC3: key AC1-%d: want one create and one replay of the same entry, got %+v", i, rs)
		}
	}

	// Phase 2: reverse every 50th entry, each reversal sent twice with one key.
	revIdx := slices.Sorted(maps.Keys(originals))
	revJobs := make(chan int, 2*ac1Workers)
	revResults := map[int][]EntryResult{}
	for range ac1Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range revJobs {
				hash, _ := domain.HashPayload(map[string]any{"ac1-reverse": i, "seed": seed})
				res, err := e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: originals[i].ID, Key: fmt.Sprintf("AC1-REV-%d", i),
					Hash: hash, Actor: "test-user", TraceID: testTrace, Today: testToday})
				if err != nil {
					fail(fmt.Errorf("reverse %d: %w", i, err))
					continue
				}
				mu.Lock()
				revResults[i] = append(revResults[i], res)
				mu.Unlock()
			}
		}()
	}
	for _, i := range revIdx {
		revJobs <- i
		revJobs <- i
	}
	close(revJobs)
	wg.Wait()
	if len(failures) > 0 {
		t.Fatalf("reversal failed: %v", errors.Join(failures...))
	}

	// AC5: the reversal is the exact negation of the original, line by line, linked both ways.
	for _, i := range revIdx {
		g, orig, rs := ac1Generate(seed, i), originals[i], revResults[i]
		if len(rs) != 2 || rs[0].Entries[0].ID != rs[1].Entries[0].ID || rs[0].Idempotency.Replayed == rs[1].Idempotency.Replayed {
			t.Fatalf("AC5/AC3: reversal of AC1-%d: want one reversal and one replay, got %+v", i, rs)
		}
		rev := rs[0].Entries[0]
		if rev.ReversalOfEntryID == nil || *rev.ReversalOfEntryID != orig.ID || rev.PostingDate != orig.PostingDate ||
			rev.DocumentID != orig.DocumentID || len(rev.Lines) != len(g.lines) || len(orig.Lines) != len(g.lines) {
			t.Fatalf("AC5: reversal of AC1-%d is not linked to it or has the wrong shape: %+v", i, rev)
		}
		byID := map[string]int{}
		for k, ol := range orig.Lines {
			// The posted original is what was asked for (line k is generator line k).
			want := g.lines[k]
			if ol.Account != want.account || ol.DebitCredit != want.side || ol.Amount != cents(want.cents) || !maps.Equal(ol.Dimensions, want.dims) {
				t.Fatalf("AC1-%d line %d posted as %+v, generated %+v", i, k, ol, want)
			}
			byID[ol.ID] = k
		}
		for _, rl := range rev.Lines {
			k, ok := -1, false
			if rl.ReversalOfLineID != nil {
				k, ok = byID[*rl.ReversalOfLineID]
			}
			if !ok {
				t.Fatalf("AC5: reversal line %+v of AC1-%d links to no original line", rl, i)
			}
			want, ol := g.lines[k], orig.Lines[k]
			if rl.Account != want.account || rl.DebitCredit == want.side || rl.Amount != cents(want.cents) || rl.NegativePosting ||
				rl.DimensionCombinationID != ol.DimensionCombinationID || !maps.Equal(rl.Dimensions, want.dims) {
				t.Fatalf("AC5: reversal line %+v is not the negation of %+v", rl, want)
			}
			delete(byID, ol.ID)
		}
		for _, l := range g.lines {
			books.add(g.ledger, g.date.Format("2006-01"), l, true)
		}
	}

	// Expected entries per ledger, from the generator alone.
	wantEntries := make([]int64, ac1Ledgers)
	for i := range count {
		wantEntries[ac1Generate(seed, i).ledger]++
	}
	for _, i := range revIdx {
		wantEntries[ac1Generate(seed, i).ledger]++
	}

	for n, l := range ledgers {
		// Trial balance (from the projection) nets to zero and equals the books per account.
		tb, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: l.ID})
		if err != nil {
			t.Fatalf("ledger %d trial balance: %v", n, err)
		}
		perAccount := map[string]ac1Cell{}
		var debit, credit int64
		for k, c := range books {
			if k.ledger != n {
				continue
			}
			a := perAccount[k.account]
			a.debit, a.credit, a.lines = a.debit+c.debit, a.credit+c.credit, a.lines+c.lines
			perAccount[k.account] = a
			debit, credit = debit+c.debit, credit+c.credit
		}
		if debit != credit {
			t.Fatalf("generator bug: ledger %d books are unbalanced (%d vs %d)", n, debit, credit)
		}
		if !tb.Balanced || tb.Net != "0.00" || tb.Debit != cents(debit) || tb.Credit != cents(credit) || len(tb.Rows) != len(perAccount) {
			t.Fatalf("AC1: ledger %d trial balance %+v, want debit = credit = %s over %d accounts", n, tb, cents(debit), len(perAccount))
		}
		for _, r := range tb.Rows {
			w := perAccount[r.Account]
			if r.Debit != cents(w.debit) || r.Credit != cents(w.credit) || r.Net != signedCents(w.debit-w.credit) || r.LineCount != w.lines {
				t.Fatalf("AC1: ledger %d account %s: trial balance %+v, books %+v", n, r.Account, r, w)
			}
		}

		// Gap-free sequence: 1..n with n the generator's entry count for the ledger.
		var entries, maxSeq int64
		if err := e.admin.QueryRow(e.ctx, `select count(*), coalesce(max(posting_sequence), 0) from journal_entry where ledger_id = $1`, l.ID).Scan(&entries, &maxSeq); err != nil {
			t.Fatal(err)
		}
		if entries != wantEntries[n] || maxSeq != wantEntries[n] {
			t.Fatalf("AC1/AC3: ledger %d has %d entries up to sequence %d, want %d (duplicates must add nothing)", n, entries, maxSeq, wantEntries[n])
		}
	}

	// Journal re-aggregated here, per (ledger, account, period), equals the books.
	rows, err := e.admin.Query(e.ctx, `select e.ledger_id::text, a.account_number, e.period_key,
			sum(l.amount_acct) filter (where l.debit_credit = 'D')::text, sum(l.amount_acct) filter (where l.debit_credit = 'C')::text, count(*)
		from journal_line l
		join journal_entry e on e.id = l.entry_id and e.tenant_id = l.tenant_id
		join main_account a on a.id = l.main_account_id and a.tenant_id = l.tenant_id
		where l.tenant_id = $1
		group by 1, 2, 3`, e.tenant)
	if err != nil {
		t.Fatal(err)
	}
	ledgerIndex := map[string]int{}
	for n, l := range ledgers {
		ledgerIndex[l.ID] = n
	}
	journal := ac1Books{}
	for rows.Next() {
		var ledgerID, account, period string
		var d, c *string
		var n int64
		if err := rows.Scan(&ledgerID, &account, &period, &d, &c, &n); err != nil {
			t.Fatal(err)
		}
		toCents := func(s *string) int64 {
			if s == nil {
				return 0
			}
			return decimal.RequireFromString(*s).Shift(2).IntPart()
		}
		journal[ac1Key{ledgerIndex[ledgerID], account, period}] = ac1Cell{toCents(d), toCents(c), n}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(journal, books) {
		for k, w := range books {
			if journal[k] != w {
				t.Fatalf("AC1: journal %+v = %+v, books say %+v", k, journal[k], w)
			}
		}
		t.Fatalf("AC1: journal has %d cells, books %d", len(journal), len(books))
	}

	// Balance projection equals the journal re-aggregation, row for row on its full key.
	var differing, projected int64
	if err := e.admin.QueryRow(e.ctx, `with j as (
			select e.ledger_id, l.main_account_id, l.dimension_combination_id, e.period_key,
				coalesce(sum(l.amount_acct) filter (where l.debit_credit = 'D'), 0) as debit,
				coalesce(sum(l.amount_acct) filter (where l.debit_credit = 'C'), 0) as credit,
				count(*) as lines, max(e.posting_sequence) as last_seq
			from journal_line l join journal_entry e on e.id = l.entry_id and e.tenant_id = l.tenant_id
			where l.tenant_id = $1 group by 1, 2, 3, 4),
		b as (select * from balance where tenant_id = $1 and currency_type = 'acct')
		select count(*) filter (where j.ledger_id is null or b.ledger_id is null or j.debit <> b.debit or j.credit <> b.credit
				or j.debit - j.credit <> b.net or j.lines <> b.line_count or j.last_seq <> b.last_posting_sequence),
			count(b.ledger_id)
		from j full join b on b.ledger_id = j.ledger_id and b.main_account_id = j.main_account_id
			and b.dimension_combination_id = j.dimension_combination_id and b.period_key = j.period_key`, e.tenant).Scan(&differing, &projected); err != nil {
		t.Fatal(err)
	}
	if differing != 0 || projected == 0 {
		t.Fatalf("AC1: %d of %d projection rows differ from the journal re-aggregation", differing, projected)
	}
	var net string
	if err := e.admin.QueryRow(e.ctx, `select coalesce(sum(net), 0)::text from balance where tenant_id = $1`, e.tenant).Scan(&net); err != nil || !decimal.RequireFromString(net).IsZero() {
		t.Fatalf("AC1: the projection's net over every ledger is %s, want 0 (%v)", net, err)
	}

	// Third opinion: the service's own rebuild finds nothing to fix.
	for n, l := range ledgers {
		rb, err := e.store.RebuildBalances(e.ctx, l.ID)
		if err != nil || rb.RowsDiffering != 0 || rb.HighWaterSequence != wantEntries[n] {
			t.Fatalf("AC1: ledger %d rebuild %+v %v", n, rb, err)
		}
	}
}

// TestPostgres_AC2_PostingRejectsUnbalancedClosedPeriodAndInvalidDimension is
// issue #11 Proof AC2 through the real posting pipeline: each case is refused
// with its code, writes no entry and no balance, consumes no sequence number,
// and does not cache its idempotency key.
func TestPostgres_AC2_PostingRejectsUnbalancedClosedPeriodAndInvalidDimension(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, e env)
		mutate  func(in *PostInput)
		want    string
	}{
		{name: "unbalanced by one cent", want: "LEDGER_UNBALANCED",
			mutate: func(in *PostInput) { in.Lines[1].Amount = decimal.RequireFromString("99.99") }},
		{name: "debit only", want: "LEDGER_UNBALANCED",
			mutate: func(in *PostInput) { in.Lines = in.Lines[:1] }},
		{name: "credit side larger", want: "LEDGER_UNBALANCED",
			mutate: func(in *PostInput) { in.Lines[1].Amount = decimal.RequireFromString("100.01") }},
		{name: "closed period (posting date before the first open date)", want: "LEDGER_PERIOD_CLOSED",
			prepare: func(t *testing.T, e env) { closePeriodsBefore(t, e, "2026-03-01") },
			mutate:  func(in *PostInput) { in.PostingDate = time.Date(2026, 2, 27, 0, 0, 0, 0, time.UTC) }},
		{name: "dimension attribute not on the ledger", want: domain.CodeDimensionInvalid,
			mutate: func(in *PostInput) { in.Lines[0].Dimensions = map[string]string{"PROJECT": "P1"} }},
		{name: "malformed dimension value", want: domain.CodeDimensionInvalid,
			mutate: func(in *PostInput) { in.Lines[0].Dimensions = map[string]string{"DEPT": "bad value!"} }},
		{name: "suspended dimension value", want: domain.CodeDimensionInactive,
			prepare: func(t *testing.T, e env) {
				if _, _, err := e.store.PutDimensionValue(e.ctx, e.ledger.ID, 0, domain.DimensionValue{Attribute: "DEPT", Value: "D100", Name: "Sales", Suspended: true}); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "dimension value not yet active", want: domain.CodeDimensionInactive,
			prepare: func(t *testing.T, e env) {
				from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
				if _, _, err := e.store.PutDimensionValue(e.ctx, e.ledger.ID, 0, domain.DimensionValue{Attribute: "DEPT", Value: "D100", Name: "Sales", ActiveFrom: &from}); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "required dimension missing under the active structure", want: domain.CodeDimensionRequired,
			prepare: func(t *testing.T, e env) {
				putStructure(t, e, plStructure(domain.StructureActive))
				putStructure(t, e, bsStructure())
			},
			mutate: func(in *PostInput) { in.Lines[0].Dimensions = map[string]string{"CC": "C200"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := setup(t)
			if tc.prepare != nil {
				tc.prepare(t, e)
			}
			bad := vendorInvoice(e.ledger.ID, "AC2-KEY", "100.00")
			if tc.mutate != nil {
				tc.mutate(&bad)
			}
			_, err := e.store.PostEntry(e.ctx, bad)
			if codes := rejectionCodes(err); !slices.Contains(codes, tc.want) {
				t.Fatalf("want %s, got %v (%v)", tc.want, codes, err)
			}
			var entries, balances int64
			if err := e.admin.QueryRow(e.ctx, `select (select count(*) from journal_entry where tenant_id = $1), (select count(*) from balance where tenant_id = $1)`,
				e.tenant).Scan(&entries, &balances); err != nil || entries != 0 || balances != 0 {
				t.Fatalf("a rejected posting wrote %d entries and %d balance rows (%v)", entries, balances, err)
			}
			// The same key is free for a valid entry, which gets sequence 1.
			good := vendorInvoice(e.ledger.ID, "AC2-KEY", "100.00")
			good.Lines[0].Dimensions = map[string]string{"DEPT": "D200"} // valid in every case above
			good.Hash, _ = domain.HashPayload("the corrected entry")
			ok, err := e.store.PostEntry(e.ctx, good)
			if err != nil || ok.Idempotency.Replayed || ok.Entries[0].PostingSequence != 1 {
				t.Fatalf("after the rejection a valid post with the same key must create sequence 1: %+v %v", ok, err)
			}
		})
	}
}
