package domain

import (
	"testing"
)

func fcCell(ref string, start, end int, qty, reduced string) ForecastCellDemand {
	return ForecastCellDemand{Ref: ref, StartDay: start, EndDay: end, Qty: dec(qty), ReducedQty: dec(reduced)}
}

func quantities(rs []Requirement) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Ref+"="+r.Qty.String())
	}
	return out
}

// AC-22.14 / BR-22.13: forecast 100, sale 40 inside the backward window, so the
// forecast demand left is 60 and with the sale the gross requirement is 100.
func TestPlanForecast_AssembleToOrderConsumesForecast(t *testing.T) {
	st := DefaultStrategy(StrategyATOFinal)
	cells := []ForecastCellDemand{fcCell("F1", 0, 29, "100", "100")}
	sales := []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 19, Qty: dec("40")}}
	got, err := PlanForecast(st, cells, sales)
	if err != nil || len(got) != 1 || !got[0].Qty.Equal(dec("60")) || got[0].RefType != RefForecast || got[0].Priority != PriorityForecast {
		t.Fatalf("PlanForecast = %+v, %v; want one forecast demand of 60", got, err)
	}
	if gross := got[0].Qty.Add(sales[0].Qty); !gross.Equal(dec("100")) {
		t.Fatalf("gross = %s; want 100", gross)
	}
}

// ATO uses the forecast as entered, not the model's reduced quantity, so sales
// are not taken off twice.
func TestPlanForecast_AssembleToOrderIgnoresTheModelReduction(t *testing.T) {
	cells := []ForecastCellDemand{fcCell("F1", 0, 29, "100", "60")}
	got, err := PlanForecast(DefaultStrategy(StrategyATOFinal), cells, nil)
	if err != nil || len(got) != 1 || !got[0].Qty.Equal(dec("100")) {
		t.Fatalf("PlanForecast = %+v, %v; want 100", got, err)
	}
}

func TestPlanForecast_FullyConsumedCellIsDropped(t *testing.T) {
	cells := []ForecastCellDemand{fcCell("F1", 0, 29, "30", "30"), fcCell("F2", 30, 59, "50", "50")}
	sales := []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 10, Qty: dec("45")}}
	got, err := PlanForecast(DefaultStrategy(StrategyATOFinal), cells, sales)
	// 45 on day 10 takes F1's 30, then nothing from F2 (forward window 0): the other 15 stay in sales.
	if err != nil || len(got) != 1 || got[0].Ref != "F2" || !got[0].Qty.Equal(dec("50")) {
		t.Fatalf("PlanForecast = %v, %v; want F2 whole", quantities(got), err)
	}
}

func TestPlanForecast_ForwardWindowReachesLaterPeriods(t *testing.T) {
	st := DefaultStrategy(StrategyATOFinal)
	st.ConsumptionMode, st.BackwardDays, st.ForwardDays = ConsumptionBackwardForward, 0, 40
	cells := []ForecastCellDemand{fcCell("F1", 0, 29, "30", "30"), fcCell("F2", 30, 59, "50", "50")}
	sales := []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 10, Qty: dec("45")}}
	got, err := PlanForecast(st, cells, sales)
	if err != nil || len(got) != 1 || got[0].Ref != "F2" || !got[0].Qty.Equal(dec("35")) {
		t.Fatalf("PlanForecast = %v, %v; want F2 at 35", quantities(got), err)
	}
}

func TestPlanForecast_ByStrategy(t *testing.T) {
	cells := []ForecastCellDemand{fcCell("F1", 0, 29, "100", "70"), fcCell("F2", 30, 59, "20", "0")}
	sales := []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 10, Qty: dec("40")}}
	tests := []struct {
		strategy string
		want     []string
	}{
		{StrategyMTSNet, []string{"F1=70"}},
		{StrategyMTSGross, []string{"F1=100", "F2=20"}},
		{StrategyMTO, []string{}},
		{StrategyPlanWithoutFinal, []string{"F1=70"}},
		{StrategySubassembly, []string{"F1=70"}},
	}
	for _, tc := range tests {
		got, err := PlanForecast(DefaultStrategy(tc.strategy), cells, sales)
		if err != nil {
			t.Fatalf("%s: %v", tc.strategy, err)
		}
		if g := quantities(got); len(g) != len(tc.want) || (len(g) > 0 && (g[0] != tc.want[0] || (len(g) > 1 && g[1] != tc.want[1]))) {
			t.Errorf("%s: got %v; want %v", tc.strategy, g, tc.want)
		}
	}
}

