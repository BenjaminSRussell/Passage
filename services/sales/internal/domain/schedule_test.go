package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestCheckRelease_AC27_13(t *testing.T) {
	buckets := []ReleaseBucket{
		{Date: "2027-04-01", CumRequired: decimal.NewFromInt(1000)},
		{Date: "2027-04-15", CumRequired: decimal.NewFromInt(1500)},
	}
	t.Run("cumulative mismatch holds the release", func(t *testing.T) {
		res, err := CheckRelease(decimal.NewFromInt(1150), decimal.NewFromInt(1200), decimal.Zero, buckets)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Mismatch || len(res.Lines) != 0 || !res.Difference.Equal(decimal.NewFromInt(-50)) {
			t.Fatalf("held release: %+v", res)
		}
	})
	t.Run("matching cumulative creates open lines", func(t *testing.T) {
		res, err := CheckRelease(decimal.NewFromInt(1200), decimal.NewFromInt(1200), decimal.Zero, buckets)
		if err != nil {
			t.Fatal(err)
		}
		if res.Mismatch || len(res.Lines) != 1 || res.Lines[0].Date != "2027-04-15" || !res.Lines[0].Quantity.Equal(decimal.NewFromInt(300)) {
			t.Fatalf("lines %+v", res.Lines)
		}
		if !res.OpenQuantity.Equal(decimal.NewFromInt(300)) {
			t.Fatalf("open %s, want 300", res.OpenQuantity)
		}
	})
	t.Run("tolerance absorbs a small difference", func(t *testing.T) {
		res, err := CheckRelease(decimal.NewFromInt(1195), decimal.NewFromInt(1200), decimal.NewFromInt(5), buckets)
		if err != nil || res.Mismatch {
			t.Fatalf("tolerance 5 should accept difference -5: %+v %v", res, err)
		}
	})
}

func TestCheckRelease_Rejects(t *testing.T) {
	ok := []ReleaseBucket{{Date: "2027-04-01", CumRequired: decimal.NewFromInt(10)}}
	cases := []struct {
		name    string
		buckets []ReleaseBucket
	}{
		{"bad date", []ReleaseBucket{{Date: "04/01/2027", CumRequired: decimal.NewFromInt(10)}}},
		{"unordered dates", []ReleaseBucket{{Date: "2027-04-02", CumRequired: decimal.NewFromInt(10)}, {Date: "2027-04-01", CumRequired: decimal.NewFromInt(20)}}},
		{"decreasing cumulative", []ReleaseBucket{{Date: "2027-04-01", CumRequired: decimal.NewFromInt(20)}, {Date: "2027-04-02", CumRequired: decimal.NewFromInt(10)}}},
	}
	for _, c := range cases {
		if _, err := CheckRelease(decimal.Zero, decimal.Zero, decimal.Zero, c.buckets); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
	if _, err := CheckRelease(decimal.NewFromInt(-1), decimal.Zero, decimal.Zero, ok); err == nil {
		t.Error("negative cumulative must be rejected")
	}
}

func TestCheckRelease_OpenLinesTotalOpenQuantity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		shipped := decimal.NewFromInt(int64(rapid.IntRange(0, 500).Draw(t, "shipped")))
		n := rapid.IntRange(1, 6).Draw(t, "buckets")
		cum := int64(0)
		buckets := make([]ReleaseBucket, 0, n)
		for i := 0; i < n; i++ {
			cum += int64(rapid.IntRange(0, 200).Draw(t, "step"))
			buckets = append(buckets, ReleaseBucket{Date: "2027-04-" + twoDigits(i+1), CumRequired: decimal.NewFromInt(cum)})
		}
		res, err := CheckRelease(shipped, shipped, decimal.Zero, buckets)
		if err != nil {
			t.Fatal(err)
		}
		var total decimal.Decimal
		for _, l := range res.Lines {
			if !l.Quantity.IsPositive() {
				t.Fatalf("non-positive schedule line %+v", l)
			}
			total = total.Add(l.Quantity)
		}
		want := decimal.NewFromInt(cum).Sub(shipped)
		if want.IsNegative() {
			want = decimal.Zero
		}
		if !total.Equal(want) || !res.OpenQuantity.Equal(want) {
			t.Fatalf("lines total %s, open %s, want %s", total, res.OpenQuantity, want)
		}
	})
}
