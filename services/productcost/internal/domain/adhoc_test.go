package domain

import (
	"errors"
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// injectionModel is an easy-cost-planning model for an injection-moulded part
// (FR-39.13): granulate by weight and cavities, machine time by hours, and an
// overhead rate of 2.00 per machine hour.
func injectionModel() CostingModel {
	return CostingModel{
		Code: "INJ-PART",
		Characteristics: []Characteristic{
			{Name: "cavities", Default: ptr("4")},
			{Name: "weight"},
			{Name: "hours"},
		},
		Lines: []ModelLine{
			{Type: LineItem, Ref: "RM-GRAN", Component: "DM", QuantityFormula: "weight*cavities"},
			{Type: LineActivity, Ref: "MACHINE", Component: "DL", QuantityFormula: "hours"},
			{Type: LineOverhead, Ref: "OH-MACH", Component: "OVH", QuantityFormula: "hours", PriceFormula: "2"},
		},
	}
}

func injectionPrices() map[string]Price {
	return map[string]Price{
		"RM-GRAN": {Amount: d("3.00"), Source: "Version"},
		"MACHINE": {Amount: d("40.00"), Source: "Version"},
	}
}

func TestEstimateAdHoc_PricesEveryLineAndSplits(t *testing.T) {
	vals := map[string]decimal.Decimal{"weight": d("0.5"), "hours": d("0.5")}
	est, err := EstimateAdHoc(injectionModel(), EstimateInput{ObjectType: ObjectQuote, ObjectID: "Q-1", Values: vals, Prices: injectionPrices(), Scale: MoneyScale})
	if err != nil {
		t.Fatalf("EstimateAdHoc: %v", err)
	}
	if est.Characteristics["cavities"].StringFixed(0) != "4" {
		t.Errorf("default cavities not applied: %v", est.Characteristics)
	}
	// granulate 0.5 × 4 = 2 at 3.00 = 6.00; machine 0.5 at 40.00 = 20.00;
	// overhead rate 2.00 × 0.5 = 1.00.
	want := map[string]string{"DM": "6.00", "DL": "20.00", "OVH": "1.00"}
	for c, w := range want {
		if got := est.Split[c].StringFixed(2); got != w {
			t.Errorf("split[%s] = %s, want %s", c, got, w)
		}
	}
	if est.Total.StringFixed(2) != "27.00" {
		t.Errorf("total = %s, want 27.00", est.Total)
	}
	if len(est.Lines) != 3 || est.Lines[0].Amount.StringFixed(2) != "6.00" {
		t.Errorf("lines = %+v", est.Lines)
	}
	if len(est.Diagnostics) != 0 {
		t.Errorf("unexpected diagnostics %+v", est.Diagnostics)
	}
}

func TestEstimateAdHoc_CharacteristicOverridesDefault(t *testing.T) {
	vals := map[string]decimal.Decimal{"weight": d("1"), "hours": d("0"), "cavities": d("2")}
	est, err := EstimateAdHoc(injectionModel(), EstimateInput{ObjectType: ObjectInternalOrder, ObjectID: "IO-7", Values: vals, Prices: injectionPrices(), Scale: MoneyScale})
	if err != nil {
		t.Fatalf("EstimateAdHoc: %v", err)
	}
	if est.Total.StringFixed(2) != "6.00" {
		t.Errorf("total = %s, want 6.00 (2 cavities × 1.0 × 3.00)", est.Total)
	}
}

func TestEstimateAdHoc_AppliesCostingSheet(t *testing.T) {
	sheet := []SheetNode{{ID: "S1", Type: NodeSurcharge, Component: "IND-MAT", Basis: []string{"DM"}, Percentage: d("10")}}
	vals := map[string]decimal.Decimal{"weight": d("0.5"), "hours": d("0.5")}
	est, err := EstimateAdHoc(injectionModel(), EstimateInput{ObjectType: ObjectProjectTask, ObjectID: "T-3", Values: vals, Prices: injectionPrices(), Sheet: sheet, RoutingHours: d("0.5"), Scale: MoneyScale})
	if err != nil {
		t.Fatalf("EstimateAdHoc: %v", err)
	}
	if got := est.Split["IND-MAT"].StringFixed(2); got != "0.60" {
		t.Errorf("IND-MAT = %s, want 0.60 (10%% of DM 6.00)", got)
	}
	if est.Total.StringFixed(2) != "27.60" {
		t.Errorf("total = %s, want 27.60", est.Total)
	}
}

func TestEstimateAdHoc_FallbackPriceWarns(t *testing.T) {
	prices := injectionPrices()
	prices["MACHINE"] = Price{Amount: d("40.00"), Source: PriceSourceFallback}
	vals := map[string]decimal.Decimal{"weight": d("0.5"), "hours": d("0.5")}
	est, err := EstimateAdHoc(injectionModel(), EstimateInput{ObjectType: ObjectQuote, ObjectID: "Q-2", Values: vals, Prices: prices, Scale: MoneyScale})
	if err != nil {
		t.Fatalf("EstimateAdHoc: %v", err)
	}
	if len(est.Diagnostics) != 1 || est.Diagnostics[0].Code != CodeFallback || est.Diagnostics[0].Ref != "MACHINE" {
		t.Errorf("diagnostics = %+v, want one %s warning on MACHINE", est.Diagnostics, CodeFallback)
	}
}

func TestEstimateAdHoc_Errors(t *testing.T) {
	good := map[string]decimal.Decimal{"weight": d("0.5"), "hours": d("0.5")}
	cases := []struct {
		name   string
		model  func() CostingModel
		object string
		id     string
		vals   map[string]decimal.Decimal
		prices map[string]Price
		code   string
	}{
		{"unknown object type", injectionModel, "Invoice", "x", good, injectionPrices(), CodeEstimateInvalid},
		{"empty object id", injectionModel, ObjectQuote, "", good, injectionPrices(), CodeEstimateInvalid},
		{"missing characteristic without default", injectionModel, ObjectQuote, "q",
			map[string]decimal.Decimal{"hours": d("1")}, injectionPrices(), CodeEstimateInvalid},
		{"missing price", injectionModel, ObjectQuote, "q", good, map[string]Price{"MACHINE": {Amount: d("40")}}, CodeNoPrice},
		{"model without lines", func() CostingModel {
			m := injectionModel()
			m.Lines = nil
			return m
		}, ObjectQuote, "q", good, injectionPrices(), CodeEstimateInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EstimateAdHoc(tc.model(), EstimateInput{ObjectType: tc.object, ObjectID: tc.id, Values: tc.vals, Prices: tc.prices, Scale: MoneyScale})
			var ce *CodedError
			if !errors.As(err, &ce) || ce.Code != tc.code {
				t.Fatalf("err = %v, want code %s", err, tc.code)
			}
		})
	}
}

