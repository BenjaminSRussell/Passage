package domain

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func decPtr(s string) *decimal.Decimal {
	d := dec(s)
	return &d
}

func intPtr(n int) *int { return &n }

// demo directives from BR-31.1: directive 10 for fasteners, directive 20 catch-all.
func demoDirectives() []Directive {
	return []Directive{
		{ID: "DIR-20", Sequence: 20, WorkType: "PURCHASE_PUTAWAY", Direction: DirectionPut, Warehouse: "WH1",
			Bands: []Band{{Seq: 1, FromQty: dec("0"), Actions: []Action{{Seq: 1, Strategy: StrategyEmptyLocation}}}}},
		{ID: "DIR-10", Sequence: 10, WorkType: "PURCHASE_PUTAWAY", Direction: DirectionPut, Warehouse: "WH1",
			Query: Query{"item_group": "FASTENERS"},
			Bands: []Band{
				{Seq: 1, FromQty: dec("0"), ToQty: decPtr("100"), Actions: []Action{{Seq: 1, Strategy: StrategyConsolidate, Zone: "FWD"}}},
				{Seq: 2, FromQty: dec("100"), ToQty: decPtr("1200"), Actions: []Action{
					{Seq: 1, Strategy: StrategyConsolidate, Zone: "CASE"}, {Seq: 2, Strategy: StrategyEmptyLocation, Zone: "CASE"}}},
				{Seq: 3, FromQty: dec("1200"), Actions: []Action{
					{Seq: 1, Strategy: StrategyEmptyLocation, Zone: "RESERVE"}, {Seq: 2, Strategy: StrategyEmptyLocation, Zone: "OVERFLOW"}}},
			}},
	}
}

func fastener(qty, weight string, lps int) Line {
	return Line{ItemID: "BOLT-M8", WorkType: "PURCHASE_PUTAWAY", Direction: DirectionPut, Warehouse: "WH1",
		Qty: dec(qty), Weight: dec(weight), Volume: dec("0"), LPs: lps, Attributes: map[string]string{"item_group": "FASTENERS"}}
}

func loc(id, name, zone string, pick int) Location {
	return Location{ID: id, Name: name, Zone: zone, PickSequence: pick, Status: StatusActive, Capacity: Capacity{}}
}

// occupied is a location holding another item, so it is not empty and refuses a second item.
func occupied(id, name, zone string, pick int) Location {
	l := loc(id, name, zone, pick)
	l.Stock = map[string]decimal.Decimal{"OTHER": dec("1")}
	return l
}

func TestEvaluate_WorkedExampleResolvesReserveLocation(t *testing.T) {
	// BR-31.1: 1,200 EA of BOLT-M8, 14.4 kg. R-05-02-01 is the first empty RESERVE location with room.
	reserveEmpty := loc("r1", "R-05-02-01", "RESERVE", 5021)
	reserveEmpty.Capacity.MaxWeight = decPtr("14.4")
	reserveFull := loc("r0", "R-05-02-00", "RESERVE", 5020)
	reserveFull.Used.LPs = 1
	reserveFull.Stock = map[string]decimal.Decimal{"OTHER": dec("5")}
	got := Evaluate(demoDirectives(), fastener("1200", "14.4", 1), []Location{reserveFull, reserveEmpty})
	if got.Blocked || got.Location == nil || got.Location.Name != "R-05-02-01" {
		t.Fatalf("want R-05-02-01, got %+v", got)
	}
	if got.Directive != "DIR-10" {
		t.Errorf("winning directive = %q, want DIR-10", got.Directive)
	}
	if last := got.Trace[len(got.Trace)-1]; last.Outcome != "hit" || last.Band != 3 || last.Action != 1 {
		t.Errorf("last trace step = %+v, want hit on band 3 action 1", last)
	}
}

