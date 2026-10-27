package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

var (
	dayZero   = time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	windowDur = DefaultSupplyWindow
)

func day(n int) time.Time { return dayZero.Add(time.Duration(n) * 24 * time.Hour) }

func supplyAt(qty string) Supply {
	return Supply{ID: "ASN-PAL-1", ItemID: "BOLT-M8", Warehouse: "WH1", Qty: dec(qty), ReceivedAt: dayZero, Quality: QualityAccepted}
}

func demand(line string, shipDay int, open string) Demand {
	return Demand{LineID: line, ItemID: "BOLT-M8", Warehouse: "WH1", ShipDate: day(shipDay), ReleasedAt: dayZero, OpenQty: dec(open)}
}

func TestPlanReceipt_AC31_10CrossDocksAndRemainderPutsAway(t *testing.T) {
	// AC-31.10: a 40 EA line shipping tomorrow, a 100 EA pallet received today.
	plan, err := PlanReceipt(supplyAt("100"), []Demand{demand("SO-1-10", 1, "40")},
		CrossDockOptions{Opportunistic: true, Window: windowDur})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Allocations) != 1 || plan.Allocations[0].LineID != "SO-1-10" || !plan.Allocations[0].Qty.Equal(dec("40")) || plan.Allocations[0].Planned {
		t.Fatalf("allocations = %+v, want 40 to SO-1-10 opportunistic", plan.Allocations)
	}
	if !plan.CrossDocked.Equal(dec("40")) || !plan.Remainder.Equal(dec("60")) || plan.Held {
		t.Errorf("cross-docked %s remainder %s held %v, want 40 / 60 / false", plan.CrossDocked, plan.Remainder, plan.Held)
	}
}

func TestPlanReceipt_Table(t *testing.T) {
	tests := []struct {
		name      string
		supply    Supply
		demands   []Demand
		opt       CrossDockOptions
		wantLines []string // allocated line ids in order
		wantQty   []string
		wantRem   string
		wantHeld  bool
	}{
		{
			name:      "planned link is consumed before opportunistic demand",
			supply:    supplyAt("50"),
			demands:   []Demand{demand("L-1", 1, "40"), demand("L-2", 2, "30")},
			opt:       CrossDockOptions{Opportunistic: true, Window: windowDur, Planned: []PlannedLink{{SupplyID: "ASN-PAL-1", LineID: "L-2", Qty: dec("30")}}},
			wantLines: []string{"L-2", "L-1"}, wantQty: []string{"30", "20"}, wantRem: "0",
		},
		{
			name:      "opportunistic off allocates only planned links",
			supply:    supplyAt("50"),
			demands:   []Demand{demand("L-1", 1, "40")},
			opt:       CrossDockOptions{Window: windowDur},
			wantLines: nil, wantRem: "50",
		},
		{
			name:      "demand beyond the supply window is not allocated",
			supply:    supplyAt("10"),
			demands:   []Demand{demand("L-1", 5, "40")},
			opt:       CrossDockOptions{Opportunistic: true, Window: windowDur},
			wantLines: nil, wantRem: "10",
		},
		{
			name:   "order priority beats ship date",
			supply: supplyAt("10"),
			demands: []Demand{
				{LineID: "L-1", ItemID: "BOLT-M8", Warehouse: "WH1", ShipDate: day(0), OrderPriority: 2, OpenQty: dec("10")},
				{LineID: "L-2", ItemID: "BOLT-M8", Warehouse: "WH1", ShipDate: day(1), OrderPriority: 1, OpenQty: dec("10")},
			},
			opt:       CrossDockOptions{Priority: PriorityOrder, Opportunistic: true, Window: windowDur},
			wantLines: []string{"L-2"}, wantQty: []string{"10"}, wantRem: "0",
		},
		{
			name:   "FIFO release orders by release time",
			supply: supplyAt("10"),
			demands: []Demand{
				{LineID: "L-late", ItemID: "BOLT-M8", Warehouse: "WH1", ShipDate: day(0), ReleasedAt: day(-1), OpenQty: dec("10")},
				{LineID: "L-early", ItemID: "BOLT-M8", Warehouse: "WH1", ShipDate: day(1), ReleasedAt: day(-3), OpenQty: dec("10")},
			},
			opt:       CrossDockOptions{Priority: PriorityFIFORelease, Opportunistic: true, Window: windowDur},
			wantLines: []string{"L-early"}, wantQty: []string{"10"}, wantRem: "0",
		},
		{
			name:      "ship-date ties break by line id",
			supply:    supplyAt("10"),
			demands:   []Demand{demand("L-b", 1, "10"), demand("L-a", 1, "10")},
			opt:       CrossDockOptions{Opportunistic: true, Window: windowDur},
			wantLines: []string{"L-a"}, wantQty: []string{"10"}, wantRem: "0",
		},
		{
			name:      "held supply is neither cross-docked nor put away",
			supply:    Supply{ID: "ASN-PAL-1", ItemID: "BOLT-M8", Warehouse: "WH1", Qty: dec("10"), ReceivedAt: dayZero, Quality: "BLOCKED"},
			demands:   []Demand{demand("L-1", 1, "10")},
			opt:       CrossDockOptions{Opportunistic: true, Window: windowDur},
			wantLines: nil, wantRem: "0", wantHeld: true,
		},
		{
			name:      "other items and warehouses never match",
			supply:    supplyAt("10"),
			demands:   []Demand{{LineID: "L-1", ItemID: "NUT-M8", Warehouse: "WH1", ShipDate: day(1), OpenQty: dec("10")}, {LineID: "L-2", ItemID: "BOLT-M8", Warehouse: "WH2", ShipDate: day(1), OpenQty: dec("10")}},
			opt:       CrossDockOptions{Opportunistic: true, Window: windowDur},
			wantLines: nil, wantRem: "10",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := PlanReceipt(tc.supply, tc.demands, tc.opt)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Held != tc.wantHeld {
				t.Fatalf("held = %v, want %v", plan.Held, tc.wantHeld)
			}
			if len(plan.Allocations) != len(tc.wantLines) {
				t.Fatalf("allocations = %+v, want lines %v", plan.Allocations, tc.wantLines)
			}
			for i, a := range plan.Allocations {
				if a.LineID != tc.wantLines[i] || !a.Qty.Equal(dec(tc.wantQty[i])) {
					t.Errorf("allocation %d = %s %s, want %s %s", i, a.LineID, a.Qty, tc.wantLines[i], tc.wantQty[i])
				}
			}
			if !plan.Remainder.Equal(dec(tc.wantRem)) {
				t.Errorf("remainder = %s, want %s", plan.Remainder, tc.wantRem)
			}
		})
	}
}