func TestPlanForecast_InvalidWindowIsRefused(t *testing.T) {
	st := DefaultStrategy(StrategyATOFinal)
	st.BackwardDays = -1
	if _, err := PlanForecast(st, []ForecastCellDemand{fcCell("F1", 0, 29, "10", "10")}, nil); err != ErrInvalidStrategy {
		t.Fatalf("err = %v; want ErrInvalidStrategy", err)
	}
}

// The result does not depend on the order the sales lines arrive in.
func TestPlanForecast_IndependentOfSalesOrder(t *testing.T) {
	cells := []ForecastCellDemand{fcCell("F1", 0, 29, "50", "50")}
	a := []Requirement{{Ref: "A", Date: 5, Qty: dec("30")}, {Ref: "B", Date: 6, Qty: dec("30")}}
	b := []Requirement{a[1], a[0]}
	x, _ := PlanForecast(DefaultStrategy(StrategyATOFinal), cells, a)
	y, _ := PlanForecast(DefaultStrategy(StrategyATOFinal), cells, b)
	if len(x) != 0 || len(y) != 0 {
		t.Fatalf("forecast left %v / %v; want none (60 of sales exceed 50)", quantities(x), quantities(y))
	}
}

func TestStrategySpec_ValidateAndDefaults(t *testing.T) {
	if errs := DefaultStrategy(StrategyMTO).Validate(); errs != nil {
		t.Fatalf("default invalid: %v", errs)
	}
	if !DefaultStrategy(StrategyMTO).UsesMarking || !DefaultStrategy(StrategyPlanWithoutFinal).UsesMarking || DefaultStrategy(StrategyATOFinal).UsesMarking {
		t.Fatal("marking defaults wrong")
	}
	bad := StrategySpec{Code: "x", ConsumptionMode: "y", BackwardDays: 400, ForwardDays: -2}
	if errs := bad.Validate(); len(errs) != 4 {
		t.Fatalf("errors %v; want code, consumptionMode, backwardDays, forwardDays", errs)
	}
	empty := StrategySpec{Code: StrategyATOFinal, ConsumptionMode: ConsumptionBackwardForward}
	if errs := empty.Validate(); errs["consumptionMode"] == "" {
		t.Fatalf("errors %v; want an empty window refused", errs)
	}
	if len(StrategyCodes()) != 6 {
		t.Fatal("six strategies")
	}
}

// BR-22.13 window edges: a sale on day d consumes a period that ends on
// d - backward or starts on d + forward (both edges inclusive), and nothing a
// day further out.
func TestPlanForecast_WindowEdgesAreInclusive(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		back, fwd int
		cell      ForecastCellDemand
		saleDay   int
		want      string
	}{
		{"backward edge consumed", ConsumptionBackward, 30, 0, fcCell("F", 0, 9, "50", "50"), 39, "F=10"},
		{"one day past backward edge", ConsumptionBackward, 30, 0, fcCell("F", 0, 9, "50", "50"), 40, "F=50"},
		{"forward edge consumed", ConsumptionForward, 0, 10, fcCell("F", 50, 59, "50", "50"), 40, "F=10"},
		{"one day past forward edge", ConsumptionForward, 0, 9, fcCell("F", 50, 59, "50", "50"), 40, "F=50"},
		{"backward mode ignores a later period", ConsumptionBackward, 30, 10, fcCell("F", 50, 59, "50", "50"), 40, "F=50"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := DefaultStrategy(StrategyATOFinal)
			st.ConsumptionMode, st.BackwardDays, st.ForwardDays = tc.mode, tc.back, tc.fwd
			sales := []Requirement{{Ref: "SO-1", RefType: RefSales, Date: tc.saleDay, Qty: dec("40")}}
			got, err := PlanForecast(st, []ForecastCellDemand{tc.cell}, sales)
			if err != nil || len(got) != 1 || quantities(got)[0] != tc.want {
				t.Fatalf("PlanForecast = %v, %v; want %s", quantities(got), err, tc.want)
			}
		})
	}
}