func TestCostingModel_ValidateRejectsBadStructure(t *testing.T) {
	cases := []struct {
		name string
		edit func(m *CostingModel)
	}{
		{"no code", func(m *CostingModel) { m.Code = " " }},
		{"duplicate characteristic", func(m *CostingModel) {
			m.Characteristics = append(m.Characteristics, Characteristic{Name: "weight"})
		}},
		{"unnamed characteristic", func(m *CostingModel) { m.Characteristics[1].Name = "" }},
		{"unknown line type", func(m *CostingModel) { m.Lines[0].Type = "Labour" }},
		{"line without ref", func(m *CostingModel) { m.Lines[0].Ref = "" }},
		{"unparseable quantity", func(m *CostingModel) { m.Lines[0].QuantityFormula = "weight*(" }},
		{"unparseable overhead rate", func(m *CostingModel) { m.Lines[2].PriceFormula = "+*" }},
		{"formula uses unknown name", func(m *CostingModel) { m.Lines[1].QuantityFormula = "minutes" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := injectionModel()
			tc.edit(&m)
			if _, err := m.Validate(); err == nil {
				t.Fatal("Validate accepted an invalid model")
			}
		})
	}
	if _, err := injectionModel().Validate(); err != nil {
		t.Errorf("valid model rejected: %v", err)
	}
}

