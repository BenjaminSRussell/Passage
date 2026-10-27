package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestCovers_BR44_14(t *testing.T) {
	start := time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC)
	base := WarrantyWindow{Status: "ACTIVE", StartsOn: start, Months: 24, PartsCovered: true,
		Exclusions: []string{"PAINT"}}
	tests := []struct {
		name   string
		w      WarrantyWindow
		at     time.Time
		jobTyp string
		want   bool
	}{
		{"inside period parts only", base, time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC), "LUBE", true},
		{"before start", base, time.Date(2025, 10, 31, 23, 0, 0, 0, time.UTC), "LUBE", false},
		{"exactly at end is outside", base, time.Date(2027, 11, 1, 0, 0, 0, 0, time.UTC), "LUBE", false},
		{"last instant inside", base, time.Date(2027, 10, 31, 23, 59, 0, 0, time.UTC), "LUBE", true},
		{"excluded job type", base, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "PAINT", false},
		{"voided", func() WarrantyWindow { w := base; w.Status = "VOIDED"; return w }(),
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "LUBE", false},
		{"no coverage flags", func() WarrantyWindow { w := base; w.PartsCovered = false; return w }(),
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "LUBE", false},
		{"labor only covers", func() WarrantyWindow { w := base; w.PartsCovered = false; w.LaborCovered = true; return w }(),
			time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "LUBE", true},
		{"empty job type never excluded", base, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Covers(tc.w, tc.at, tc.jobTyp); got != tc.want {
				t.Fatalf("Covers = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidateWarranty(t *testing.T) {
	ok := WarrantyWindow{Months: 12, PartsCovered: true}
	tests := []struct {
		name string
		w    WarrantyWindow
		err  bool
	}{
		{"valid", ok, false},
		{"zero months", WarrantyWindow{Months: 0, PartsCovered: true}, true},
		{"too many months", WarrantyWindow{Months: 241, PartsCovered: true}, true},
		{"nothing covered", WarrantyWindow{Months: 12}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateWarranty(tc.w); (err != nil) != tc.err {
				t.Fatalf("err = %v, want error %v", err, tc.err)
			}
		})
	}
	if !ValidStartBasis("PURCHASE") || !ValidStartBasis("INSTALL") || ValidStartBasis("LEASE") {
		t.Fatal("start basis set wrong")
	}
}

// AC-44.13: a parts-only warranty claims the parts amount and none of the labor.
func TestClaimFor(t *testing.T) {
	parts, labor := decimal.RequireFromString("350.004"), decimal.RequireFromString("360")
	tests := []struct {
		name               string
		partsOn, laborOn   bool
		wantParts, wantLab string
	}{
		{"parts only", true, false, "350", "0"},
		{"labor only", false, true, "0", "360"},
		{"both", true, true, "350", "360"},
		{"neither", false, false, "0", "0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := ClaimFor(tc.partsOn, tc.laborOn, parts, labor)
			if !c.Parts.Equal(decimal.RequireFromString(tc.wantParts)) || !c.Labor.Equal(decimal.RequireFromString(tc.wantLab)) {
				t.Fatalf("claim = %s/%s, want %s/%s", c.Parts, c.Labor, tc.wantParts, tc.wantLab)
			}
			if !c.Total().Equal(c.Parts.Add(c.Labor)) {
				t.Fatalf("total %s", c.Total())
			}
		})
	}
}
