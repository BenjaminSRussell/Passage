package domain

import (
	"testing"
	"time"
)

func TestSegmentValueAndParse(t *testing.T) {
	tests := []struct {
		typ  SegmentType
		ref  string
		want string
		ok   bool
	}{
		{SegmentNone, "", "", true},
		{"", "", "", true},
		{SegmentSalesOrder, " SO-1 ", "SALES_ORDER:SO-1", true},
		{SegmentProject, "P-7", "PROJECT:P-7", true},
		{SegmentNone, "SO-1", "", false},
		{SegmentProject, "", "", false},
		{"WBS", "P-7", "", false},
		{SegmentProject, string(make([]byte, 60)), "", false},
	}
	for _, tc := range tests {
		got, err := SegmentValue(tc.typ, tc.ref)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("SegmentValue(%q, %q) = %q, %v; want %q ok=%v", tc.typ, tc.ref, got, err, tc.want, tc.ok)
		}
	}
	if typ, ref, err := ParseSegment("PROJECT:P-7"); err != nil || typ != SegmentProject || ref != "P-7" {
		t.Fatalf("parse: %s %s %v", typ, ref, err)
	}
	for _, bad := range []string{"P-7", "PROJECT:", "OTHER:X", "PROJECT: P"} {
		if _, err := (Dimensions{DimSite: "S1", DimSegment: bad}).Normalize(); err == nil {
			t.Errorf("segment %q must be rejected by Normalize", bad)
		}
	}
}

// Adding the segment dimension must not move existing dimension-set keys.
func TestSegmentDoesNotChangeExistingHashes(t *testing.T) {
	d, _ := Dimensions{DimSite: "S1", DimWarehouse: "W1", DimOwner: "V1"}.Normalize()
	if got, want := d.Canonical(), "site=2:S1;warehouse=2:W1;owner=2:V1;"; got != want {
		t.Fatalf("canonical %q, want %q", got, want)
	}
	s, _ := Dimensions{DimSite: "S1", DimSegment: "PROJECT:P1"}.Normalize()
	if got, want := s.Canonical(), "site=2:S1;segment=10:PROJECT:P1;"; got != want {
		t.Fatalf("canonical %q, want %q", got, want)
	}
}

func TestSegmentEligibility(t *testing.T) {
	tests := []struct {
		stock, demand string
		fallback      bool
		want          bool
	}{
		{"", "", false, true},
		{"PROJECT:P1", "PROJECT:P1", false, true},
		{"", "PROJECT:P1", true, true},
		{"", "PROJECT:P1", false, false},
		{"PROJECT:P2", "PROJECT:P1", true, false},
		{"PROJECT:P1", "", true, false},
	}
	for _, tc := range tests {
		if got := SegmentEligible(tc.stock, tc.demand, tc.fallback); got != tc.want {
			t.Errorf("SegmentEligible(%q, %q, %v) = %v, want %v", tc.stock, tc.demand, tc.fallback, got, tc.want)
		}
		if err := CheckSegment(tc.stock, tc.demand, tc.fallback); (err == nil) != tc.want || (err != nil && codeOf(err) != CodeSegment) {
			t.Errorf("CheckSegment(%q, %q, %v) = %v", tc.stock, tc.demand, tc.fallback, err)
		}
	}
}

// BR-16.17: a segmented demand takes its own segment first, then unsegmented
// stock with fallback, never another segment; an unsegmented demand never
// takes segmented stock.
func TestPlanDemandPolicyRespectsSegments(t *testing.T) {
	day := func(n int) time.Time { return time.Date(2026, 1, n, 0, 0, 0, 0, time.UTC) }
	batches := []BatchStock{
		{Key: "free-old", ReceiptDate: day(1), Available: d("5"), Status: StockAvailable},
		{Key: "p1", ReceiptDate: day(2), Available: d("4"), Status: StockAvailable, Segment: "PROJECT:P1"},
		{Key: "p2", ReceiptDate: day(1), Available: d("100"), Status: StockAvailable, Segment: "PROJECT:P2"},
		{Key: "free-new", ReceiptDate: day(3), Available: d("5"), Status: StockAvailable},
	}
	keys := func(s Selection) []string {
		var out []string
		for _, a := range s.Allocations {
			out = append(out, a.Key+"="+a.Qty.String())
		}
		return out
	}
	sel, err := PlanDemandPolicy(PolicyFIFO, batches, d("7"), day(10), 0, false, "PROJECT:P1", true)
	if err != nil || len(sel.Allocations) != 2 || keys(sel)[0] != "p1=4" || keys(sel)[1] != "free-old=3" {
		t.Fatalf("own segment first, then unsegmented FIFO: %v %v", keys(sel), err)
	}
	if _, err := PlanDemandPolicy(PolicyFIFO, batches, d("7"), day(10), 0, false, "PROJECT:P1", false); codeOf(err) != CodeReserveShortfall {
		t.Fatalf("without fallback only 4 are eligible: %v, want %s", err, CodeReserveShortfall)
	}
	sel, err = PlanDemandPolicy(PolicyFIFO, batches, d("10"), day(10), 0, false, "", true)
	if err != nil || len(sel.Allocations) != 2 || keys(sel)[0] != "free-old=5" || keys(sel)[1] != "free-new=5" {
		t.Fatalf("unsegmented demand takes unsegmented stock only: %v %v", keys(sel), err)
	}
	if _, err := PlanDemandPolicy(PolicyFIFO, batches, d("11"), day(10), 0, false, "", true); codeOf(err) != CodeReserveShortfall {
		t.Fatalf("segmented stock must never serve an unsegmented demand: %v", err)
	}
	sel, err = PlanDemandPolicy(PolicyFIFO, batches, d("200"), day(10), 0, true, "PROJECT:P3", true)
	if err != nil || !sel.Shortfall.Equal(d("190")) {
		t.Fatalf("another project's stock is never taken: %v shortfall %s %v", keys(sel), sel.Shortfall, err)
	}
}
