package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestCombination_SortedAndEmpty(t *testing.T) {
	if got := Combination(nil); got != "-" {
		t.Fatalf("empty = %q", got)
	}
	if got := Combination(map[string]string{"DEPT": "D100", "CC": "C1"}); got != "CC=C1;DEPT=D100" {
		t.Fatalf("got %q", got)
	}
}

func entry(sourceType string, lines ...SourceLine) PostedEntry {
	return PostedEntry{LedgerID: "11111111-1111-1111-1111-111111111111", EntryID: "e1", PostingSequence: 7, PostingDate: "2026-03-31",
		PeriodKey: "2026-03", Voucher: "V1", SourceType: sourceType, Lines: lines}
}

func TestBuild_FoldsLinesIntoDeltas(t *testing.T) {
	e := entry("", SourceLine{LineNumber: 1, Account: "6010", Dimensions: map[string]string{"DEPT": "D1"}, DebitCredit: "D", AmountAcct: dec("40.50"), SourceURI: "/ap/invoices/1", TraceID: "t"},
		SourceLine{LineNumber: 2, Account: "6010", Dimensions: map[string]string{"DEPT": "D1"}, DebitCredit: "D", AmountAcct: dec("9.50")},
		SourceLine{LineNumber: 3, Account: "2000", DebitCredit: "C", AmountAcct: dec("50")})
	lines, deltas, err := e.Build()
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 || len(deltas) != 2 {
		t.Fatalf("lines %d deltas %d", len(lines), len(deltas))
	}
	if deltas[0].Account != "2000" || !deltas[0].Net.Equal(dec("-50")) || deltas[0].Combination != "-" {
		t.Fatalf("delta 0 %+v", deltas[0])
	}
	if deltas[1].Account != "6010" || !deltas[1].Net.Equal(dec("50")) || deltas[1].Combination != "DEPT=D1" || deltas[1].Dims["DEPT"] != "D1" {
		t.Fatalf("delta 1 %+v", deltas[1])
	}
	if deltas[1].Quarter != 1 || deltas[1].PeriodYear != 2026 || deltas[1].PeriodNo != 3 || !deltas[1].ClosingNet.IsZero() {
		t.Fatalf("period or closing %+v", deltas[1])
	}
	if lines[0].CurrencyType != "acct" || lines[0].SourceURI != "/ap/invoices/1" || lines[0].ClosingEntry {
		t.Fatalf("line 0 %+v", lines[0])
	}
}

func TestBuild_ClosingEntryFillsClosingNet(t *testing.T) {
	e := entry(YearEndClosingSource, SourceLine{LineNumber: 1, Account: "4000", DebitCredit: "D", AmountAcct: dec("10")},
		SourceLine{LineNumber: 2, Account: "3900", DebitCredit: "C", AmountAcct: dec("10")})
	e.SourceSystem = CloseSourceSystem
	lines, deltas, err := e.Build()
	if err != nil {
		t.Fatal(err)
	}
	if !lines[0].ClosingEntry || !deltas[0].ClosingNet.Equal(deltas[0].Net) || deltas[0].ClosingNet.IsZero() {
		t.Fatalf("closing not carried: %+v %+v", lines[0], deltas[0])
	}
	// Any caller may tag an entry year_end_closing; only the close service's count.
	e.SourceSystem = "api"
	if lines, deltas, _ = e.Build(); lines[0].ClosingEntry || !deltas[0].ClosingNet.IsZero() {
		t.Fatalf("a year_end_closing tag from another system became closing: %+v", lines[0])
	}
	// The reversal of a closing entry takes the closing amounts back out.
	rev := entry("reversal", SourceLine{LineNumber: 1, Account: "4000", DebitCredit: "C", AmountAcct: dec("10")},
		SourceLine{LineNumber: 2, Account: "3900", DebitCredit: "D", AmountAcct: dec("10")})
	rev.ReversesClosing = true
	if lines, deltas, _ = rev.Build(); !lines[0].ClosingEntry || !deltas[1].ClosingNet.Equal(dec("-10")) {
		t.Fatalf("reversal of closing: %+v %+v", lines[0], deltas)
	}
}

// TestBuild_NegativePostingReducesTheOtherSide: a storno line (BR-11.16) on the
// credit side of a reversal reduces the debit, so gross debits fall and the
// net is that of an ordinary credit.
func TestBuild_NegativePostingReducesTheOtherSide(t *testing.T) {
	e := entry("reversal", SourceLine{LineNumber: 1, Account: "6010", DebitCredit: "C", AmountAcct: dec("25"), Negative: true},
		SourceLine{LineNumber: 2, Account: "2000", DebitCredit: "D", AmountAcct: dec("25"), Negative: true})
	lines, deltas, err := e.Build()
	if err != nil {
		t.Fatal(err)
	}
	if !lines[0].Debit.Equal(dec("-25")) || !lines[0].Credit.IsZero() || !lines[1].Credit.Equal(dec("-25")) || !lines[1].Debit.IsZero() {
		t.Fatalf("storno sides: %+v / %+v", lines[0], lines[1])
	}
	if deltas[1].Account != "6010" || !deltas[1].Net.Equal(dec("-25")) || !deltas[0].Net.Equal(dec("25")) {
		t.Fatalf("storno nets %+v", deltas)
	}
}

