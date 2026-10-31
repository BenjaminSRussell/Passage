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
