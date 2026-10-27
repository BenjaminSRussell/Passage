package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// BR-21.1 Released → Started: only start-flushed lines are issued, for what
// is still unconsumed, rounded up to the quantity scale and never negative.
func TestStartIssueQty(t *testing.T) {
	d := decimal.RequireFromString
	tests := []struct {
		name               string
		flush              Flushing
		required, consumed string
		want               string
	}{
		{"start line issues the full requirement", FlushStart, "315", "0", "315"},
		{"start line issues only the remainder", FlushStart, "300", "120", "180"},
		{"start line already issued issues nothing", FlushStart, "300", "300", "0"},
		{"start line over-issued never goes negative", FlushStart, "300", "310", "0"},
		{"start line remainder rounds up to the quantity scale", FlushStart, "10.0000005", "0", "10.000001"},
		{"finish line waits for report as finished", FlushFinish, "300", "0", "0"},
		{"manual line waits for a picking list", FlushManual, "300", "0", "0"},
		{"available-at-location line waits for a picking list", FlushAvailable, "300", "0", "0"},
	}
	for _, tc := range tests {
		got := StartIssueQty(tc.flush, d(tc.required), d(tc.consumed))
		if !got.Equal(d(tc.want)) {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

// After the Start issue a start-flushed line is consumed exactly to its
// requirement (or stays over it); other lines are untouched.
func TestStartIssueQty_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		required := decimal.New(rapid.Int64Range(0, 10_000_000).Draw(t, "required"), -QtyScale)
		consumed := decimal.New(rapid.Int64Range(0, 12_000_000).Draw(t, "consumed"), -QtyScale)
		flush := rapid.SampledFrom([]Flushing{FlushStart, FlushFinish, FlushManual, FlushAvailable}).Draw(t, "flush")
		got := StartIssueQty(flush, required, consumed)
		if got.IsNegative() {
			t.Fatalf("issue %s is negative", got)
		}
		if flush != FlushStart {
			if !got.IsZero() {
				t.Fatalf("%s line issued %s at Start", flush, got)
			}
			return
		}
		if want := decimal.Max(required, consumed); !consumed.Add(got).Equal(want) {
			t.Fatalf("consumed %s + issue %s, want %s", consumed, got, want)
		}
	})
}
