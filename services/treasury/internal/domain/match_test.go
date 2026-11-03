package domain

import (
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

var tol = Tolerance{Amount: decimal.RequireFromString("0.05"), DateWindowDays: 2}

func facts(amount, date, ref, cp string) Facts {
	return Facts{Amount: decimal.RequireFromString(amount), ValueDate: date, Reference: ref, Counterparty: cp}
}

func TestScore_CriteriaTable(t *testing.T) {
	line := facts("100.00", "2026-10-22", "INV-778812", "Northwind Traders GmbH")
	tests := []struct {
		name string
		cand Facts
		want int
	}{
		{"all criteria equal", line, 100},
		{"amount 0.01 off inside tolerance 0.05", facts("100.01", "2026-10-22", "INV-778812", "Northwind Traders GmbH"), 92},
		{"value date one day off inside window 2", facts("100.00", "2026-10-23", "INV-778812", "Northwind Traders GmbH"), 90},
		{"reference contained in the other", facts("100.00", "2026-10-22", "778812", "Northwind Traders GmbH"), 88},
		{"no reference or counterparty on either side", facts("100.00", "2026-10-22", "", ""), 60},
		{"amount outside tolerance", facts("101.00", "2026-10-22", "INV-778812", "Northwind Traders GmbH"), 60},
		{"date outside window", facts("100.00", "2026-10-30", "INV-778812", "Northwind Traders GmbH"), 80},
		{"unrelated candidate", facts("7.00", "2026-01-01", "X1", "Acme"), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Score(line, tc.cand, tol); got != tc.want {
				t.Fatalf("Score = %d; want %d", got, tc.want)
			}
		})
	}
}

func TestDecide_Table(t *testing.T) {
	c := func(id string, s int) Candidate { return Candidate{ID: id, Score: s} }
	tests := []struct {
		name   string
		cands  []Candidate
		status Status
		best   string
	}{
		{"no candidates", nil, StatusUnmatched, ""},
		{"best below suggest", []Candidate{c("a", 55)}, StatusUnmatched, ""},
		{"best between suggest and auto", []Candidate{c("a", 85)}, StatusSuggested, "a"},
		{"unique leader at auto", []Candidate{c("a", 95), c("b", 40)}, StatusMatched, "a"},
		{"leader inside tie margin is downgraded", []Candidate{c("a", 95), c("b", 91)}, StatusSuggested, "a"},
		{"leader exactly tie margin ahead auto-matches", []Candidate{c("a", 95), c("b", 90)}, StatusMatched, "a"},
		{"ties at the top are suggested", []Candidate{c("b", 96), c("a", 96)}, StatusSuggested, "a"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide(tc.cands, DefaultThresholds)
			if d.Status != tc.status {
				t.Fatalf("status = %s; want %s", d.Status, tc.status)
			}
			if tc.best == "" {
				if d.Best != nil {
					t.Fatalf("best = %+v; want none", d.Best)
				}
				return
			}
			if d.Best == nil || d.Best.ID != tc.best {
				t.Fatalf("best = %+v; want %s", d.Best, tc.best)
			}
		})
	}
}

func TestThresholds_Valid(t *testing.T) {
	if err := DefaultThresholds.Valid(); err != nil {
		t.Fatal(err)
	}
	bads := []Thresholds{
		{Auto: 59, Suggest: 50, TieMargin: 5},
		{Auto: 90, Suggest: 90, TieMargin: 5},
		{Auto: 90, Suggest: 60, TieMargin: 21},
	}
	for _, bad := range bads {
		if bad.Valid() == nil {
			t.Fatalf("%+v must be rejected", bad)
		}
	}
}