func TestPlanForecastNotes_ReportsSalesTheForecastCouldNotConsume(t *testing.T) {
	cells := []ForecastCellDemand{fcCell("F1", 0, 29, "100", "100")}
	sales := []Requirement{
		{Ref: "SO-1", RefType: RefSales, Date: 5, Qty: dec("70")},
		{Ref: "SO-2", RefType: RefSales, Date: 10, Qty: dec("50")},
		{Ref: "SO-3", RefType: RefSales, Date: 12, Qty: dec("0.5")},
	}
	demand, onTop, err := PlanForecastNotes(DefaultStrategy(StrategyATOFinal), cells, sales)
	if err != nil || len(demand) != 0 {
		t.Fatalf("demand = %v, %v; want the forecast used up", quantities(demand), err)
	}
	// SO-1 takes 70, SO-2 takes the last 30 and has 20 left, SO-3 has all of 0.5 left.
	if len(onTop) != 2 || onTop[0].Ref != "SO-2" || !onTop[0].Qty.Equal(dec("20")) || onTop[0].Date != 10 || onTop[1].Ref != "SO-3" || !onTop[1].Qty.Equal(dec("0.5")) {
		t.Fatalf("onTop = %+v; want SO-2 of 20 and SO-3 of 0.5", onTop)
	}
	for _, code := range []string{StrategyMTSNet, StrategyMTSGross, StrategyMTO} {
		if _, onTop, _ := PlanForecastNotes(DefaultStrategy(code), cells, sales); len(onTop) != 0 {
			t.Errorf("%s reports sales on top: %+v", code, onTop)
		}
	}
}

func TestPlanForecast_GrossDemandIsMarkedGross(t *testing.T) {
	cells := []ForecastCellDemand{fcCell("F1", 0, 29, "100", "60")}
	for code, want := range map[string]bool{StrategyMTSGross: true, StrategyMTSNet: false, StrategyATOFinal: false} {
		got, err := PlanForecast(DefaultStrategy(code), cells, nil)
		if err != nil || len(got) != 1 || got[0].Gross != want {
			t.Errorf("%s: %+v, %v; want Gross %v", code, got, err, want)
		}
	}
}

// A gross demand leaves the on-hand layer to the other demand: 50 on hand,
// a gross forecast of 30 and a sale of 40 order 30 (the sale takes the stock).
func TestRun_GrossDemandIsNotNettedAgainstOnHand(t *testing.T) {
	it := Item{ID: "G1", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 2, OnHand: d("50"),
		Demand: []Requirement{
			{Ref: "SO-1", RefType: RefSales, Date: 10, Qty: d("40"), Priority: PrioritySales},
			{Ref: "FC-1", RefType: RefForecast, Date: 10, Qty: d("30"), Priority: PriorityForecast, Gross: true},
		}}
	res, err := Run(0, []Item{it})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Orders) != 1 || !res.Orders[0].Qty.Equal(d("30")) {
		t.Fatalf("orders = %+v; want one of 30", res.Orders)
	}
	// The net view counts the on-hand once: the sale takes 40 of it, the gross
	// forecast's 30 is the net requirement, and PAB closes at 50 − 40 − 30 + 30.
	if len(res.Net) != 1 {
		t.Fatalf("net rows = %+v; want day 10", res.Net)
	}
	if row := res.Net[0]; row.Day != 10 || !row.Net.Equal(d("30")) || !row.PAB.Equal(d("10")) {
		t.Fatalf("day 10 row = %+v; want net 30 and PAB 10", row)
	}
	// Net, the 50 on hand cover the sale of 40 and 10 of the forecast, so 20 is short.
	it.Demand[1].Gross = false
	res, err = Run(0, []Item{it})
	if err != nil || len(res.Orders) != 1 || !res.Orders[0].Qty.Equal(d("20")) {
		t.Fatalf("net orders = %+v, %v; want one of 20", res.Orders, err)
	}
	if row := res.Net[0]; !row.Net.Equal(d("20")) || !row.PAB.Equal(d("0")) {
		t.Fatalf("net day 10 row = %+v; want net 20 and PAB 0", row)
	}
}

func TestRun_SalesOnTopRaisesTheInformationMessage(t *testing.T) {
	it := Item{ID: "G1", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 2,
		SalesOnTop: []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 9, Qty: d("12")}}}
	res, err := Run(0, []Item{it})
	if err != nil || len(res.Messages) != 1 {
		t.Fatalf("messages = %+v, %v", res.Messages, err)
	}
	if m := res.Messages[0]; m.Action != ActionSalesOnTop || m.ItemID != "G1" || m.DemandRef != "SO-1" || m.SuggestedDate != 9 || !m.Qty.Equal(d("12")) {
		t.Fatalf("message = %+v", m)
	}
}
