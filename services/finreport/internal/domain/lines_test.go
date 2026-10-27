package domain

import (
	"errors"
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestSafeSourceURI(t *testing.T) {
	for _, tc := range []struct {
		uri string
		ok  bool
	}{
		{"", true},
		{"/payables/invoices/INV-1001", true},
		{"https://dms.example.com/doc/42?v=1", true},
		{"http://legacy.example.com/voucher/7", true},
		{"//evil.example.com/x", false},
		{`/\evil.example.com`, false},
		{"javascript:alert(1)", false},
		{"data:text/html,<script>", false},
		{"payables/invoices/1", false},
		{"https:///no-host", false},
		{"ftp://files.example.com/a", false},
	} {
		if got := SafeSourceURI(tc.uri); got != tc.ok {
			t.Errorf("SafeSourceURI(%q) = %v, want %v", tc.uri, got, tc.ok)
		}
	}
}

func validLine() JournalLine {
	return JournalLine{LedgerID: "L1", PostingSequence: 7, LineNumber: 1, PostingDate: "2026-03-05", PeriodYear: 2026, PeriodNo: 3,
		Account: "4000", Combination: "-", CurrencyType: "acct", Voucher: "GJ-1", Debit: decimal.Zero,
		Credit: decimal.RequireFromString("12.345678"), SourceURI: "/payables/invoices/1", TraceID: "abc"}
}

func TestJournalLine_Validate(t *testing.T) {
	if err := validLine().Validate(); err != nil {
		t.Fatalf("valid line refused: %v", err)
	}
	if got := validLine().Net(); !got.Equal(decimal.RequireFromString("-12.345678")) {
		t.Fatalf("Net = %s, want debit - credit", got)
	}
	if got := validLine().Period(); got != "2026-03" {
		t.Fatalf("Period = %q", got)
	}
	cases := map[string]func(*JournalLine){
		"sequence zero":     func(l *JournalLine) { l.PostingSequence = 0 },
		"line zero":         func(l *JournalLine) { l.LineNumber = 0 },
		"bad date":          func(l *JournalLine) { l.PostingDate = "2026-02-30" },
		"no account":        func(l *JournalLine) { l.Account = "" },
		"currency type":     func(l *JournalLine) { l.CurrencyType = "eur" },
		"seven places":      func(l *JournalLine) { l.Debit = decimal.RequireFromString("0.0000001") },
		"script source":     func(l *JournalLine) { l.SourceURI = "javascript:alert(1)" },
		"negative period":   func(l *JournalLine) { l.PeriodNo = -1 },
		"protocol-relative": func(l *JournalLine) { l.SourceURI = "//evil.example.com" },
	}
	for name, mutate := range cases {
		l := validLine()
		mutate(&l)
		var de *Error
		if err := l.Validate(); !errors.As(err, &de) || de.Code != CodeLineInvalid {
			t.Errorf("%s: error %v, want %s", name, err, CodeLineInvalid)
		}
	}
}

func TestLineCursor_RoundTripAndRejectsForgeries(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		c := LineCursor{
			PostingDate:     fmt.Sprintf("20%02d-%02d-%02d", rapid.IntRange(0, 99).Draw(t, "y"), rapid.IntRange(1, 12).Draw(t, "m"), rapid.IntRange(1, 28).Draw(t, "d")),
			PostingSequence: rapid.Int64Range(0, 1<<62).Draw(t, "seq"),
			LineNumber:      rapid.IntRange(0, 1<<30).Draw(t, "line"),
			Key:             rapid.IntRange(0, 1<<20).Draw(t, "key"),
		}
		got, err := DecodeLineCursor(c.Encode())
		if err != nil || got != c {
			t.Fatalf("round trip %+v -> %+v, %v", c, got, err)
		}
	})
	for _, bad := range []string{"", "!!", "MjAyNi0wMy0wNXwxfDE", "bm90IGEgY3Vyc29y", LineCursor{PostingDate: "2026-13-01"}.Encode(),
		LineCursor{PostingDate: "2026-01-01", Key: -1}.Encode()} {
		if _, err := DecodeLineCursor(bad); !errors.Is(err, ErrBadCursor) {
			t.Errorf("DecodeLineCursor(%q) = %v, want ErrBadCursor", bad, err)
		}
	}
}

func scopeDefinition(kind Kind) Definition {
	return Definition{Code: "SCOPE", Version: 3, Kind: kind, Rows: []Row{
		{Code: "REV", Kind: RowAccountRange, AccountFrom: "4000", AccountTo: "4999", NormalBalance: "credit"},
		{Code: "COST", Kind: RowAccountRange, AccountFrom: "5000", AccountTo: "5999", NormalBalance: "debit"},
		{Code: "TOT", Kind: RowTotal, References: []string{"REV", "COST"}, NormalBalance: "credit"},
	}}
}

