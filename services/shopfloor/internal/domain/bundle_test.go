package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func qty(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestSplitBundleHours_BR25_1(t *testing.T) {
	tests := []struct {
		name    string
		total   string
		members []BundleMember
		basis   string
		want    []string
	}{
		{
			name:    "quantity weights 3:1",
			total:   "8",
			members: []BundleMember{{"J1", qty("30")}, {"J2", qty("10")}},
			basis:   SplitByQuantity,
			want:    []string{"6.00", "2.00"},
		},
		{
			name:    "equal basis ignores quantity",
			total:   "8",
			members: []BundleMember{{"J1", qty("30")}, {"J2", qty("10")}},
			basis:   SplitEqual,
			want:    []string{"4.00", "4.00"},
		},
		{
			name:    "equal thirds give the leftover hundredth to the first job",
			total:   "1",
			members: []BundleMember{{"J1", qty("0")}, {"J2", qty("0")}, {"J3", qty("0")}},
			basis:   SplitEqual,
			want:    []string{"0.34", "0.33", "0.33"},
		},
		{
			name:    "no good quantity falls back to equal split",
			total:   "3",
			members: []BundleMember{{"J1", qty("0")}, {"J2", qty("0")}},
			basis:   SplitByQuantity,
			want:    []string{"1.50", "1.50"},
		},
		{
			name:    "zero quantity job receives no hours when others report",
			total:   "5",
			members: []BundleMember{{"J1", qty("4")}, {"J2", qty("0")}},
			basis:   SplitByQuantity,
			want:    []string{"5.00", "0.00"},
		},
		{
			name:    "decimal quantities weigh exactly (2.5 kg : 7.5 kg)",
			total:   "4",
			members: []BundleMember{{"J1", qty("2.5")}, {"J2", qty("7.5")}},
			basis:   SplitByQuantity,
			want:    []string{"1.00", "3.00"},
		},
		{
			name:    "quantities far beyond int64 products do not overflow",
			total:   "23.99",
			members: []BundleMember{{"J1", qty("99999999999999.9999")}, {"J2", qty("99999999999999.9999")}},
			basis:   SplitByQuantity,
			want:    []string{"12.00", "11.99"},
		},
		{
			name:    "total is rounded to hundredths first",
			total:   "2.005",
			members: []BundleMember{{"J1", qty("1")}},
			basis:   SplitByQuantity,
			want:    []string{"2.01"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SplitBundleHours(qty(tc.total), tc.members, tc.basis)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d shares, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i].StringFixed(2) != tc.want[i] {
					t.Fatalf("share %d = %s, want %s", i, got[i].StringFixed(2), tc.want[i])
				}
			}
		})
	}
}

func TestSplitBundleHours_Rejects(t *testing.T) {
	tests := []struct {
		name    string
		total   string
		members []BundleMember
		basis   string
	}{
		{"no jobs", "1", nil, SplitEqual},
		{"negative hours", "-1", []BundleMember{{"J1", qty("1")}}, SplitEqual},
		{"unknown basis", "1", []BundleMember{{"J1", qty("1")}}, "weight"},
		{"negative quantity", "1", []BundleMember{{"J1", qty("-1")}, {"J2", qty("2")}}, SplitByQuantity},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SplitBundleHours(qty(tc.total), tc.members, tc.basis)
			var de *Error
			if !errors.As(err, &de) || de.Code != CodeBundleInvalid {
				t.Fatalf("err = %v, want %s", err, CodeBundleInvalid)
			}
		})
	}
}

func TestSplitBundleHours_SumsExactlyProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		hundredths := rapid.Int64Range(0, 100000).Draw(t, "hundredths")
		n := rapid.IntRange(1, 8).Draw(t, "jobs")
		members := make([]BundleMember, n)
		for i := range members {
			members[i] = BundleMember{JobID: string(rune('A' + i)), QtyGood: decimal.New(rapid.Int64Range(0, 5000000).Draw(t, "qty"), -4)}
		}
		basis := rapid.SampledFrom([]string{SplitByQuantity, SplitEqual}).Draw(t, "basis")
		total := decimal.New(hundredths, -2)
		got, err := SplitBundleHours(total, members, basis)
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		for _, s := range got {
			if s.IsNegative() {
				t.Fatalf("negative share %s", s)
			}
			if s.Exponent() < -2 {
				t.Fatalf("share %s has more than two decimals", s)
			}
			sum = sum.Add(s)
		}
		if !sum.Equal(total) {
			t.Fatalf("shares sum to %s, want %s", sum, total)
		}
	})
}
