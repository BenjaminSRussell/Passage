package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

var planDay = time.Date(2027, 3, 10, 0, 0, 0, 0, time.UTC)

func slipLine(id string) SlipLine {
	return SlipLine{
		Source: Source{PackingSlipLineID: id, OrderLineID: "ol-" + id, ProductID: "p-" + id, PriceExplainRef: "px-" + id},
		Line: Line{Description: "item " + id, Qty: decimal.NewFromInt(2), UnitPrice: decimal.NewFromInt(10),
			DiscountPct: decimal.Zero, RevenueAccount: "4000", TaxCode: "DE19"},
	}
}

func slip(id, order string, date time.Time, lineIDs ...string) Slip {
	s := Slip{ID: id, OrderID: order, Date: date, Charges: decimal.Zero}
	for _, l := range lineIDs {
		s.Lines = append(s.Lines, slipLine(l))
	}
	return s
}

func TestPlanProposals_GroupingAndRefusals(t *testing.T) {
	tests := []struct {
		name        string
		slips       []Slip
		consolidate bool
		maxLines    int
		wantTypes   []string
		wantLines   []int
		wantCode    string
	}{
		{
			name:      "one invoice per order by default, slips of one order together",
			slips:     []Slip{slip("s1", "o1", planDay, "a", "b"), slip("s2", "o2", planDay, "c"), slip("s3", "o1", planDay, "d")},
			maxLines:  2000,
			wantTypes: []string{TypeShipment, TypeShipment}, wantLines: []int{3, 1},
		},
		{
			name:        "AC-29.5 three slips of one account consolidate to one invoice",
			slips:       []Slip{slip("s1", "o1", planDay, "a"), slip("s2", "o2", planDay, "b"), slip("s3", "o3", planDay, "c")},
			consolidate: true, maxLines: 2000,
			wantTypes: []string{TypeConsolidated}, wantLines: []int{3},
		},
		{
			name:        "consolidating slips of a single order is a shipment invoice",
			slips:       []Slip{slip("s1", "o1", planDay, "a"), slip("s2", "o1", planDay, "b")},
			consolidate: true, maxLines: 2000,
			wantTypes: []string{TypeShipment}, wantLines: []int{2},
		},
		{
			name:        "BR-29.5 auto split at slip boundaries above the line limit",
			slips:       []Slip{slip("s1", "o1", planDay, "a", "b"), slip("s2", "o2", planDay, "c"), slip("s3", "o3", planDay, "d", "e")},
			consolidate: true, maxLines: 3,
			wantTypes: []string{TypeConsolidated, TypeShipment}, wantLines: []int{3, 2},
		},
		{
			name:     "BIL-0007 a slip alone above the line limit",
			slips:    []Slip{slip("s1", "o1", planDay, "a", "b", "c")},
			maxLines: 2, wantCode: CodeTooManyLines,
		},
		{
			name:     "invoice date may follow the slip date",
			slips:    []Slip{slip("s1", "o1", planDay.AddDate(0, 0, -5), "a")},
			maxLines: 2000, wantTypes: []string{TypeShipment}, wantLines: []int{1},
		},
		{
			name:     "BIL-0006 invoice date before the slip date",
			slips:    []Slip{slip("s1", "o1", planDay, "a"), slip("s2", "o1", planDay.AddDate(0, 0, 1), "b")},
			maxLines: 2000, wantCode: CodeDateBeforeSlip,
		},
		{
			name:     "a slip line may not be invoiced twice in one request",
			slips:    []Slip{slip("s1", "o1", planDay, "a"), slip("s2", "o1", planDay, "a")},
			maxLines: 2000, wantCode: "COM-0001",
		},
		{
			name:     "a slip may not be named twice",
			slips:    []Slip{slip("s1", "o1", planDay, "a"), slip("s1", "o1", planDay, "b")},
			maxLines: 2000, wantCode: "COM-0001",
		},
		{
			name:     "a slip without lines",
			slips:    []Slip{slip("s1", "o1", planDay)},
			maxLines: 2000, wantCode: "COM-0001",
		},
		{
			name:     "no slips",
			maxLines: 2000, wantCode: "COM-0001",
		},
		{
			name: "BIL-0004 a slip line without revenue account",
			slips: func() []Slip {
				s := slip("s1", "o1", planDay, "a")
				s.Lines[0].Line.RevenueAccount = ""
				return []Slip{s}
			}(),
			maxLines: 2000, wantCode: "BIL-0004",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PlanProposals(tc.slips, tc.consolidate, planDay, tc.maxLines)
			if tc.wantCode != "" {
				var de *Error
				if !errors.As(err, &de) || de.Code != tc.wantCode {
					t.Fatalf("error %v, want %s", err, tc.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.wantTypes) {
				t.Fatalf("%d invoices, want %d", len(got), len(tc.wantTypes))
			}
			for i, p := range got {
				if p.Type != tc.wantTypes[i] || len(p.Lines) != tc.wantLines[i] {
					t.Errorf("invoice %d: type %s with %d lines, want %s with %d", i, p.Type, len(p.Lines), tc.wantTypes[i], tc.wantLines[i])
				}
			}
		})
	}
}

func TestPlanProposals_SourcesAndCharges(t *testing.T) {
	early := planDay.AddDate(0, 0, -2)
	s1 := slip("s1", "o1", early, "a", "b")
	s1.Charges = decimal.RequireFromString("12.50")
	s2 := slip("s2", "o2", planDay, "c")
	s2.Charges = decimal.RequireFromString("7.50")
	got, err := PlanProposals([]Slip{s1, s2}, true, planDay, 2000)
	if err != nil {
		t.Fatal(err)
	}
	p := got[0]
	if !p.Charges.Equal(decimal.NewFromInt(20)) {
		t.Errorf("charges %s, want the slips' 20", p.Charges)
	}
	want := []Source{
		{PackingSlipID: "s1", PackingSlipLineID: "a", PackingSlipDate: early, OrderID: "o1", OrderLineID: "ol-a", ProductID: "p-a", PriceExplainRef: "px-a"},
		{PackingSlipID: "s1", PackingSlipLineID: "b", PackingSlipDate: early, OrderID: "o1", OrderLineID: "ol-b", ProductID: "p-b", PriceExplainRef: "px-b"},
		{PackingSlipID: "s2", PackingSlipLineID: "c", PackingSlipDate: planDay, OrderID: "o2", OrderLineID: "ol-c", ProductID: "p-c", PriceExplainRef: "px-c"},
	}
	for i, src := range p.Sources {
		if src != want[i] {
			t.Errorf("source %d = %+v, want %+v", i, src, want[i])
		}
		if p.Lines[i].No != i+1 {
			t.Errorf("line %d numbered %d", i, p.Lines[i].No)
		}
	}
}

// Every slip line lands on exactly one invoice, no invoice exceeds the limit,
// and lines keep their slip and order (BR-29.5).
func TestPlanProposals_EveryLineOnceProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxLines := rapid.IntRange(1, 6).Draw(t, "maxLines")
		consolidate := rapid.Bool().Draw(t, "consolidate")
		nSlips := rapid.IntRange(1, 8).Draw(t, "slips")
		var slips []Slip
		total := 0
		for i := 0; i < nSlips; i++ {
			n := rapid.IntRange(1, maxLines).Draw(t, fmt.Sprintf("lines%d", i))
			order := fmt.Sprintf("o%d", rapid.IntRange(0, 3).Draw(t, fmt.Sprintf("order%d", i)))
			s := slip(fmt.Sprintf("s%d", i), order, planDay)
			for j := 0; j < n; j++ {
				s.Lines = append(s.Lines, slipLine(fmt.Sprintf("s%d-l%d", i, j)))
			}
			total += n
			slips = append(slips, s)
		}
		got, err := PlanProposals(slips, consolidate, planDay, maxLines)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, p := range got {
			if len(p.Lines) == 0 || len(p.Lines) > maxLines {
				t.Fatalf("invoice with %d lines, limit %d", len(p.Lines), maxLines)
			}
			orders := map[string]bool{}
			for _, src := range p.Sources {
				if seen[src.PackingSlipLineID] {
					t.Fatalf("slip line %s on two invoices", src.PackingSlipLineID)
				}
				seen[src.PackingSlipLineID] = true
				orders[src.OrderID] = true
			}
			if !consolidate && len(orders) != 1 {
				t.Fatalf("an unconsolidated invoice spans %d orders", len(orders))
			}
		}
		if len(seen) != total {
			t.Fatalf("%d of %d slip lines invoiced", len(seen), total)
		}
	})
}