func TestNewLineScope(t *testing.T) {
	rev := Key{LedgerID: "L1", Period: "2026-03", Account: "4000", Combination: "-", CurrencyType: "acct", Amount: d(100)}
	cost := Key{LedgerID: "L1", Period: "2026-03", Account: "5000", Combination: "-", CurrencyType: "acct", Amount: d(40)}

	t.Run("closing entries follow the statement kind", func(t *testing.T) {
		for kind, want := range map[Kind]bool{KindIncomeStatement: true, KindCashFlow: true, KindBalanceSheet: false, KindTrialBalance: false, KindCustom: false} {
			s, err := NewLineScope(scopeDefinition(kind), Cell{Row: "COST", Value: d(40), Keys: []Key{cost}}, 9, -1)
			if err != nil || s.ExcludeClosing != want || s.HWM != 9 {
				t.Errorf("%s: scope %+v, %v; want ExcludeClosing=%v", kind, s, err, want)
			}
		}
	})
	t.Run("a credit row signs keys stored without their sign", func(t *testing.T) {
		s, err := NewLineScope(scopeDefinition(KindIncomeStatement), Cell{Row: "REV", Value: d(100), Keys: []Key{rev}}, 1, -1)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Signed(0, d(-100)); !got.Equal(d(100)) {
			t.Fatalf("credit row line net -100 shows %s, want 100", got)
		}
	})
	t.Run("a total keeps each key's own sign", func(t *testing.T) {
		r, c := rev, cost
		r.Credit = true
		s, err := NewLineScope(scopeDefinition(KindIncomeStatement), Cell{Row: "TOT", Value: d(140), Keys: []Key{r, c}}, 1, -1)
		if err != nil {
			t.Fatal(err)
		}
		if !s.Signed(0, d(-100)).Equal(d(100)) || !s.Signed(1, d(40)).Equal(d(40)) {
			t.Fatalf("total signs: rev %s, cost %s", s.Signed(0, d(-100)), s.Signed(1, d(40)))
		}
		if rec := s.Reconcile([]decimal.Decimal{d(-100), d(40)}); !rec.Reconciled() || !rec.LinesTotal.Equal(d(140)) {
			t.Fatalf("total reconciliation %+v", rec)
		}
	})
	t.Run("one key selects its amount", func(t *testing.T) {
		s, err := NewLineScope(scopeDefinition(KindCustom), Cell{Row: "COST", Value: d(80), Keys: []Key{cost, cost}}, 1, 1)
		if err != nil || len(s.Keys) != 1 || !s.Value.Equal(d(40)) {
			t.Fatalf("scope %+v, %v", s, err)
		}
		if _, err := NewLineScope(scopeDefinition(KindCustom), Cell{Row: "COST", Keys: []Key{cost}}, 1, 1); !errors.Is(err, ErrNoSuchKey) {
			t.Fatalf("key past the end: %v, want ErrNoSuchKey", err)
		}
	})
	t.Run("a row missing from the definition is an error", func(t *testing.T) {
		if _, err := NewLineScope(scopeDefinition(KindCustom), Cell{Row: "GONE"}, 1, -1); err == nil {
			t.Fatal("unknown row accepted")
		}
	})
}

func TestReconcile_FlagsEveryKeyThatDoesNotTie(t *testing.T) {
	a := Key{Account: "1000", Amount: d(10)}
	b := Key{Account: "1001", Amount: d(5)}
	s := LineScope{Keys: []Key{a, b}, Value: d(15)}
	if rec := s.Reconcile([]decimal.Decimal{d(10), d(5)}); !rec.Reconciled() || !rec.Difference.IsZero() {
		t.Fatalf("tied lines: %+v", rec)
	}
	// A missing line: the gap shows as the difference.
	rec := s.Reconcile([]decimal.Decimal{d(10), d(2)})
	if rec.Reconciled() || rec.UnreconciledKeys != 1 || !rec.Difference.Equal(d(3)) {
		t.Fatalf("gap: %+v", rec)
	}
	// Errors that cancel in total still leave both keys unreconciled.
	rec = s.Reconcile([]decimal.Decimal{d(12), d(3)})
	if rec.Reconciled() || rec.UnreconciledKeys != 2 || !rec.Difference.IsZero() {
		t.Fatalf("offsetting errors: %+v", rec)
	}
}

