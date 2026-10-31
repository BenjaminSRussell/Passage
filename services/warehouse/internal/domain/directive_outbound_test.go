package domain

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
)

func outboundDirectives() []Directive {
	return []Directive{
		{ID: "DIR-P20", Sequence: 20, WorkType: "SALES_PICK", Direction: DirectionPick, Warehouse: "WH1",
			Bands: []Band{{Seq: 1, FromQty: dec("0"), Actions: []Action{{Seq: 1, Strategy: StrategyConsolidate}}}}},
		{ID: "DIR-P10", Sequence: 10, WorkType: "SALES_PICK", Direction: DirectionPick, Warehouse: "WH1",
			Query: Query{"item_group": "FASTENERS"},
			Bands: []Band{
				{Seq: 1, FromQty: dec("0"), ToQty: decPtr("100"), Actions: []Action{{Seq: 1, Strategy: StrategyConsolidate, Zone: "FWD"}}},
				{Seq: 2, FromQty: dec("100"), ToQty: decPtr("1200"), Actions: []Action{
					{Seq: 1, Strategy: StrategyConsolidate, Zone: "CASE"}, {Seq: 2, Strategy: StrategyConsolidate, Zone: "RESERVE"}}},
				{Seq: 3, FromQty: dec("1200"), Actions: []Action{
					{Seq: 1, Strategy: StrategyConsolidate, Zone: "RESERVE"}, {Seq: 2, Strategy: StrategyConsolidate, Zone: "OVERFLOW"}}},
			}},
	}
}

// TestEvaluate_OutboundPickFixture is the outbound half of Proof AC1: 100 pick
// scenarios (10 quantities x 10 stock layouts of the locations F, C, R and O).
// The expectation is derived by hand from the directives, not from the engine:
// DIR-P10 reads its band's zones in order and takes the zone's location when
// it holds the whole quantity; when no zone does, the catch-all DIR-P20 takes
// the first location by pick sequence that does; with none the line is blocked.
func TestEvaluate_OutboundPickFixture(t *testing.T) {
	zones := []string{"FWD", "CASE", "RESERVE", "OVERFLOW"}
	stockOf := map[byte]string{'0': "", 's': "50", 'm': "500", 'l': "5000"}
	layouts := []string{"ssss", "0sml", "s0ml", "00ml", "0000", "s000", "0s00", "00m0", "000l", "slml"}
	qtys := []int{1, 50, 99, 100, 500, 1199, 1200, 3000, 5000, 6000}
	bandZones := func(q int) []string {
		switch {
		case q < 100:
			return []string{"FWD"}
		case q < 1200:
			return []string{"CASE", "RESERVE"}
		}
		return []string{"RESERVE", "OVERFLOW"}
	}
	holds := func(layout string, zone string, q int) bool {
		for i, z := range zones {
			if z != zone || layout[i] == '0' {
				continue
			}
			have, _ := decimal.NewFromString(stockOf[layout[i]])
			return have.GreaterThanOrEqual(decimal.NewFromInt(int64(q)))
		}
		return false
	}
	count := 0
	for _, layout := range layouts {
		var locs []Location
		for i, z := range zones {
			l := loc(fmt.Sprintf("L%d", i), string(z[0])+"-LOC", z, i+1)
			if s := stockOf[layout[i]]; s != "" {
				l.Stock = map[string]decimal.Decimal{"BOLT-M8": dec(s)}
			}
			locs = append(locs, l)
		}
		for _, q := range qtys {
			count++
			wantLoc, wantDir := "", ""
			for _, z := range bandZones(q) {
				if holds(layout, z, q) {
					wantLoc, wantDir = z[:1]+"-LOC", "DIR-P10"
					break
				}
			}
			if wantLoc == "" {
				for _, z := range zones {
					if holds(layout, z, q) {
						wantLoc, wantDir = z[:1]+"-LOC", "DIR-P20"
						break
					}
				}
			}
			line := Line{ItemID: "BOLT-M8", WorkType: "SALES_PICK", Direction: DirectionPick, Warehouse: "WH1",
				Qty: decimal.NewFromInt(int64(q)), Attributes: map[string]string{"item_group": "FASTENERS"}}
			got := Evaluate(outboundDirectives(), line, locs)
			name := fmt.Sprintf("layout %s qty %d", layout, q)
			switch {
			case wantLoc == "" && !(got.Blocked && got.Code == CodeNoLocation && got.Location == nil):
				t.Errorf("%s: want blocked WMS-0101, got %+v", name, got)
			case wantLoc != "" && (got.Blocked || got.Location == nil || got.Location.Name != wantLoc || got.Directive != wantDir):
				t.Errorf("%s: want %s via %s, got %+v", name, wantLoc, wantDir, got)
			}
		}
	}
	if count != 100 {
		t.Fatalf("fixture has %d scenarios, want 100", count)
	}
}

// TestEvaluate_OutboundPickLiterals pins expected locations as literals, with
// several locations per zone so pick-sequence ordering and "holds the whole
// quantity" decide the answer.
func TestEvaluate_OutboundPickLiterals(t *testing.T) {
	stock := func(l Location, qty string) Location {
		l.Stock = map[string]decimal.Decimal{"BOLT-M8": dec(qty)}
		return l
	}
	locs := []Location{
		stock(loc("f2", "F-02", "FWD", 2), "500"),
		stock(loc("f1", "F-01", "FWD", 1), "30"),
		stock(loc("c1", "C-01", "CASE", 3), "400"),
		stock(loc("c2", "C-02", "CASE", 4), "900"),
		stock(loc("r1", "R-01", "RESERVE", 5), "5000"),
	}
	tests := []struct {
		qty, loc, directive string
	}{
		{"10", "F-01", "DIR-P10"},
		{"30", "F-01", "DIR-P10"},
		{"31", "F-02", "DIR-P10"},
		{"99", "F-02", "DIR-P10"},
		{"100", "C-01", "DIR-P10"},
		{"400", "C-01", "DIR-P10"},
		{"401", "C-02", "DIR-P10"},
		{"901", "R-01", "DIR-P10"},
		{"1200", "R-01", "DIR-P10"},
		{"5000", "R-01", "DIR-P10"},
	}
	for _, tc := range tests {
		line := Line{ItemID: "BOLT-M8", WorkType: "SALES_PICK", Direction: DirectionPick, Warehouse: "WH1", Qty: dec(tc.qty),
			Attributes: map[string]string{"item_group": "FASTENERS"}}
		got := Evaluate(outboundDirectives(), line, locs)
		if got.Blocked || got.Location.Name != tc.loc || got.Directive != tc.directive {
			t.Errorf("qty %s: want %s via %s, got %+v", tc.qty, tc.loc, tc.directive, got)
		}
	}
	line := Line{ItemID: "BOLT-M8", WorkType: "SALES_PICK", Direction: DirectionPick, Warehouse: "WH1", Qty: dec("5001"),
		Attributes: map[string]string{"item_group": "FASTENERS"}}
	if got := Evaluate(outboundDirectives(), line, locs); !got.Blocked || got.Code != CodeNoLocation {
		t.Errorf("qty 5001 must be blocked, got %+v", got)
	}
	other := line
	other.Qty, other.Attributes = dec("31"), nil
	if got := Evaluate(outboundDirectives(), other, locs); got.Blocked || got.Location.Name != "F-02" || got.Directive != "DIR-P20" {
		t.Errorf("a non-fastener falls to the catch-all and takes the first location by pick sequence that covers it (F-02), got %+v", got)
	}
}
