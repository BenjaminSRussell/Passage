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

func TestMatchLine_ReferenceRuleCarriesGarbledCounterparty(t *testing.T) {
	tx := facts("120.00", "2026-10-22", "E2E00042", "Vendor 042 Holdings")
	other := facts("120.00", "2026-10-22", "E2E00043", "Vendor 043 Holdings")
	line := facts("120.00", "2026-10-23", "E2E00042", "ZQX unknown payer")
	cands := map[string]Facts{"tx-42": tx, "tx-43": other}
	if d := Decide([]Candidate{{"tx-42", Score(line, tx, tol)}, {"tx-43", Score(line, other, tol)}}, DefaultThresholds); d.Status == StatusMatched {
		t.Fatalf("balanced rule alone must not auto-match, got %+v", d)
	}
	d := MatchLine(line, cands, DefaultRules(tol), DefaultThresholds)
	if d.Status != StatusMatched || d.Best.ID != "tx-42" {
		t.Fatalf("decision = %+v; want tx-42 auto-matched by the reference rule", d)
	}
}

// TestMatchLine_Fixture1000AutoMatchRate runs a deterministic 1,000-line
// fixture (AC1 / AC-36.2): 600 exact lines, 360 reference-only lines (amount and
// reference agree, value date one day late, counterparty garbled), 20 lines whose
// reference and counterparty both differ (a person must decide) and 20 bank fees
// with no book counterpart. At least 95% of all lines must be auto-matched to the
// right transaction and none to a wrong one.
func TestMatchLine_Fixture1000AutoMatchRate(t *testing.T) {
	const transactions = 980
	txs := make(map[string]Facts, transactions)
	for i := 0; i < transactions; i++ {
		txs[fmt.Sprintf("tx-%d", i)] = Facts{
			Amount:       decimal.New(int64(10000+(i%53)*100), -2),
			ValueDate:    fmt.Sprintf("2026-10-%02d", 1+i%28),
			Reference:    fmt.Sprintf("E2E%05d", i),
			Counterparty: fmt.Sprintf("Vendor %03d Holdings", i),
		}
	}
	type line struct {
		facts    Facts
		expected string
	}
	var lines []line
	for i := 0; i < 600; i++ {
		lines = append(lines, line{txs[fmt.Sprintf("tx-%d", i)], fmt.Sprintf("tx-%d", i)})
	}
	for i := 600; i < 960; i++ {
		l := txs[fmt.Sprintf("tx-%d", i)]
		l.Counterparty = fmt.Sprintf("ZQX%d unknown payer", i)
		day, _ := Day(l.ValueDate)
		l.ValueDate = day.AddDate(0, 0, 1).Format("2006-01-02")
		lines = append(lines, line{l, fmt.Sprintf("tx-%d", i)})
	}
	for i := 960; i < 980; i++ {
		l := txs[fmt.Sprintf("tx-%d", i)]
		l.Reference = fmt.Sprintf("OTHER%d", i)
		l.Counterparty = fmt.Sprintf("Someone Else %d", i)
		lines = append(lines, line{l, ""})
	}
	for i := 0; i < 20; i++ {
		lines = append(lines, line{Facts{Amount: decimal.RequireFromString("-12.50"), ValueDate: "2026-10-05", Reference: fmt.Sprintf("FEE%d", i), Counterparty: "Example Bank AG"}, ""})
	}
	if len(lines) != 1000 {
		t.Fatalf("fixture has %d lines", len(lines))
	}

	var autoCorrect, autoWrong int
	for _, l := range lines {
		cands := map[string]Facts{}
		for id, tx := range txs {
			if l.facts.Amount.Sub(tx.Amount).Abs().LessThanOrEqual(tol.Amount) {
				cands[id] = tx
			}
		}
		d := MatchLine(l.facts, cands, DefaultRules(tol), DefaultThresholds)
		switch {
		case d.Status != StatusMatched:
		case d.Best.ID == l.expected:
			autoCorrect++
		default:
			autoWrong++
		}
	}
	t.Logf("auto-matched correctly %d of %d, wrongly %d", autoCorrect, len(lines), autoWrong)
	if autoWrong != 0 {
		t.Fatalf("%d lines were auto-matched to the wrong transaction", autoWrong)
	}
	if autoCorrect*100 < 95*len(lines) {
		t.Fatalf("auto-matched %d of %d lines correctly; want at least 95%%", autoCorrect, len(lines))
	}
}