func TestEvaluate_FallsThroughActionsAndDirectives(t *testing.T) {
	overflow := loc("o1", "O-01-01-01", "OVERFLOW", 9001)
	tests := []struct {
		name      string
		line      Line
		locations []Location
		wantLoc   string // empty = blocked
		wantDir   string
	}{
		{
			name:      "reserve full falls to the first empty overflow location",
			line:      fastener("1200", "14.4", 1),
			locations: []Location{overflow},
			wantLoc:   "O-01-01-01", wantDir: "DIR-10",
		},
		{
			name:      "no fastener directive matches, catch-all takes an empty location",
			line:      Line{ItemID: "WASHER", WorkType: "PURCHASE_PUTAWAY", Direction: DirectionPut, Warehouse: "WH1", Qty: dec("10"), Weight: dec("0.1")},
			locations: []Location{overflow},
			wantLoc:   "O-01-01-01", wantDir: "DIR-20",
		},
		{
			name:      "nothing admits the line, so it is blocked",
			line:      fastener("1200", "14.4", 1),
			locations: []Location{occupied("x", "X-01-01-01", "FWD", 1)},
			wantLoc:   "", wantDir: "",
		},
		{
			name:      "another warehouse is out of scope",
			line:      Line{ItemID: "WASHER", WorkType: "PURCHASE_PUTAWAY", Direction: DirectionPut, Warehouse: "WH2", Qty: dec("10")},
			locations: []Location{overflow},
			wantLoc:   "", wantDir: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(demoDirectives(), tc.line, tc.locations)
			if tc.wantLoc == "" {
				if !got.Blocked || got.Code != CodeNoLocation || got.Location != nil {
					t.Fatalf("want blocked WMS-0101, got %+v", got)
				}
				return
			}
			if got.Location == nil || got.Location.Name != tc.wantLoc || got.Directive != tc.wantDir {
				t.Fatalf("want %s via %s, got %+v", tc.wantLoc, tc.wantDir, got)
			}
		})
	}
}

func TestEvaluate_BlockedTraceExplainsRejection(t *testing.T) {
	// AC-31.2: a location with max_lps = 1 holding one LP is skipped and the trace says so.
	full := loc("f", "F-01-01-01", "FWD", 1)
	full.Capacity.MaxLPs = intPtr(1)
	full.Used.LPs = 1
	full.Stock = map[string]decimal.Decimal{"BOLT-M8": dec("100")}
	got := Evaluate(demoDirectives(), fastener("50", "0.6", 1), []Location{full})
	if !got.Blocked {
		t.Fatalf("want blocked, got %+v", got)
	}
	var found bool
	for _, s := range got.Trace {
		if strings.Contains(s.Reason, "capacity: lps 1/1") {
			found = true
		}
	}
	if !found {
		t.Errorf("trace lacks 'capacity: lps 1/1': %+v", got.Trace)
	}
}

func TestEvaluate_ConsolidatePrefersLocationsAlreadyHoldingItem(t *testing.T) {
	empty := loc("a", "FWD-01-01-01", "FWD", 1)
	holding := loc("b", "FWD-01-01-09", "FWD", 99)
	holding.Stock = map[string]decimal.Decimal{"BOLT-M8": dec("10")}
	got := Evaluate(demoDirectives(), fastener("50", "0.6", 0), []Location{empty, holding})
	if got.Location == nil || got.Location.Name != "FWD-01-01-09" {
		t.Fatalf("consolidate should prefer the holding location, got %+v", got.Location)
	}
}

func TestEvaluate_BandBoundaries(t *testing.T) {
	// from_qty <= qty < to_qty: 99 is band 1 (FWD), 100 is band 2 (CASE).
	fwd := loc("f", "FWD-01-01-01", "FWD", 1)
	caseLoc := loc("c", "CASE-01-01-01", "CASE", 2)
	for qty, want := range map[string]string{"99": "FWD-01-01-01", "100": "CASE-01-01-01"} {
		got := Evaluate(demoDirectives(), fastener(qty, "0", 0), []Location{fwd, caseLoc})
		if got.Location == nil || got.Location.Name != want {
			t.Errorf("qty %s: want %s, got %+v", qty, want, got.Location)
		}
	}
}

func TestEvaluate_PickUsesStockAndIgnoresCapacity(t *testing.T) {
	stocked := loc("s", "FWD-01-01-01", "FWD", 1)
	stocked.Stock = map[string]decimal.Decimal{"BOLT-M8": dec("500")}
	pick := fastener("50", "0", 0)
	pick.Direction = DirectionPick
	pick.WorkType = "SALES_PICK"
	dirs := []Directive{{ID: "PK-1", Sequence: 1, WorkType: "SALES_PICK", Direction: DirectionPick, Warehouse: "WH1",
		Bands: []Band{{Seq: 1, FromQty: dec("0"), Actions: []Action{{Seq: 1, Strategy: StrategyFEFO}}}}}}
	got := Evaluate(dirs, pick, []Location{stocked})
	if got.Location == nil || got.Location.Name != "FWD-01-01-01" {
		t.Fatalf("pick should resolve the stocked location, got %+v", got)
	}
}

