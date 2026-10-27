package domain

import (
	"encoding/csv"
	"os"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// loadFixture reads testdata/fx-revenue-20.csv, the 20-row hand-computed fixture.
func loadFixture(t *testing.T) []JournalLine {
	t.Helper()
	f, err := os.Open("../../testdata/fx-revenue-20.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var lines []JournalLine
	for _, row := range rows[1:] {
		lines = append(lines, JournalLine{AccountCategory: row[0], Status: row[1], Amount: decimal.RequireFromString(row[2])})
	}
	return lines
}

func TestFormulas_Fixture20Rows(t *testing.T) {
	lines := loadFixture(t)
	if len(lines) != 20 {
		t.Fatalf("fixture has %d rows, want 20", len(lines))
	}
	revenue := Revenue(lines)
	cogs := COGS(lines)
	if !revenue.Equal(decimal.RequireFromString("1760.00")) {
		t.Errorf("revenue = %s, want 1760.00", revenue)
	}
	if !cogs.Equal(decimal.RequireFromString("510.99")) {
		t.Errorf("cogs = %s, want 510.99", cogs)
	}
	if gm := GrossMargin(revenue, cogs); !gm.Equal(decimal.RequireFromString("1249.01")) {
		t.Errorf("gross margin = %s, want 1249.01", gm)
	}
	pct, ok := GrossMarginPct(revenue, cogs)
	if !ok || !pct.Round(6).Equal(decimal.RequireFromString("0.709665")) {
		t.Errorf("gross margin pct = %s (ok %v), want 0.709665", pct.Round(6), ok)
	}
}

func TestGrossMarginPct_ZeroRevenueIsNull(t *testing.T) {
	if _, ok := GrossMarginPct(decimal.Zero, decimal.NewFromInt(10)); ok {
		t.Fatal("zero revenue must return no value, never infinity")
	}
}

func TestRevenue_NegationProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 30).Draw(t, "n")
		lines := make([]JournalLine, n)
		flipped := make([]JournalLine, n)
		for i := range lines {
			cents := rapid.Int64Range(-1_000_000, 1_000_000).Draw(t, "cents")
			amount := decimal.New(cents, -2)
			lines[i] = JournalLine{AccountCategory: "REVENUE", Status: "POSTED", Amount: amount}
			flipped[i] = JournalLine{AccountCategory: "REVENUE", Status: "POSTED", Amount: amount.Neg()}
		}
		if !Revenue(flipped).Equal(Revenue(lines).Neg()) {
			t.Fatalf("revenue of negated lines must be the negation of revenue")
		}
	})
}

// BR-46.3: a reversal posts on its own date with the opposite sign, so the
// original day keeps its revenue, the reversal day shows the negative, and the
// two days together net to zero.
func TestRevenue_ReversalNetsToZeroAcrossItsOwnDay(t *testing.T) {
	original := []JournalLine{{AccountCategory: "REVENUE", Status: "POSTED", Amount: decimal.RequireFromString("-1200.00")}}
	reversal := []JournalLine{{AccountCategory: "REVENUE", Status: "POSTED", Amount: decimal.RequireFromString("1200.00")}}
	if got := Revenue(original); !got.Equal(decimal.RequireFromString("1200")) {
		t.Fatalf("original day revenue = %s, want 1200", got)
	}
	if got := Revenue(reversal); !got.Equal(decimal.RequireFromString("-1200")) {
		t.Fatalf("reversal day revenue = %s, want -1200", got)
	}
	if got := Revenue(append(original, reversal...)); !got.IsZero() {
		t.Fatalf("both days = %s, want 0", got)
	}
}