func TestEstimateAdHoc_PriceFormulaOnAnyLine(t *testing.T) {
	// A subcontract line priced by weight band: 1.50 per kg plus 2.00 handling.
	m := CostingModel{
		Code:            "SUB",
		Characteristics: []Characteristic{{Name: "kg"}},
		Lines: []ModelLine{{Type: LineSubcontract, Ref: "PLATING", Component: "SUB",
			QuantityFormula: "1", PriceFormula: "kg * 1.5 + 2"}},
	}
	est, err := EstimateAdHoc(m, EstimateInput{ObjectType: ObjectQuote, ObjectID: "Q",
		Values: map[string]decimal.Decimal{"kg": d("4")}, Scale: MoneyScale})
	if err != nil {
		t.Fatalf("EstimateAdHoc: %v", err)
	}
	if got := est.Lines[0]; got.Price.String() != "8" || got.Source != PriceSourceFormula {
		t.Errorf("line = %+v, want price 8 from the formula", got)
	}
	if est.Total.String() != "8" {
		t.Errorf("total = %s, want 8", est.Total)
	}
}

func TestEstimateAdHoc_RoundsToTheCurrencyAndSplitSumsToTotal(t *testing.T) {
	// Three components of 1/3 each: every part rounds to 0.33 and the residual
	// 0.01 goes to DM, so the split sums to the total 1.00 (BR-39.1).
	m := CostingModel{Code: "THIRDS", Lines: []ModelLine{
		{Type: LineItem, Ref: "A", Component: "DM", QuantityFormula: "1", PriceFormula: "1/3"},
		{Type: LineActivity, Ref: "B", Component: "DL", QuantityFormula: "1", PriceFormula: "1/3"},
		{Type: LineOverhead, Ref: "C", Component: "OVH", QuantityFormula: "1", PriceFormula: "1/3"},
	}}
	est, err := EstimateAdHoc(m, EstimateInput{ObjectType: ObjectQuote, ObjectID: "Q", Scale: MoneyScale})
	if err != nil {
		t.Fatalf("EstimateAdHoc: %v", err)
	}
	if est.Total.StringFixed(2) != "1.00" || est.Split.Total().StringFixed(2) != "1.00" {
		t.Errorf("total %s, split total %s, want 1.00 both", est.Total, est.Split.Total())
	}
	if est.Split["DM"].StringFixed(2) != "0.34" || est.Split["DL"].StringFixed(2) != "0.33" {
		t.Errorf("split = %v, want DM 0.34 (residual) and DL 0.33", est.Split)
	}
	jpy, err := EstimateAdHoc(m, EstimateInput{ObjectType: ObjectQuote, ObjectID: "Q", Scale: PostingScale("JPY")})
	if err != nil {
		t.Fatalf("EstimateAdHoc JPY: %v", err)
	}
	if jpy.Total.String() != "1" || jpy.Split.Total().String() != "1" {
		t.Errorf("JPY total %s split %v, want 1 with no decimals", jpy.Total, jpy.Split)
	}
}

func TestEstimateAdHoc_SheetSubtotalsAndHourRates(t *testing.T) {
	sheet := []SheetNode{
		{ID: "MAT", Type: NodeBasis, Basis: []string{"DM"}},
		{ID: "MOH", Type: NodeSurcharge, Component: "IND-MAT", BasisRef: "MAT", Percentage: d("10")},
		{ID: "MCH", Type: NodeRate, Component: "IND-MCH", Rate: d("5"), RateUnit: "Hour"},
	}
	vals := map[string]decimal.Decimal{"weight": d("0.5"), "hours": d("0.5")}
	est, err := EstimateAdHoc(injectionModel(), EstimateInput{ObjectType: ObjectQuote, ObjectID: "Q",
		Values: vals, Prices: injectionPrices(), Sheet: sheet, RoutingHours: d("2"), Scale: MoneyScale})
	if err != nil {
		t.Fatalf("EstimateAdHoc: %v", err)
	}
	if est.Subtotals["MAT"].StringFixed(2) != "6.00" {
		t.Errorf("subtotals = %v, want MAT 6.00", est.Subtotals)
	}
	// 27.00 direct + 0.60 material overhead + 5.00 × 2 hours.
	if est.Split["IND-MCH"].StringFixed(2) != "10.00" || est.Total.StringFixed(2) != "37.60" {
		t.Errorf("split %v total %s, want IND-MCH 10.00 and total 37.60", est.Split, est.Total)
	}
}

