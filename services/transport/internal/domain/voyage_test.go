package domain

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func validVoyage() Voyage {
	return Voyage{
		Reference: "MAEU-2611E",
		Currency:  "USD",
		Legs:      []Leg{{From: "CNSHA", To: "USLAX", Mode: "OCEAN"}, {From: "USLAX", To: "DC-RENO", Mode: "ROAD"}},
		Containers: []Container{
			{Number: "CSQU3054383", Type: "40HC", Seal: "778812"},
			{Number: "MSCU1234566", Type: "20GP"},
		},
		Folios:    []Folio{{FolioNo: "F-1", Forwarder: "Kuehne", Containers: []string{"CSQU3054383", "MSCU1234566"}}},
		CostLines: []VoyageCostLine{{CostType: "FREIGHT", Amount: dec("1000.00"), AllocationMethod: MethodWeight}},
		ReceiptLines: []VoyageReceiptLine{
			{Line: 1, Value: dec("100"), Weight: dec("1"), Quantity: dec("10")},
			{Line: 2, Value: dec("100"), Weight: dec("1"), Quantity: dec("10")},
			{Line: 3, Value: dec("250"), Weight: dec("1"), Quantity: dec("20")},
		},
	}
}

func TestNormalizeVoyage(t *testing.T) {
	v := validVoyage()
	v.ID, v.Status, v.Allocations = "client-id", VoyagePosted, []Allocation{{Line: 1, Amount: dec("1")}}
	v.Currency, v.Containers[0].Number, v.Legs[1].Mode = " usd ", " csqu3054383 ", "road"
	v.CostLines[0].AllocationMethod = "weight"
	n := NormalizeVoyage(v)
	if n.ID != "" || n.Status != "" || n.Allocations != nil {
		t.Fatalf("server-owned fields kept: %+v", n)
	}
	if n.Currency != "USD" || n.Containers[0].Number != "CSQU3054383" || n.Legs[1].Mode != "ROAD" || n.CostLines[0].AllocationMethod != "WEIGHT" {
		t.Fatalf("not normalised: %+v", n)
	}
	if n.Legs[0].Seq != 1 || n.Legs[1].Seq != 2 {
		t.Fatalf("legs not numbered: %+v", n.Legs)
	}
}

func TestValidateVoyage(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Voyage)
		field  string
	}{
		{"valid", func(*Voyage) {}, ""},
		{"no reference", func(v *Voyage) { v.Reference = "" }, "reference"},
		{"bad currency", func(v *Voyage) { v.Currency = "US" }, "currency"},
		{"no legs", func(v *Voyage) { v.Legs = nil }, "legs"},
		{"leg without destination", func(v *Voyage) { v.Legs[0].To = "" }, "legs[0]"},
		{"unknown leg mode", func(v *Voyage) { v.Legs[1].Mode = "TELEPORT" }, "legs[1]"},
		{"duplicate container", func(v *Voyage) { v.Containers[1].Number = "CSQU3054383" }, "containers[1]"},
		{"unknown container type", func(v *Voyage) { v.Containers[0].Type = "99XX" }, "containers[0]"},
		{"folio refers to a missing container", func(v *Voyage) { v.Folios[0].Containers = []string{"TGHU0000013"} }, "folios[0]"},
		{"folio without forwarder", func(v *Voyage) { v.Folios[0].Forwarder = "" }, "folios[0]"},
		{"no cost lines", func(v *Voyage) { v.CostLines = nil }, "costLines"},
		{"negative cost", func(v *Voyage) { v.CostLines[0].Amount = dec("-1") }, "costLines[0]"},
		{"sub-cent cost", func(v *Voyage) { v.CostLines[0].Amount = dec("10.005") }, "costLines[0]"},
		{"unknown method", func(v *Voyage) { v.CostLines[0].AllocationMethod = "PRIORITY" }, "costLines[0]"},
		{"method with zero basis", func(v *Voyage) { v.CostLines[0].AllocationMethod = MethodVolume }, "costLines[0]"},
		{"no receipt lines", func(v *Voyage) { v.ReceiptLines = nil }, "receiptLines"},
		{"duplicate receipt line", func(v *Voyage) { v.ReceiptLines[1].Line = 1 }, "receiptLines[1]"},
		{"receipt line zero", func(v *Voyage) { v.ReceiptLines[0].Line = 0 }, "receiptLines[0]"},
		{"negative weight", func(v *Voyage) { v.ReceiptLines[2].Weight = dec("-1") }, "receiptLines[2]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := validVoyage()
			tc.mutate(&v)
			errs := ValidateVoyage(v)
			if tc.field == "" {
				if len(errs) != 0 {
					t.Fatalf("unexpected errors %v", errs)
				}
				return
			}
			if _, ok := errs[tc.field]; !ok {
				t.Fatalf("errors %v, want field %q", errs, tc.field)
			}
		})
	}
}