func TestScore_PropertiesHold(t *testing.T) {
	amount := rapid.Int64Range(-1000000, 1000000)
	day := rapid.IntRange(0, 400)
	text := rapid.StringOf(rapid.SampledFrom([]rune("ABCXYZ 0123456789-/")))
	dateOf := func(d int) string {
		return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, d).Format("2006-01-02")
	}
	rapid.Check(t, func(t *rapid.T) {
		a := decimal.New(amount.Draw(t, "a"), -2)
		b := decimal.New(amount.Draw(t, "b"), -2)
		x := Facts{Amount: a, ValueDate: dateOf(day.Draw(t, "dx")), Reference: text.Draw(t, "ref"), Counterparty: text.Draw(t, "cx")}
		y := Facts{Amount: b, ValueDate: dateOf(day.Draw(t, "dy")), Reference: text.Draw(t, "ry"), Counterparty: text.Draw(t, "cy")}
		s := Score(x, y, tol)
		if s < 0 || s > 100 {
			t.Fatalf("score %d outside 0..100", s)
		}
		// A candidate identical to the line scores 100 when every criterion has content.
		if r := normalizedReference(x.Reference); r != "" && !placeholderReference(r) && len(trigrams(x.Counterparty)) > 0 {
			if got := Score(x, x, tol); got != 100 {
				t.Fatalf("identical candidate scored %d", got)
			}
		}
		// Moving the amount further from the line never raises the score.
		farther := y
		step := rapid.IntRange(1, 1000).Draw(t, "step")
		farther.Amount = y.Amount.Add(decimal.NewFromInt(int64(step)))
		if a.Sub(farther.Amount).Abs().GreaterThanOrEqual(a.Sub(y.Amount).Abs()) && Score(x, farther, tol) > Score(x, y, tol) {
			t.Fatalf("score rose when the amount moved away")
		}
	})
}

func TestDecide_AutoAlwaysHasUniqueLeader(t *testing.T) {
	score := rapid.IntRange(0, 100)
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 5).Draw(t, "n")
		cands := make([]Candidate, n)
		for i := range cands {
			cands[i] = Candidate{ID: fmt.Sprintf("c%d", i), Score: score.Draw(t, "s")}
		}
		d := Decide(cands, DefaultThresholds)
		if d.Status != StatusMatched {
			return
		}
		if d.Best.Score < DefaultThresholds.Auto {
			t.Fatalf("auto-match below threshold: %+v", d.Best)
		}
		for _, c := range cands {
			if c.ID != d.Best.ID && c.Score > d.Best.Score-DefaultThresholds.TieMargin {
				t.Fatalf("auto-match %+v with runner-up %+v inside the tie margin", d.Best, c)
			}
		}
	})
}

func TestMatchLine_ReferenceRule(t *testing.T) {
	garbled := func(f Facts, date string) Facts {
		f.Counterparty, f.ValueDate = "ZQX unknown payer", date
		return f
	}
	tx := facts("120.00", "2026-10-22", "E2E00042", "Vendor 042 Holdings")
	other := facts("120.00", "2026-10-22", "E2E00043", "Vendor 043 Holdings")
	loan := func(date string) Facts { return facts("500.00", date, "LOAN-12345", "Loan Servicer") }
	tests := []struct {
		name  string
		line  Facts
		cands map[string]Facts
		want  Status
		best  string
	}{
		{"unique long reference inside the window", garbled(tx, "2026-10-23"), map[string]Facts{"tx-42": tx, "tx-43": other}, StatusMatched, "tx-42"},
		{"recurring reference, line 30 days off the only same-reference item", garbled(loan("2026-11-21"), "2026-11-21"), map[string]Facts{"jan": loan("2026-10-22"), "feb": facts("500.00", "2026-12-22", "LOAN-12346", "Loan Servicer")}, StatusSuggested, "jan"},
		{"recurring reference on several open items is ambiguous", garbled(loan("2026-10-23"), "2026-10-23"), map[string]Facts{"a": loan("2026-10-22"), "b": loan("2026-11-21")}, StatusSuggested, "a"},
		{"short numeric reference is not trusted", garbled(facts("75.00", "2026-10-22", "1001", "x"), "2026-10-23"), map[string]Facts{"a": facts("75.00", "2026-10-22", "1001", "Vendor A")}, StatusSuggested, "a"},
		{"placeholder reference is not trusted", garbled(facts("75.00", "2026-10-22", "NOTPROVIDED", "x"), "2026-10-23"), map[string]Facts{"a": facts("75.00", "2026-10-22", "NOTPROVIDED", "Vendor A")}, StatusUnmatched, ""},
		{"N/A placeholder is not trusted", garbled(facts("75.00", "2026-10-22", "N/A", "x"), "2026-10-23"), map[string]Facts{"a": facts("75.00", "2026-10-22", "N/A", "Vendor A")}, StatusUnmatched, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := MatchLine(tc.line, tc.cands, DefaultRules(tol), DefaultThresholds)
			if d.Status != tc.want || (tc.best != "" && (d.Best == nil || d.Best.ID != tc.best)) {
				t.Fatalf("decision = %+v; want %s %s", d, tc.want, tc.best)
			}
		})
	}
}