func TestEstimateAdHoc_RefusesBadValues(t *testing.T) {
	good := map[string]decimal.Decimal{"weight": d("0.5"), "hours": d("0.5")}
	cases := []struct {
		name  string
		model CostingModel
		vals  map[string]decimal.Decimal
		code  string
	}{
		{"value for a characteristic not in the model", injectionModel(),
			map[string]decimal.Decimal{"weight": d("1"), "hours": d("1"), "colour": d("2")}, CodeEstimateInvalid},
		{"negative quantity", injectionModel(),
			map[string]decimal.Decimal{"weight": d("-1"), "hours": d("1")}, CodeEstimateInvalid},
		{"division by zero", CostingModel{Code: "Z", Characteristics: []Characteristic{{Name: "n"}},
			Lines: []ModelLine{{Type: LineItem, Ref: "RM-GRAN", Component: "DM", QuantityFormula: "1/n"}}},
			map[string]decimal.Decimal{"n": d("0")}, CodeEstimateInvalid},
		{"negative formula price", CostingModel{Code: "P",
			Lines: []ModelLine{{Type: LineOverhead, Ref: "OH", Component: "OVH", QuantityFormula: "1", PriceFormula: "-2"}}},
			good, CodeEstimateInvalid},
		{"overhead without a price formula or a price", CostingModel{Code: "O",
			Lines: []ModelLine{{Type: LineOverhead, Ref: "OH", Component: "OVH", QuantityFormula: "1"}}},
			nil, CodeNoPrice},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EstimateAdHoc(tc.model, EstimateInput{ObjectType: ObjectQuote, ObjectID: "Q",
				Values: tc.vals, Prices: injectionPrices(), Scale: MoneyScale})
			var ce *CodedError
			if !errors.As(err, &ce) || ce.Code != tc.code {
				t.Fatalf("err = %v, want code %s", err, tc.code)
			}
		})
	}
}

func TestCostingModel_CharacteristicNamesMustBeFormulaNames(t *testing.T) {
	m := injectionModel()
	m.Characteristics[1].Name = "material weight"
	if _, err := m.Validate(); err == nil {
		t.Error("a characteristic name a formula cannot read was accepted")
	}
}

// TestEstimateAdHoc_SplitAlwaysSumsToTotal: for any priced lines and currency
// scale, the rounded split adds up to the total, and the total is the rounded
// sum of the line amounts (BR-39.1).
func TestEstimateAdHoc_SplitAlwaysSumsToTotal(t *testing.T) {
	comps := []string{"DM", "DL", "OVH", "SUB"}
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(t, "lines")
		scale := int32(rapid.IntRange(0, 3).Draw(t, "scale"))
		m := CostingModel{Code: "P"}
		for i := 0; i < n; i++ {
			qty := decimal.New(rapid.Int64Range(0, 1_000_000).Draw(t, "qty"), -3)
			price := decimal.New(rapid.Int64Range(0, 10_000_000).Draw(t, "price"), -4)
			m.Lines = append(m.Lines, ModelLine{Type: LineItem, Ref: fmt.Sprintf("R%d", i),
				Component:       comps[rapid.IntRange(0, len(comps)-1).Draw(t, "comp")],
				QuantityFormula: qty.String(), PriceFormula: price.String() + "/3"})
		}
		est, err := EstimateAdHoc(m, EstimateInput{ObjectType: ObjectQuote, ObjectID: "Q", Scale: scale})
		if err != nil {
			t.Fatal(err)
		}
		if !est.Split.Total().Equal(est.Total) {
			t.Fatalf("split %v sums to %s, total %s", est.Split, est.Split.Total(), est.Total)
		}
		sum := decimal.Zero
		for _, ln := range est.Lines {
			sum = sum.Add(ln.Amount)
		}
		if !sum.Round(scale).Equal(est.Total) {
			t.Fatalf("lines sum %s rounds to %s, total %s", sum, sum.Round(scale), est.Total)
		}
	})
}
