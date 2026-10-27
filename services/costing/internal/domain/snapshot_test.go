package domain

import (
	"testing"
)

// Two lines aged as of 2026-02-19: the FR-20.1 example under FIFO (50 units of
// the 2026-01-20 receipt left, 30 days old, 600.00) and a 10-unit receipt of
// 2026-01-01 (49 days old, 50.00). The snapshot sums the buckets and ties.
func TestBuildSnapshot_SumsLinesAndTies(t *testing.T) {
	asOf := jan(31).AddDate(0, 0, 19)
	lines := []SnapshotLine{
		{ProductID: "p1", SiteID: "s1", Txns: fr201()},
		{ProductID: "p2", SiteID: "s1", Txns: []Txn{{ID: "R3", Kind: Receipt, Qty: dec("10"), Value: dec("50.00"), FinancialDate: jan(1)}}},
	}
	snap, err := BuildSnapshot(lines, asOf, DefaultBuckets(), Options{Method: FIFO, Scale: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"0-30": "600.00", "31-90": "50.00", "91-180": "0.00", "181-365": "0.00", "> 365": "0.00"}
	for _, b := range snap.Buckets {
		if got := b.Value.StringFixed(2); got != want[b.Label] {
			t.Errorf("bucket %s = %s, want %s", b.Label, got, want[b.Label])
		}
	}
	if snap.TotalValue.StringFixed(2) != "650.00" || snap.RemainingValue.StringFixed(2) != "650.00" || !snap.Ties {
		t.Errorf("total %s remaining %s ties %v, want 650.00 650.00 true", snap.TotalValue, snap.RemainingValue, snap.Ties)
	}
	if !snap.TotalQty.Equal(dec("60")) || len(snap.Lines) != 2 || !snap.Lines[0].RemainingValue.Equal(dec("600")) {
		t.Errorf("qty %s lines %+v", snap.TotalQty, snap.Lines)
	}
}

// A split product (AC-20.12 data) keeps its per-type aging on the line, each
// type at its own running average, and the line still ties to 1,700.00.
func TestBuildSnapshot_SplitValuationPerLine(t *testing.T) {
	snap, err := BuildSnapshot([]SnapshotLine{{ProductID: "p", SiteID: "s", Txns: splitFixture()}}, jan(31),
		DefaultBuckets(), Options{Method: WeightedAvgRunning, Scale: 2})
	if err != nil {
		t.Fatal(err)
	}
	line := snap.Lines[0]
	if len(line.PerType) != 2 || !line.Ties || line.RemainingValue.StringFixed(2) != "1700.00" {
		t.Fatalf("line = %+v", line)
	}
	byType := map[string]string{}
	for _, ta := range line.PerType {
		byType[ta.ValuationType] = ta.Report.TotalValue.StringFixed(2)
	}
	if byType["DOMESTIC"] != "1000.00" || byType["IMPORT"] != "700.00" {
		t.Errorf("per type = %v, want DOMESTIC 1000.00 and IMPORT 700.00", byType)
	}
}

func TestBuildSnapshot_Refusals(t *testing.T) {
	receipt := func(on int) []Txn {
		return []Txn{{ID: "R", Kind: Receipt, Qty: dec("1"), Value: dec("1"), FinancialDate: jan(on)}}
	}
	tests := []struct {
		name  string
		lines []SnapshotLine
		want  string
	}{
		{"no lines", nil, CodeInvalidTxn},
		{"same product and site twice", []SnapshotLine{{ProductID: "p", SiteID: "s", Txns: receipt(1)}, {ProductID: "p", SiteID: "s", Txns: receipt(2)}}, CodeInvalidTxn},
		{"transaction after the as-of date", []SnapshotLine{{ProductID: "p", SiteID: "s", Txns: receipt(30)}}, CodeInvalidTxn},
		{"untyped issue of a split product", []SnapshotLine{{ProductID: "p", SiteID: "s", Txns: append(splitFixture()[:2],
			Txn{ID: "I9", Kind: Issue, Qty: dec("1"), FinancialDate: jan(25)})}}, CodeValuationTypeRequired},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildSnapshot(tc.lines, jan(25), DefaultBuckets(), Options{Method: FIFO, Scale: 2})
			if got := domainCode(err); got != tc.want {
				t.Fatalf("code %q (%v), want %s", got, err, tc.want)
			}
		})
	}
}