type fixtureLine struct {
	facts    Facts
	expected string
	group    string
}

func fixtureTransactions() (map[string]Facts, []fixtureLine) {
	txs := map[string]Facts{}
	var lines []fixtureLine
	add := func(id string, f Facts) { txs[id] = f }
	garble := func(f Facts, n int) Facts {
		day, _ := Day(f.ValueDate)
		f.ValueDate = day.AddDate(0, 0, 1).Format("2006-01-02")
		f.Counterparty = fmt.Sprintf("ZQX%d unknown payer", n)
		return f
	}
	for i := 0; i < 750; i++ {
		add(fmt.Sprintf("tx-%d", i), Facts{
			Amount: decimal.New(int64(10000+(i%53)*100), -2), ValueDate: fmt.Sprintf("2026-10-%02d", 1+i%28),
			Reference: fmt.Sprintf("E2E%05d", i), Counterparty: fmt.Sprintf("Vendor %03d Holdings", i),
		})
	}
	for i := 0; i < 560; i++ {
		id := fmt.Sprintf("tx-%d", i)
		lines = append(lines, fixtureLine{txs[id], id, "exact"})
	}
	for i := 560; i < 750; i++ {
		id := fmt.Sprintf("tx-%d", i)
		lines = append(lines, fixtureLine{garble(txs[id], i), id, "reference-only"})
	}
	start, _ := Day("2026-01-01")
	for s := 0; s < 5; s++ {
		for k := 0; k < 6; k++ {
			id := fmt.Sprintf("loan-%d-%d", s, k)
			f := Facts{Amount: decimal.New(int64(50000+s*1000), -2), ValueDate: start.AddDate(0, 0, 30*k).Format("2006-01-02"), Reference: fmt.Sprintf("LOAN-1000%d", s), Counterparty: fmt.Sprintf("Loan Servicer %d", s)}
			add(id, f)
			lines = append(lines, fixtureLine{f, id, "recurring-exact"})
			if k < 4 {
				lines = append(lines, fixtureLine{garble(f, s*10+k), id, "recurring-garbled"})
			}
		}
	}
	for r := 0; r < 10; r++ {
		for k := 0; k < 2; k++ {
			id := fmt.Sprintf("short-%d-%d", r, k)
			f := Facts{Amount: decimal.New(int64(7000+r*100+k*37), -2), ValueDate: "2026-09-15", Reference: fmt.Sprintf("10%02d", r), Counterparty: fmt.Sprintf("Short Ref Vendor %d%d", r, k)}
			add(id, f)
			lines = append(lines, fixtureLine{garble(f, r*10+k), id, "short-reference"})
		}
	}
	for i := 0; i < 15; i++ {
		id := fmt.Sprintf("placeholder-%d", i)
		f := Facts{Amount: decimal.New(int64(9000+i*113), -2), ValueDate: "2026-09-20", Reference: "NOTPROVIDED", Counterparty: fmt.Sprintf("Placeholder Payer %d", i)}
		add(id, f)
		lines = append(lines, fixtureLine{garble(f, i), id, "placeholder-reference"})
	}
	for i := 0; i < 15; i++ {
		id := fmt.Sprintf("tolerance-%d", i)
		f := Facts{Amount: decimal.New(int64(31000+i*211), -2), ValueDate: "2026-09-25", Reference: fmt.Sprintf("TOL%05d", i), Counterparty: fmt.Sprintf("Tolerance Payer %d", i)}
		add(id, f)
		l := f
		l.Amount = l.Amount.Add(decimal.RequireFromString("0.03"))
		lines = append(lines, fixtureLine{l, id, "amount-within-tolerance"})
	}
	for i := 0; i < 150; i++ {
		lines = append(lines, fixtureLine{Facts{Amount: decimal.RequireFromString("-12.50"), ValueDate: "2026-10-05", Reference: fmt.Sprintf("FEE%d", i), Counterparty: "Example Bank AG"}, "", "bank-fee"})
	}
	return txs, lines
}