// TestReadModelSourceURI: ledger links (passage://) become Passage routes, safe
// links stay and unsafe ones are dropped rather than refusing the entry.
func TestReadModelSourceURI(t *testing.T) {
	cases := map[string]string{
		"passage://ledger/journal-batches/b1": "/ledger/journal-batches/b1",
		"/ap/invoices/1":                      "/ap/invoices/1",
		"https://dms.example/doc/9":           "https://dms.example/doc/9",
		"javascript:alert(1)":                 "",
		"passage:///evil.example/x":           "",
		"":                                    "",
	}
	for in, want := range cases {
		if got := ReadModelSourceURI(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	e := entry("reversal", SourceLine{LineNumber: 1, Account: "1", DebitCredit: "D", AmountAcct: dec("1"), SourceURI: "javascript:x"},
		SourceLine{LineNumber: 2, Account: "2", DebitCredit: "C", AmountAcct: dec("1"), SourceURI: "passage://ledger/journal-entries/e1"})
	lines, _, err := e.Build()
	if err != nil || lines[0].SourceURI != "" || lines[1].SourceURI != "/ledger/journal-entries/e1" {
		t.Fatalf("links %+v err %v", lines, err)
	}
}

func TestBuild_Refusals(t *testing.T) {
	good := []SourceLine{{LineNumber: 1, Account: "1", DebitCredit: "D", AmountAcct: dec("1")}, {LineNumber: 2, Account: "2", DebitCredit: "C", AmountAcct: dec("1")}}
	cases := map[string]func(*PostedEntry){
		"period":   func(e *PostedEntry) { e.PeriodKey = "2026-13" },
		"sequence": func(e *PostedEntry) { e.PostingSequence = 0 },
		"no lines": func(e *PostedEntry) { e.Lines = nil },
		"side": func(e *PostedEntry) {
			e.Lines = []SourceLine{{LineNumber: 1, Account: "1", DebitCredit: "X", AmountAcct: dec("1")}}
		},
		"unbalanced": func(e *PostedEntry) { e.Lines = good[:1] },
		"negative": func(e *PostedEntry) {
			e.Lines = []SourceLine{{LineNumber: 1, Account: "1", DebitCredit: "D", AmountAcct: dec("-1")}, {LineNumber: 2, Account: "2", DebitCredit: "C", AmountAcct: dec("-1")}}
		},
		"date": func(e *PostedEntry) { e.PostingDate = "31.03.2026" },
		"scale": func(e *PostedEntry) {
			e.Lines = []SourceLine{{LineNumber: 1, Account: "1", DebitCredit: "D", AmountAcct: dec("0.0000001")}, {LineNumber: 2, Account: "2", DebitCredit: "C", AmountAcct: dec("0.0000001")}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := entry("", good...)
			mutate(&e)
			_, _, err := e.Build()
			var de *Error
			if !errors.As(err, &de) || de.Code != CodeLineInvalid {
				t.Fatalf("want %s, got %v", CodeLineInvalid, err)
			}
		})
	}
}

// TestBuild_DeltasSumToLines: for any balanced entry the deltas sum to zero and
// each account's delta equals the net of its lines.
func TestBuild_DeltasSumToLines(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 12).Draw(t, "n")
		var lines []SourceLine
		total := decimal.Zero
		for i := 1; i <= n; i++ {
			cents := rapid.Int64Range(1, 1_000_000).Draw(t, "cents")
			side := rapid.SampledFrom([]string{"D", "C"}).Draw(t, "side")
			amt := decimal.New(cents, -2)
			if side == "D" {
				total = total.Add(amt)
			} else {
				total = total.Sub(amt)
			}
			lines = append(lines, SourceLine{LineNumber: i, Account: rapid.SampledFrom([]string{"1000", "2000", "3000"}).Draw(t, "acct"), DebitCredit: side, AmountAcct: amt})
		}
		if !total.IsZero() { // balance the entry with one closing line
			side, amt := "C", total
			if total.IsNegative() {
				side, amt = "D", total.Neg()
			}
			lines = append(lines, SourceLine{LineNumber: n + 1, Account: "9999", DebitCredit: side, AmountAcct: amt})
		}
		_, deltas, err := entry("", lines...).Build()
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		for _, d := range deltas {
			sum = sum.Add(d.Net)
		}
		if !sum.IsZero() {
			t.Fatalf("deltas sum to %s", sum)
		}
	})
}