func TestPlanReceipt_QualityHoldWarningUsesCatalogCode(t *testing.T) {
	plan, _ := PlanReceipt(Supply{ID: "S", Qty: dec("1"), Quality: "BLOCKED"}, nil, CrossDockOptions{})
	if len(plan.Warnings) != 1 || !strings.HasPrefix(plan.Warnings[0], CodeQualityHold) {
		t.Errorf("warnings = %v, want %s", plan.Warnings, CodeQualityHold)
	}
}

func TestPlanReceipt_RejectsBadInput(t *testing.T) {
	dup := []Demand{demand("L-1", 1, "1"), demand("L-1", 2, "1")}
	tests := []struct {
		name    string
		supply  Supply
		demands []Demand
		opt     CrossDockOptions
	}{
		{"zero supply", supplyAt("0"), nil, CrossDockOptions{}},
		{"unknown priority", supplyAt("1"), nil, CrossDockOptions{Priority: "RANDOM"}},
		{"negative window", supplyAt("1"), nil, CrossDockOptions{Window: -time.Hour}},
		{"window over 336 h", supplyAt("1"), nil, CrossDockOptions{Window: MaxSupplyWindow + time.Hour}},
		{"duplicate demand line", supplyAt("1"), dup, CrossDockOptions{Opportunistic: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := PlanReceipt(tc.supply, tc.demands, tc.opt); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestPlanReceipt_PropertyConservesQuantityAndNeverOverAllocates(t *testing.T) {
	// BR-31.11: allocation is greedy, conserves the receipt, never exceeds open
	// demand, and leaves put-away only when every in-window demand is exhausted.
	rapid.Check(t, func(t *rapid.T) {
		recv := decimal.NewFromInt(rapid.Int64Range(1, 100).Draw(t, "recv"))
		n := rapid.IntRange(0, 6).Draw(t, "demands")
		demands := make([]Demand, 0, n)
		inWindowOpen := decimal.Zero
		for i := 0; i < n; i++ {
			open := decimal.NewFromInt(rapid.Int64Range(0, 50).Draw(t, "open"))
			shift := rapid.IntRange(-2, 4).Draw(t, "shift")
			d := Demand{
				LineID: string(rune('a' + i)), ItemID: "BOLT-M8", Warehouse: "WH1",
				ShipDate: day(shift), OrderPriority: rapid.IntRange(1, 3).Draw(t, "prio"),
				ReleasedAt: day(rapid.IntRange(-5, 0).Draw(t, "released")), OpenQty: open,
			}
			demands = append(demands, d)
			if open.IsPositive() && !d.ShipDate.After(dayZero.Add(windowDur)) {
				inWindowOpen = inWindowOpen.Add(open)
			}
		}
		prio := rapid.SampledFrom([]DemandPriority{PrioritySHIPDate, PriorityOrder, PriorityFIFORelease}).Draw(t, "priority")
		s := Supply{ID: "S", ItemID: "BOLT-M8", Warehouse: "WH1", Qty: recv, ReceivedAt: dayZero, Quality: QualityAccepted}
		plan, err := PlanReceipt(s, demands, CrossDockOptions{Priority: prio, Opportunistic: true, Window: windowDur})
		if err != nil {
			t.Fatal(err)
		}
		if !plan.CrossDocked.Add(plan.Remainder).Equal(recv) {
			t.Fatalf("receipt %s != cross-docked %s + remainder %s", recv, plan.CrossDocked, plan.Remainder)
		}
		open := map[string]decimal.Decimal{}
		for _, d := range demands {
			open[d.LineID] = d.OpenQty
		}
		total := decimal.Zero
		for _, a := range plan.Allocations {
			if !a.Qty.IsPositive() || a.Qty.GreaterThan(open[a.LineID]) {
				t.Fatalf("allocation %+v exceeds open demand %s", a, open[a.LineID])
			}
			open[a.LineID] = open[a.LineID].Sub(a.Qty)
			total = total.Add(a.Qty)
		}
		if !total.Equal(plan.CrossDocked) {
			t.Fatalf("allocations sum %s != cross-docked %s", total, plan.CrossDocked)
		}
		if plan.Remainder.IsPositive() && !total.Equal(inWindowOpen) {
			t.Fatalf("put-away remains %s while in-window demand %s is not exhausted (allocated %s)", plan.Remainder, inWindowOpen, total)
		}
	})
}