// TestContainerErrors_AC3410 is AC-34.10: CSQU3054383 is accepted and
// CSQU3054384 is rejected.
func TestContainerErrors_AC3410(t *testing.T) {
	v := validVoyage()
	if errs := ContainerErrors(v); len(errs) != 0 {
		t.Fatalf("valid numbers rejected: %v", errs)
	}
	v.Containers[0].Number = "CSQU3054384"
	errs := ContainerErrors(v)
	if len(errs) != 1 || errs["containers[0].number"] == "" {
		t.Fatalf("errors = %v, want containers[0].number", errs)
	}
}

// TestVoyageAllocate_AC346 is AC-34.6 through a voyage: freight $1,000.00 by
// weight over three 1 kg lines gives 333.33 / 333.33 / 333.34, the residual on
// the highest-value line, and a second cost line adds on top.
func TestVoyageAllocate_AC346(t *testing.T) {
	v := validVoyage()
	v.CostLines = append(v.CostLines, VoyageCostLine{CostType: "DUTY", Amount: dec("80.00"), AllocationMethod: MethodQuantity})
	posted, err := v.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if posted.Status != VoyagePosted || len(posted.CostAllocations) != 2 || len(posted.Allocations) != 3 {
		t.Fatalf("posted = %+v", posted)
	}
	freight := posted.CostAllocations[0]
	for i, want := range []string{"333.33", "333.33", "333.34"} {
		if !freight.Shares[i].Amount.Equal(dec(want)) {
			t.Fatalf("freight share %d = %s, want %s", i, freight.Shares[i].Amount, want)
		}
	}
	if freight.ResidualLine != 3 || !freight.Residual.Equal(dec("0.01")) {
		t.Fatalf("residual %s on line %d", freight.Residual, freight.ResidualLine)
	}
	// Duty by quantity 10/10/20 of 80.00: 20 / 20 / 40.
	for i, want := range []string{"353.33", "353.33", "373.34"} {
		if !posted.Allocations[i].Amount.Equal(dec(want)) {
			t.Fatalf("receipt line %d total = %s, want %s", posted.Allocations[i].Line, posted.Allocations[i].Amount, want)
		}
	}
	if v.Status != "" || v.Allocations != nil {
		t.Fatal("Allocate changed its receiver")
	}
}

// TestVoyageAllocate_PropertySumsToCostTotal: for random cost lines and methods,
// each cost line's shares sum to its amount and the receipt-line totals sum to
// the voyage cost total, to the cent.
func TestVoyageAllocate_PropertySumsToCostTotal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(t, "receipt lines")
		v := Voyage{}
		for i := 0; i < n; i++ {
			draw := func(name string) decimal.Decimal {
				return decimal.New(int64(rapid.IntRange(1, 100000).Draw(t, fmt.Sprintf("%s%d", name, i))), -2)
			}
			v.ReceiptLines = append(v.ReceiptLines, VoyageReceiptLine{Line: i + 1, Value: draw("value"), Weight: draw("weight"), Volume: draw("volume"), Quantity: draw("qty")})
		}
		for i := rapid.IntRange(1, 4).Draw(t, "cost lines"); i > 0; i-- {
			v.CostLines = append(v.CostLines, VoyageCostLine{
				CostType:         "C",
				Amount:           decimal.New(int64(rapid.IntRange(0, 10000000).Draw(t, fmt.Sprintf("cost%d", i))), -2),
				AllocationMethod: rapid.SampledFrom(AllocationMethods).Draw(t, fmt.Sprintf("method%d", i)),
			})
		}
		posted, err := v.Allocate()
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range posted.CostAllocations {
			sum := decimal.Zero
			for _, s := range c.Shares {
				sum = sum.Add(s.Amount)
			}
			if !sum.Equal(c.Amount) {
				t.Fatalf("%s shares sum to %s, want %s", c.AllocationMethod, sum, c.Amount)
			}
		}
		total := decimal.Zero
		for _, a := range posted.Allocations {
			total = total.Add(a.Amount)
		}
		if !total.Equal(v.CostTotal()) {
			t.Fatalf("allocated %s, cost total %s", total, v.CostTotal())
		}
	})
}