// AC-15.3 (FR-15.7, BR-15.4) for the evaluator and the drill rules: journal
// lines are aggregated into balances the way the read model does, the
// statement is evaluated, and for every balance-backed cell the lines each key
// selects (same balance, at or below the HWM, closing entries left out where
// the kind says so), signed by their key, sum to the cell exactly. The store
// test TestDrillLines_PropertyPagesSumToCell checks the SQL and the paging.
func TestDrill_LinesSumToEveryCell(t *testing.T) {
	cal := calendarMonths(t, 4)
	rapid.Check(t, func(t *rapid.T) {
		kind := rapid.SampledFrom([]Kind{KindIncomeStatement, KindBalanceSheet, KindCashFlow, KindCustom}).Draw(t, "kind")
		def := Definition{Code: "PROP", Name: "Property", Kind: kind,
			Rows: []Row{
				{Code: "REV", Kind: RowAccountRange, Label: "Revenue", AccountFrom: "4000", AccountTo: "4999", NormalBalance: "credit"},
				{Code: "COST", Kind: RowAccountRange, Label: "Cost", AccountFrom: "5000", AccountTo: "5999", NormalBalance: "debit"},
				{Code: "CC", Kind: RowDimensionFilter, Label: "CC100", DimensionFilter: map[string][]string{"cc": {"CC100"}}, NormalBalance: "debit"},
				{Code: "TOT", Kind: RowTotal, Label: "Total", References: []string{"REV", "COST", "REV"}, NormalBalance: "credit"},
			},
			Columns: []Column{{Code: "P", Label: "P", PeriodExpr: "P"}, {Code: "Y", Label: "YTD", PeriodExpr: "YTD"}},
		}
		hwm := rapid.Int64Range(1, 60).Draw(t, "hwm")
		n := rapid.IntRange(0, 50).Draw(t, "n")
		var lines []JournalLine
		type bk struct {
			period            int
			account, cc, curr string
		}
		bals := map[bk]*Balance{}
		for i := 0; i < n; i++ {
			l := JournalLine{LedgerID: "L1", PostingSequence: int64(i + 1), LineNumber: 1, PostingDate: "2026-01-01",
				Account:      rapid.SampledFrom([]string{"4000", "4500", "5000", "5100"}).Draw(t, "account"),
				Combination:  rapid.SampledFrom([]string{"CC100", "CC200"}).Draw(t, "cc"),
				CurrencyType: "acct", ClosingEntry: rapid.IntRange(0, 4).Draw(t, "closing") == 0,
				Debit: decimal.New(rapid.Int64Range(0, 100000).Draw(t, "debit"), -2), Credit: decimal.New(rapid.Int64Range(0, 100000).Draw(t, "credit"), -2)}
			p := rapid.IntRange(0, 3).Draw(t, "period")
			l.PeriodYear, l.PeriodNo = cal.Periods()[p].Year, cal.Periods()[p].No
			lines = append(lines, l)
			if l.PostingSequence > hwm {
				continue // posted after the run: not in the balances the run read
			}
			k := bk{p, l.Account, l.Combination, l.CurrencyType}
			b := bals[k]
			if b == nil {
				b = &Balance{LedgerID: "L1", Period: p, Account: l.Account, Combination: l.Combination, Dims: map[string]string{"cc": l.Combination},
					CurrencyType: "acct", Net: decimal.Zero, ClosingNet: decimal.Zero}
				bals[k] = b
			}
			b.Net = b.Net.Add(l.Net())
			if l.ClosingEntry {
				b.ClosingNet = b.ClosingNet.Add(l.Net())
			}
		}
		var in []Balance
		for _, b := range bals {
			in = append(in, *b)
		}
		at := rapid.IntRange(0, 3).Draw(t, "at")
		res, err := Evaluate(Input{Definition: def, LedgerID: "L1", CurrencyType: "acct", Calendar: cal, At: at, Balances: in})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range res.Cells {
			if c.Computed || c.Blank {
				continue
			}
			scope, err := NewLineScope(def, c, hwm, -1)
			if err != nil {
				t.Fatal(err)
			}
			net := make([]decimal.Decimal, len(scope.Keys))
			total := decimal.Zero
			for i, k := range scope.Keys {
				net[i] = decimal.Zero
				for _, l := range lines {
					if l.Period() == k.Period && l.Account == k.Account && l.Combination == k.Combination && l.CurrencyType == k.CurrencyType &&
						l.PostingSequence <= scope.HWM && !(scope.ExcludeClosing && l.ClosingEntry) {
						net[i] = net[i].Add(l.Net())
						total = total.Add(scope.Signed(i, l.Net()))
					}
				}
			}
			rec := scope.Reconcile(net)
			if !rec.Reconciled() || !total.Equal(c.Value) || !rec.LinesTotal.Equal(c.Value) {
				t.Fatalf("%s cell %s/%s = %s, lines sum %s: %+v", kind, c.Row, c.Column, c.Value, total, rec)
			}
		}
	})
}
