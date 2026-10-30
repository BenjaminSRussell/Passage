package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

const (
	scItemA = "00000000-0000-0000-0000-00000000000a"
	scItemB = "00000000-0000-0000-0000-00000000000b"
)

func scenarioItems() []Item {
	return []Item{
		{ID: scItemA, Coverage: CoverageRequirement, OrderType: OrderProduction, LeadTimeDays: 5, AutoFirm: true, FirmingDays: 3,
			Demand: []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 10, Qty: dec("100"), Priority: PrioritySales}, {Ref: "B-1", RefType: RefBOM, Date: 10, Qty: dec("10")}}},
		{ID: scItemB, Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 2,
			Demand: []Requirement{{Ref: "SO-2", RefType: RefSales, Date: 12, Qty: dec("40"), Priority: PrioritySales}}},
	}
}

func validScenario() ScenarioSpec {
	return ScenarioSpec{Code: "UP20", PlanCode: "MAIN", Name: "Demand +20%", DemandFactor: dec("1.2"), Active: true}
}

func TestScenarioValidate(t *testing.T) {
	if errs := validScenario().Validate(); errs != nil {
		t.Fatalf("valid scenario refused: %v", errs)
	}
	bad := ScenarioSpec{Code: "x", PlanCode: "", Name: "", DemandFactor: dec("-1"),
		ItemFactors:       []ItemFactor{{ItemID: "nope", Factor: dec("1")}, {ItemID: scItemA, Factor: dec("101")}, {ItemID: scItemA, Factor: dec("1")}},
		AddedDemand:       []AddedDemand{{ItemID: scItemA, Date: "2026-13-01", Qty: dec("0")}},
		LeadTimeOverrides: []LeadTimeOverride{{ItemID: scItemA, LeadTimeDays: -1}},
	}
	errs := bad.Validate()
	for _, k := range []string{"code", "planCode", "name", "demandFactor", "itemFactors[0].itemId", "itemFactors[1].factor", "itemFactors[2].itemId",
		"addedDemand[0].date", "addedDemand[0].qty", "leadTimeOverrides[0].leadTimeDays"} {
		if errs[k] == "" {
			t.Errorf("missing error for %s: %v", k, errs)
		}
	}
	over := validScenario()
	over.DemandFactor = dec("1.00001")
	if over.Validate()["demandFactor"] == "" {
		t.Error("5 decimals accepted")
	}
}

func TestApplyScenario_FactorsLeadTimesAndAddedDemand(t *testing.T) {
	asOf := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	in := scenarioItems()
	s := validScenario()
	s.ItemFactors = []ItemFactor{{ItemID: scItemB, Factor: dec("0.5")}}
	s.LeadTimeOverrides = []LeadTimeOverride{{ItemID: scItemA, LeadTimeDays: 9}}
	s.AddedDemand = []AddedDemand{{ItemID: scItemA, Date: "2026-11-20", Qty: dec("15")}, {ItemID: "00000000-0000-0000-0000-0000000000ff", Date: "2026-11-20", Qty: dec("1")}}
	out, err := ApplyScenario(in, asOf, s)
	if err != nil {
		t.Fatal(err)
	}
	if got := out[0].Demand[0].Qty; !got.Equal(dec("120")) {
		t.Errorf("sales demand of A = %s, want 120", got)
	}
	if got := out[0].Demand[1].Qty; !got.Equal(dec("10")) {
		t.Errorf("dependent demand scaled: %s", got)
	}
	if got := out[1].Demand[0].Qty; !got.Equal(dec("20")) {
		t.Errorf("item factor not used: %s", got)
	}
	if out[0].LeadTimeDays != 9 || out[1].LeadTimeDays != 2 {
		t.Errorf("lead times %d %d", out[0].LeadTimeDays, out[1].LeadTimeDays)
	}
	if out[0].AutoFirm {
		t.Error("a simulation must not auto-firm")
	}
	added := out[0].Demand[len(out[0].Demand)-1]
	if added.Ref != "SC:UP20:1" || added.Date != 19 || !added.Qty.Equal(dec("15")) || added.RefType != RefSales {
		t.Errorf("added demand = %+v", added)
	}
	if !in[0].Demand[0].Qty.Equal(dec("100")) || !in[0].AutoFirm || in[0].LeadTimeDays != 5 || len(in[0].Demand) != 2 {
		t.Error("input items were changed")
	}
}

// AC-22.11: with demand +20% the simulated production is at least the
// operational plan's, item by item.
func TestApplyScenario_AC2211SimulatedQuantityNotBelowOperational(t *testing.T) {
	asOf := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	base, err := RunWithCalendar(0, nil, scenarioItems())
	if err != nil {
		t.Fatal(err)
	}
	items, err := ApplyScenario(scenarioItems(), asOf, validScenario())
	if err != nil {
		t.Fatal(err)
	}
	sim, err := RunWithCalendar(0, nil, items)
	if err != nil {
		t.Fatal(err)
	}
	total := func(r Result, item string) decimal.Decimal {
		sum := decimal.Zero
		for _, o := range r.Orders {
			if o.ItemID == item {
				sum = sum.Add(o.Qty)
			}
		}
		return sum
	}
	for _, id := range []string{scItemA, scItemB} {
		if total(sim, id).LessThan(total(base, id)) {
			t.Errorf("item %s: simulated %s below operational %s", id, total(sim, id), total(base, id))
		}
	}
	if !total(sim, scItemB).Equal(dec("48")) {
		t.Errorf("item B simulated qty = %s, want 48", total(sim, scItemB))
	}
}

func TestApplyScenario_ZeroFactorRemovesDemandAndFarDateRefused(t *testing.T) {
	asOf := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	s := validScenario()
	s.DemandFactor = decimal.Zero
	out, err := ApplyScenario(scenarioItems(), asOf, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(out[1].Demand) != 0 {
		t.Errorf("demand left: %+v", out[1].Demand)
	}
	s.AddedDemand = []AddedDemand{{ItemID: scItemA, Date: "2046-01-01", Qty: dec("1")}}
	if _, err := ApplyScenario(scenarioItems(), asOf, s); err == nil {
		t.Error("date 20 years out accepted")
	}
}

// The demand factor scales independent demand only: sales lines and forecast.
// Safety-stock and transfer-issue lines sent as demand keep their quantity.
func TestApplyScenario_FactorScalesSalesAndForecastOnly(t *testing.T) {
	asOf := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	items := []Item{{ID: scItemA, Coverage: CoverageRequirement, OrderType: OrderPurchase, Demand: []Requirement{
		{Ref: "SO-1", RefType: RefSales, Date: 3, Qty: dec("10")},
		{Ref: "FC-1", RefType: RefForecast, Date: 4, Qty: dec("20")},
		{Ref: "SS-1", RefType: RefSafety, Date: 0, Qty: dec("30")},
		{Ref: "TR-1", RefType: "transfer-issue", Date: 5, Qty: dec("40")},
	}}}
	s := validScenario()
	s.DemandFactor = dec("2")
	out, err := ApplyScenario(items, asOf, s)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"20", "40", "30", "40"} {
		if got := out[0].Demand[i]; !got.Qty.Equal(dec(want)) {
			t.Errorf("%s qty = %s, want %s", got.Ref, got.Qty, want)
		}
	}
}
