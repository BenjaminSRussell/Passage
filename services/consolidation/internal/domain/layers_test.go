package domain

import (
	"testing"
)

// TestUnrealizedProfitLayers_AC40_12 is AC-40.12: 40 units remaining from an IC
// receipt at legal cost 120.00 and group cost 100.00 give 800.00 to eliminate.
func TestUnrealizedProfitLayers_AC40_12(t *testing.T) {
	got, err := UnrealizedProfitLayers([]ICLayer{
		{ID: "RCPT-1", Quantity: d("40"), LegalUnitCost: d("120"), GroupUnitCost: d("100"), GroupCostKnown: true},
	}, "EUR", d("10"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Total.Equal(d("800")) {
		t.Fatalf("total = %s, want 800", got.Total)
	}
	if len(got.Layers) != 1 || got.Layers[0].Basis != BasisGroupCost || got.Layers[0].ID != "RCPT-1" {
		t.Fatalf("layers = %+v", got.Layers)
	}
	if got.FallbackCount != 0 || got.Warning() != nil {
		t.Fatalf("fallback count = %d, warning %v; want none", got.FallbackCount, got.Warning())
	}
}

type layerCase struct {
	name      string
	layers    []ICLayer
	margin    string
	want      string
	fallbacks int
	invalid   bool
}

// The margin fallback is 40 × 120 × 10 % = 480, which is the CNS-0908 warning case.
// Mixed layers: 10 × (50 − 45) = 50 by group cost, plus 20 × 50 × 5 % = 50 by fallback.
var layerCases = []layerCase{
	{name: "margin fallback when group cost is unknown", layers: []ICLayer{{Quantity: d("40"), LegalUnitCost: d("120")}}, margin: "10", want: "480", fallbacks: 1},
	{name: "layers add up and mix bases", layers: []ICLayer{{Quantity: d("10"), LegalUnitCost: d("50"), GroupUnitCost: d("45"), GroupCostKnown: true}, {Quantity: d("20"), LegalUnitCost: d("50")}}, margin: "5", want: "100", fallbacks: 1},
	{name: "zero quantity contributes nothing", layers: []ICLayer{{Quantity: d("0"), LegalUnitCost: d("10"), GroupUnitCost: d("1"), GroupCostKnown: true}}, margin: "0", want: "0"},
	{name: "legal cost below group cost is an unrealized loss", layers: []ICLayer{{Quantity: d("5"), LegalUnitCost: d("90"), GroupUnitCost: d("100"), GroupCostKnown: true}}, margin: "0", want: "-50"},
	{name: "negative group cost is invalid", layers: []ICLayer{{Quantity: d("1"), LegalUnitCost: d("10"), GroupUnitCost: d("-1"), GroupCostKnown: true}}, margin: "0", invalid: true},
	{name: "negative quantity is invalid", layers: []ICLayer{{Quantity: d("-1"), LegalUnitCost: d("10")}}, margin: "0", invalid: true},
	{name: "margin above 100 percent is invalid", margin: "101", invalid: true},
}

func TestUnrealizedProfitLayers(t *testing.T) {
	for _, tc := range layerCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := UnrealizedProfitLayers(tc.layers, "EUR", d(tc.margin))
			if tc.invalid {
				if err == nil {
					t.Fatal("want an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !got.Total.Equal(d(tc.want)) {
				t.Fatalf("total = %s, want %s", got.Total, tc.want)
			}
			if got.FallbackCount != tc.fallbacks {
				t.Fatalf("fallbacks = %d, want %d", got.FallbackCount, tc.fallbacks)
			}
			if w := got.Warning(); (w != nil) != (tc.fallbacks > 0) || (w != nil && w.Code != CodeGroupCostUnknown) {
				t.Fatalf("warning = %v with %d fallbacks", w, tc.fallbacks)
			}
		})
	}
}
