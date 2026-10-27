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
		if refStrip.ReplaceAllString(x.Reference, "") != "" && len(trigrams(x.Counterparty)) > 0 {
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

// TestDecide_RecordedFixturePrecision runs the scorer over a 1,000-line
// fixture: 600 exact lines, 250 reference-only lines (amount and reference
// agree, value date one day late, counterparty garbled) and 150 bank fees with
// no book counterpart. No wrong line may be auto-matched; reference-only lines
// are suggested, not auto-matched, in this slice.
func TestDecide_RecordedFixturePrecision(t *testing.T) {
	const transactions = 850 // the reference-only lines cover transactions 600..849
	txs := make([]Facts, transactions)
	for i := range txs {
		cents := 10000 + (i%53)*100
		day := 1 + i%28
		txs[i] = Facts{
			Amount:       decimal.New(int64(cents), -2),
			ValueDate:    fmt.Sprintf("2026-10-%02d", day),
			Reference:    fmt.Sprintf("E2E%05d", i),
			Counterparty: fmt.Sprintf("Vendor %03d Holdings", i),
		}
	}
	type line struct {
		facts    Facts
		expected string // "" when no transaction should match
	}
	var lines []line
	for i := 0; i < 600; i++ {
		lines = append(lines, line{txs[i], fmt.Sprintf("tx-%d", i)})
	}
	for i := 600; i < 850; i++ {
		l := txs[i]
		l.Counterparty = "ZQX" + fmt.Sprint(i) + " unknown payer"
		next, _ := Day(l.ValueDate)
		l.ValueDate = next.AddDate(0, 0, 1).Format("2006-01-02")
		lines = append(lines, line{l, fmt.Sprintf("tx-%d", i)})
	}
	for i := 850; i < 1000; i++ {
		fee := Facts{Amount: decimal.RequireFromString("-12.50"), ValueDate: "2026-10-05", Reference: fmt.Sprintf("FEE%d", i), Counterparty: "Example Bank AG"}
		lines = append(lines, line{fee, ""})
	}

	var autoCorrect, autoWrong, suggestedCorrect int
	for _, l := range lines {
		var cands []Candidate
		for i, tx := range txs {
			if l.facts.Amount.Sub(tx.Amount).Abs().GreaterThan(tol.Amount) {
				continue
			}
			cands = append(cands, Candidate{ID: fmt.Sprintf("tx-%d", i), Score: Score(l.facts, tx, tol)})
		}
		d := Decide(cands, DefaultThresholds)
		switch {
		case d.Status == StatusMatched && d.Best.ID == l.expected:
			autoCorrect++
		case d.Status == StatusMatched:
			autoWrong++
		case d.Status == StatusSuggested && d.Best != nil && d.Best.ID == l.expected:
			suggestedCorrect++
		}
	}
	t.Logf("auto correct %d, auto wrong %d, suggested correct %d of %d lines", autoCorrect, autoWrong, suggestedCorrect, len(lines))
	if autoWrong != 0 {
		t.Fatalf("%d lines were auto-matched to the wrong transaction", autoWrong)
	}
	if autoCorrect < 600 {
		t.Fatalf("auto-matched %d exact lines; want all 600", autoCorrect)
	}
}