// TestMatchLine_AdversarialFixture1000 measures AC1 / AC-36.2 on a deterministic
// 1,000-line fixture that includes recurring payments, short and placeholder
// references, amounts inside the tolerance and 150 bank fees. Every candidate is
// scored; nothing is prefiltered by amount. The test fails on any wrong
// auto-match and records the honest rate; it pins the measured per-group
// outcome so a regression in either direction is visible.
func TestMatchLine_AdversarialFixture1000(t *testing.T) {
	txs, lines := fixtureTransactions()
	if len(lines) != 1000 {
		t.Fatalf("fixture has %d lines", len(lines))
	}
	autoCorrect := map[string]int{}
	total := map[string]int{}
	var autoWrong int
	for _, l := range lines {
		total[l.group]++
		d := MatchLine(l.facts, txs, DefaultRules(tol), DefaultThresholds)
		if d.Status != StatusMatched {
			continue
		}
		if d.Best.ID == l.expected {
			autoCorrect[l.group]++
		} else {
			autoWrong++
			t.Errorf("%s line %+v auto-matched to %s; expected %q", l.group, l.facts, d.Best.ID, l.expected)
		}
	}
	correct, withCounterpart := 0, 0
	for g, n := range total {
		correct += autoCorrect[g]
		if g != "bank-fee" {
			withCounterpart += n
		}
	}
	t.Logf("auto-matched correctly %d of %d lines (%.1f%%), %.1f%% of the %d lines with a book counterpart; wrong %d; by group %v of %v",
		correct, len(lines), 100*float64(correct)/float64(len(lines)), 100*float64(correct)/float64(withCounterpart), withCounterpart, autoWrong, autoCorrect, total)
	if autoWrong != 0 {
		t.Fatalf("%d wrong auto-matches", autoWrong)
	}
	want := map[string]int{"exact": 560, "reference-only": 190, "recurring-exact": 30}
	for g, n := range want {
		if autoCorrect[g] != n {
			t.Errorf("group %s: %d auto-matched correctly; want %d", g, autoCorrect[g], n)
		}
	}
	for _, g := range []string{"recurring-garbled", "short-reference", "placeholder-reference", "amount-within-tolerance", "bank-fee"} {
		if autoCorrect[g] != 0 {
			t.Errorf("group %s: %d auto-matched; a person must decide these", g, autoCorrect[g])
		}
	}
}

func TestScore_RemittanceReferenceCountsWhenEndToEndIsAPlaceholder(t *testing.T) {
	line := facts("100.00", "2026-10-22", "NOTPROVIDED", "")
	line.AltReference = "INV-778812"
	cand := facts("100.00", "2026-10-22", "INV-778812", "")
	if got := Score(line, cand, tol); got != 85 {
		t.Fatalf("score = %d; want 85 (amount, date and the remittance reference)", got)
	}
	line.AltReference = ""
	if got := Score(line, cand, tol); got != 60 {
		t.Fatalf("score = %d; want 60 without the remittance reference", got)
	}
}
