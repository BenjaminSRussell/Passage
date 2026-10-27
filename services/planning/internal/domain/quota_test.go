package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func qs(id string, quota, alloc, base string, prio int) QuotaSource {
	return QuotaSource{ID: id, Type: SourceVendor, Quota: dec(quota), Allocated: dec(alloc), BaseQty: dec(base), Priority: prio}
}

// BR-22.12 example: A quota 60 / allocated 300; B quota 40 / allocated 100.
func brQuotaSources() []QuotaSource {
	return []QuotaSource{qs("A", "60", "300", "0", 1), qs("B", "40", "100", "0", 2)}
}

func TestAllocateWhole_LowestRatingWins(t *testing.T) {
	got, err := Allocate(brQuotaSources(), dec("100"), false, decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SourceID != "B" || !got[0].Qty.Equal(dec("100")) {
		t.Fatalf("assignments = %+v, want all 100 to B", got)
	}
	if !got[0].RatingAfter.Equal(dec("5")) {
		t.Fatalf("rating after = %s, want 5.00", got[0].RatingAfter)
	}
}

func TestAllocateWhole_AllocationTipsTheChoice(t *testing.T) {
	// After B takes 100 it has allocated 200 and rates 5.00, the same as A.
	sources := brQuotaSources()
	sources[1].Allocated = dec("200")
	got, err := Allocate(sources, dec("100"), false, decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].SourceID != "A" {
		t.Fatalf("tie at 5.00 must go to the lower priority number, got %s", got[0].SourceID)
	}
}

func TestAllocateSplit_EqualisesRatings(t *testing.T) {
	got, err := Allocate(brQuotaSources(), dec("100"), true, decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SourceID != "B" || !got[0].Qty.Equal(dec("100")) {
		t.Fatalf("split assignments = %+v, want B 100 (A is already at the water level)", got)
	}
}

func TestAllocateSplit_SharesAcrossSources(t *testing.T) {
	sources := []QuotaSource{qs("X", "50", "0", "0", 1), qs("Y", "50", "0", "30", 2)}
	got, err := Allocate(sources, dec("100"), true, decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	// Level = (100 + 30) / 100 = 1.3: X takes 65, Y takes 35 after its base 30.
	if len(got) != 2 || !got[0].Qty.Equal(dec("65")) || !got[1].Qty.Equal(dec("35")) {
		t.Fatalf("assignments = %+v, want X 65 and Y 35", got)
	}
	for _, a := range got {
		if !a.RatingAfter.Equal(dec("1.3")) {
			t.Fatalf("%s rating after = %s, want 1.3", a.SourceID, a.RatingAfter)
		}
	}
}

func TestAllocateSplit_MinSplitDropsSmallShares(t *testing.T) {
	sources := []QuotaSource{qs("X", "50", "0", "0", 1), qs("Y", "50", "0", "0", 2)}
	// Equal halves of 50 are below the minimum split of 60, so one source takes all.
	got, err := Allocate(sources, dec("100"), true, dec("60"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Qty.Equal(dec("100")) {
		t.Fatalf("assignments = %+v, want one line of 100", got)
	}
}

func TestAllocateSplit_RequirementBelowMinSplitGoesWhole(t *testing.T) {
	sources := []QuotaSource{qs("X", "50", "0", "0", 1), qs("Y", "50", "10", "0", 2)}
	// 20 is below the minimum split of 60 even as a single line; it must not fail.
	got, err := Allocate(sources, dec("20"), true, dec("60"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SourceID != "X" || !got[0].Qty.Equal(dec("20")) {
		t.Fatalf("assignments = %+v, want 20 to X (the lower rating)", got)
	}
}

func TestAllocate_BlockedSourcesAreSkipped(t *testing.T) {
	sources := brQuotaSources()
	sources[1].Blocked = true // B's trade agreement expired
	got, err := Allocate(sources, dec("100"), false, decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].SourceID != "A" {
		t.Fatalf("blocked B chosen: %+v", got)
	}
}

func TestAllocate_ZeroQuotaIsFallbackOnly(t *testing.T) {
	sources := []QuotaSource{
		qs("Z1", "0", "0", "0", 0),
		qs("Z2", "0", "0", "0", 5),
		qs("P", "10", "0", "0", 9),
	}
	got, err := Allocate(sources, dec("40"), true, decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SourceID != "P" {
		t.Fatalf("quota-0 sources used while P has quota: %+v", got)
	}

	fallback := []QuotaSource{qs("Z1", "0", "0", "0", 5), qs("Z2", "0", "0", "0", 1)}
	got, err = Allocate(fallback, dec("40"), true, decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SourceID != "Z2" || !got[0].Qty.Equal(dec("40")) {
		t.Fatalf("fallback = %+v, want Z2 (lower priority number) taking 40", got)
	}
}

func TestAllocate_NoUsableSource(t *testing.T) {
	sources := brQuotaSources()
	sources[0].Blocked, sources[1].Blocked = true, true
	if _, err := Allocate(sources, dec("10"), true, decimal.Zero); !errors.Is(err, ErrNoUsableSource) {
		t.Fatalf("err = %v, want ErrNoUsableSource", err)
	}
}

func TestAllocate_RejectsBadInput(t *testing.T) {
	if _, err := Allocate(brQuotaSources(), decimal.Zero, false, decimal.Zero); !errors.Is(err, ErrInvalidQuota) {
		t.Fatalf("zero qty: err = %v", err)
	}
	bad := []QuotaSource{qs("A", "-1", "0", "0", 1)}
	if _, err := Allocate(bad, dec("1"), false, decimal.Zero); !errors.Is(err, ErrInvalidQuota) {
		t.Fatalf("negative quota: err = %v", err)
	}
}

func TestArrangementsOverlap(t *testing.T) {
	d := func(s string) time.Time { t, _ := time.Parse("2006-01-02", s); return t }
	tests := []struct {
		name    string
		aFrom   string
		aTo     string
		bFrom   string
		bTo     string
		overlap bool
	}{
		{"disjoint", "2026-01-01", "2026-03-31", "2026-04-01", "2026-06-30", false},
		{"share one day", "2026-01-01", "2026-04-01", "2026-04-01", "2026-06-30", true},
		{"contained", "2026-01-01", "2026-12-31", "2026-03-01", "2026-03-31", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ArrangementsOverlap(d(tt.aFrom), d(tt.aTo), d(tt.bFrom), d(tt.bTo)); got != tt.overlap {
				t.Fatalf("overlap = %v, want %v", got, tt.overlap)
			}
		})
	}
}

// Property: a split conserves quantity, every line is positive, and the water
// level holds: receiving sources end at the same rating and every eligible
// source that receives nothing already sits at or above that level.
func TestProperty_SplitConservesAndEqualisesRatings(t *testing.T) {
	const tol = "0.0001"
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 5).Draw(t, "n")
		sources := make([]QuotaSource, n)
		for i := range sources {
			sources[i] = QuotaSource{
				ID:        string(rune('A' + i)),
				Quota:     decimal.NewFromInt(int64(rapid.IntRange(1, 20).Draw(t, "quota"))),
				Allocated: decimal.NewFromInt(int64(rapid.IntRange(0, 200).Draw(t, "alloc"))),
				BaseQty:   decimal.NewFromInt(int64(rapid.IntRange(0, 50).Draw(t, "base"))),
				Priority:  i,
			}
		}
		qty := decimal.NewFromInt(int64(rapid.IntRange(1, 1000).Draw(t, "qty")))
		got, err := Allocate(sources, qty, true, decimal.Zero)
		if err != nil {
			t.Fatal(err)
		}
		total := decimal.Zero
		receiving := map[string]bool{}
		var level decimal.Decimal
		for i, a := range got {
			if !a.Qty.IsPositive() {
				t.Fatalf("non-positive line %+v", a)
			}
			total = total.Add(a.Qty)
			receiving[a.SourceID] = true
			if i == 0 {
				level = a.RatingAfter
			} else if a.RatingAfter.Sub(level).Abs().GreaterThan(dec(tol)) {
				t.Fatalf("receivers not equal: %s vs %s", a.RatingAfter, level)
			}
		}
		if !total.Equal(qty) {
			t.Fatalf("assigned %s, want %s", total, qty)
		}
		for _, s := range sources {
			if !receiving[s.ID] && Rating(s).LessThan(level.Sub(dec(tol))) {
				t.Fatalf("source %s rating %s below level %s but received nothing", s.ID, Rating(s), level)
			}
		}
	})
}