func TestEvaluate_BlockedStatusRefusesDirection(t *testing.T) {
	blocked := loc("b", "FWD-01-01-01", "FWD", 1)
	blocked.Status = StatusBlockedIn
	got := Evaluate(demoDirectives(), fastener("50", "0", 0), []Location{blocked})
	if !got.Blocked {
		t.Fatalf("BLOCKED_IN must refuse put-away, got %+v", got)
	}
}

func TestEvaluate_MixingRuleRefusesSecondItem(t *testing.T) {
	// WMS-0103: profile allow_mixed_items = false refuses a second item.
	mixed := loc("m", "FWD-01-01-01", "FWD", 1)
	mixed.Stock = map[string]decimal.Decimal{"NUT-M8": dec("3")}
	got := Evaluate(demoDirectives(), fastener("50", "0", 0), []Location{mixed})
	if !got.Blocked {
		t.Fatalf("mixing must be refused, got %+v", got)
	}
	var found bool
	for _, s := range got.Trace {
		found = found || strings.Contains(s.Reason, "mixing: holds NUT-M8")
	}
	if !found {
		t.Errorf("trace lacks mixing reason: %+v", got.Trace)
	}
}

func TestValidStrategyAndDirection(t *testing.T) {
	for _, s := range []Strategy{"consolidate", "fifo", "lifo", "fefo", "empty_location", "match_packing_quantity", "round_up_to_lp", "outbound_staging"} {
		if !ValidStrategy(s) {
			t.Errorf("%s should be valid", s)
		}
	}
	if ValidStrategy("random") || ValidDirection("PACK") || !ValidDirection(DirectionPick) {
		t.Error("unexpected validation result")
	}
}

func TestEvaluate_PickSkipsLocationHoldingLessThanTheLine(t *testing.T) {
	// No split picks in slice 1: a location holding 30 of a 50 EA line is not a
	// candidate, so the next location by pick sequence that covers it wins.
	short := loc("s", "FWD-01-01-01", "FWD", 1)
	short.Stock = map[string]decimal.Decimal{"BOLT-M8": dec("30")}
	enough := loc("e", "FWD-01-01-02", "FWD", 2)
	enough.Stock = map[string]decimal.Decimal{"BOLT-M8": dec("50")}
	pick := fastener("50", "0", 0)
	pick.Direction = DirectionPick
	pick.WorkType = "SALES_PICK"
	dirs := []Directive{{ID: "PK-1", Sequence: 1, WorkType: "SALES_PICK", Direction: DirectionPick, Warehouse: "WH1",
		Bands: []Band{{Seq: 1, FromQty: dec("0"), Actions: []Action{{Seq: 1, Strategy: StrategyFIFO}}}}}}
	got := Evaluate(dirs, pick, []Location{short, enough})
	if got.Location == nil || got.Location.Name != "FWD-01-01-02" {
		t.Fatalf("want FWD-01-01-02, got %+v", got)
	}
	got = Evaluate(dirs, pick, []Location{short})
	if !got.Blocked || !strings.Contains(got.Trace[0].Reason, "stock below line quantity (1)") {
		t.Fatalf("want blocked with a stock reason, got %+v", got)
	}
}

func TestEvaluate_BlockedOutRefusesPickButAllowsPut(t *testing.T) {
	l := loc("b", "FWD-01-01-01", "FWD", 1)
	l.Status = StatusBlockedOut
	l.Stock = map[string]decimal.Decimal{"BOLT-M8": dec("500")}
	l.Capacity.AllowMixedItems = true
	pick := fastener("50", "0", 0)
	pick.Direction = DirectionPick
	pick.WorkType = "SALES_PICK"
	dirs := []Directive{{ID: "PK-1", Sequence: 1, WorkType: "SALES_PICK", Direction: DirectionPick, Warehouse: "WH1",
		Bands: []Band{{Seq: 1, FromQty: dec("0"), Actions: []Action{{Seq: 1, Strategy: StrategyFIFO}}}}}}
	if got := Evaluate(dirs, pick, []Location{l}); !got.Blocked || !strings.Contains(got.Trace[0].Reason, "status BLOCKED_OUT blocks PICK") {
		t.Fatalf("BLOCKED_OUT must refuse a pick, got %+v", got)
	}
	if got := Evaluate(demoDirectives(), fastener("50", "0", 0), []Location{l}); got.Blocked {
		t.Fatalf("BLOCKED_OUT must still accept put-away, got %+v", got)
	}
}
