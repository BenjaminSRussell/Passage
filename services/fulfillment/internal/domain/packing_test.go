package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestAutoPack_FillsRulesAndClosesOnQuantityAndWeight(t *testing.T) {
	rule := PackRule{ProductID: "fg-100", ContainerType: "C12", QtyPerContainer: d("12"), MaxWeightKg: d("15"), TareKg: d("0.5")}
	tests := []struct {
		name      string
		lines     []PackLine
		rules     []PackRule
		wantQty   []string // per planned container, total units
		wantGross []string
		unpacked  int
		wantErr   error
	}{
		{
			name:      "AC-28.11 30 units at 1 kg pack as 12, 12, 6",
			lines:     []PackLine{{ShipmentLineID: "l1", ProductID: "fg-100", Qty: d("30"), UnitWeightKg: d("1")}},
			rules:     []PackRule{rule},
			wantQty:   []string{"12", "12", "6"},
			wantGross: []string{"12.5", "12.5", "6.5"},
		},
		{
			name:      "weight closes the container before the quantity limit",
			lines:     []PackLine{{ShipmentLineID: "l1", ProductID: "fg-100", Qty: d("10"), UnitWeightKg: d("2")}},
			rules:     []PackRule{rule},
			wantQty:   []string{"7", "3"},
			wantGross: []string{"14.5", "6.5"},
		},
		{
			name:     "no rule leaves the line unpacked",
			lines:    []PackLine{{ShipmentLineID: "l9", ProductID: "other", Qty: d("3")}},
			rules:    []PackRule{rule},
			unpacked: 1,
		},
		{
			name:    "a single unit above max weight is rejected",
			lines:   []PackLine{{ShipmentLineID: "l1", ProductID: "fg-100", Qty: d("1"), UnitWeightKg: d("20")}},
			rules:   []PackRule{rule},
			wantErr: ErrContainerWeight,
		},
		{
			name:    "serial-tracked units cannot be split",
			lines:   []PackLine{{ShipmentLineID: "l1", ProductID: "fg-100", Qty: d("2.5"), Serial: true}},
			rules:   []PackRule{rule},
			wantErr: ErrSerialFraction,
		},
		{
			name:    "a rule with zero quantity per container is invalid",
			lines:   []PackLine{{ShipmentLineID: "l1", ProductID: "fg-100", Qty: d("1")}},
			rules:   []PackRule{{ProductID: "fg-100", ContainerType: "C12", QtyPerContainer: d("0"), MaxWeightKg: d("15")}},
			wantErr: ErrRuleInvalid,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := AutoPack(tc.lines, tc.rules, "")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if len(plan.Unpacked) != tc.unpacked {
				t.Fatalf("unpacked = %d, want %d", len(plan.Unpacked), tc.unpacked)
			}
			if len(plan.Containers) != len(tc.wantQty) {
				t.Fatalf("containers = %d, want %d", len(plan.Containers), len(tc.wantQty))
			}
			for i, c := range plan.Containers {
				total := decimal.Zero
				for _, l := range c.Lines {
					total = total.Add(l.Qty)
				}
				if !total.Equal(d(tc.wantQty[i])) {
					t.Errorf("container %d qty = %s, want %s", i, total, tc.wantQty[i])
				}
				if !c.GrossKg.Equal(d(tc.wantGross[i])) {
					t.Errorf("container %d gross = %s, want %s", i, c.GrossKg, tc.wantGross[i])
				}
			}
		})
	}
}

func TestAutoPack_CustomerRuleWinsOverGeneral(t *testing.T) {
	general := PackRule{ProductID: "p", ContainerType: "CARTON", QtyPerContainer: d("24"), MaxWeightKg: d("20")}
	specific := PackRule{ProductID: "p", CustomerID: "c1", ContainerType: "C12", QtyPerContainer: d("12"), MaxWeightKg: d("20")}
	lines := []PackLine{{ShipmentLineID: "l1", ProductID: "p", Qty: d("12")}}

	plan, err := AutoPack(lines, []PackRule{general, specific}, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Containers[0].ContainerType != "C12" {
		t.Fatalf("container = %s, want C12 for customer c1", plan.Containers[0].ContainerType)
	}
	plan, _ = AutoPack(lines, []PackRule{general, specific}, "c2")
	if plan.Containers[0].ContainerType != "CARTON" {
		t.Fatalf("container = %s, want CARTON for other customers", plan.Containers[0].ContainerType)
	}
}

func TestAutoPack_SpecificRuleLinesGoFirst(t *testing.T) {
	general := PackRule{ProductID: "g", ContainerType: "G", QtyPerContainer: d("10"), MaxWeightKg: d("50")}
	specific := PackRule{ProductID: "s", CustomerID: "c1", ContainerType: "S", QtyPerContainer: d("10"), MaxWeightKg: d("50")}
	lines := []PackLine{
		{ShipmentLineID: "general", ProductID: "g", Qty: d("1")},
		{ShipmentLineID: "specific", ProductID: "s", Qty: d("1")},
	}
	plan, err := AutoPack(lines, []PackRule{general, specific}, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Containers[0].ContainerType != "S" {
		t.Fatalf("first container = %s, want the specific rule's S", plan.Containers[0].ContainerType)
	}
}

func TestPackagingFees_AC28_12(t *testing.T) {
	rates := map[string]FeeRate{"CARDBOARD": {RatePerKg: d("0.50"), Currency: "EUR"}}
	uses := []PackagingUse{{Material: "CARDBOARD", WeightKg: d("20")}}
	fees, err := PackagingFees(uses, rates, "EUR")
	if err != nil {
		t.Fatal(err)
	}
	if len(fees) != 1 || !fees[0].Amount.Equal(d("10.00")) {
		t.Fatalf("fees = %+v, want one line of 10.00 EUR", fees)
	}
}

func TestPackagingFees_RoundingAndErrors(t *testing.T) {
	tests := []struct {
		name     string
		uses     []PackagingUse
		rates    map[string]FeeRate
		currency string
		want     string
		wantErr  error
	}{
		{
			name:     "half-even at the minor unit: 0.125 rounds to 0.12",
			uses:     []PackagingUse{{Material: "PLASTIC", WeightKg: d("0.25")}},
			rates:    map[string]FeeRate{"PLASTIC": {RatePerKg: d("0.5"), Currency: "EUR"}},
			currency: "EUR", want: "0.12",
		},
		{
			name:     "half-even rounds 0.375 up to 0.38",
			uses:     []PackagingUse{{Material: "PLASTIC", WeightKg: d("0.75")}},
			rates:    map[string]FeeRate{"PLASTIC": {RatePerKg: d("0.5"), Currency: "EUR"}},
			currency: "EUR", want: "0.38",
		},
		{
			name:     "weights of one material are summed before rating",
			uses:     []PackagingUse{{Material: "WOOD", WeightKg: d("1")}, {Material: "WOOD", WeightKg: d("2")}},
			rates:    map[string]FeeRate{"WOOD": {RatePerKg: d("1"), Currency: "EUR"}},
			currency: "EUR", want: "3.00",
		},
		{
			name:     "missing rate is FUL-0014",
			uses:     []PackagingUse{{Material: "GLASS", WeightKg: d("1")}},
			rates:    map[string]FeeRate{},
			currency: "EUR", wantErr: ErrRateMissing,
		},
		{
			name:     "rate in another currency is rejected",
			uses:     []PackagingUse{{Material: "GLASS", WeightKg: d("1")}},
			rates:    map[string]FeeRate{"GLASS": {RatePerKg: d("1"), Currency: "USD"}},
			currency: "EUR", wantErr: ErrRateCurrency,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fees, err := PackagingFees(tc.uses, tc.rates, tc.currency)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.want != "" && (len(fees) != 1 || fees[0].Amount.StringFixed(2) != tc.want) {
				t.Fatalf("fees = %+v, want %s", fees, tc.want)
			}
		})
	}
}

func TestApplyBalance_AC28_13(t *testing.T) {
	bal := d("10").Sub(d("4")) // 10 pallets out, 4 back
	if !bal.Equal(d("6")) {
		t.Fatalf("balance = %s, want 6", bal)
	}
	if _, err := ApplyBalance(bal, d("-7")); !errors.Is(err, ErrRTPNegative) {
		t.Fatalf("err = %v, want ErrRTPNegative for a return of 7", err)
	}
	next, err := ApplyBalance(bal, d("-6"))
	if err != nil || !next.IsZero() {
		t.Fatalf("return of 6 = %s, %v; want 0", next, err)
	}
}

// Auto-pack invariants (BR-28.11): every unit of a ruled line lands in exactly
// one container, no container exceeds its quantity per container or its max
// gross weight, gross = tare + Σ qty × unit weight, and integer quantities of
// serial-tracked lines are never split into fractions.
func TestAutoPack_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		perContainer := decimal.NewFromInt(int64(rapid.IntRange(1, 20).Draw(t, "perContainer")))
		tare := decimal.New(int64(rapid.IntRange(0, 20).Draw(t, "tareTenths")), -1)
		unitKg := decimal.New(int64(rapid.IntRange(0, 30).Draw(t, "unitTenths")), -1)
		maxKg := tare.Add(unitKg.Mul(decimal.NewFromInt(int64(rapid.IntRange(1, 25).Draw(t, "unitsByWeight"))))).Add(d("0.05"))
		serial := rapid.Bool().Draw(t, "serial")
		qty := decimal.NewFromInt(int64(rapid.IntRange(1, 200).Draw(t, "qty")))
		rule := PackRule{ProductID: "p", ContainerType: "C", QtyPerContainer: perContainer, MaxWeightKg: maxKg, TareKg: tare}
		plan, err := AutoPack([]PackLine{{ShipmentLineID: "l", ProductID: "p", Qty: qty, UnitWeightKg: unitKg, Serial: serial}}, []PackRule{rule}, "")
		if err != nil {
			t.Fatalf("AutoPack: %v", err)
		}
		total := decimal.Zero
		for i, c := range plan.Containers {
			units := decimal.Zero
			for _, l := range c.Lines {
				if !l.Qty.IsPositive() || (serial && !l.Qty.Equal(l.Qty.Floor())) {
					t.Fatalf("container %d line qty %s", i, l.Qty)
				}
				units = units.Add(l.Qty)
			}
			if units.GreaterThan(perContainer) {
				t.Fatalf("container %d holds %s > %s per container", i, units, perContainer)
			}
			if gross := tare.Add(units.Mul(unitKg)); !c.GrossKg.Equal(gross) || c.GrossKg.GreaterThan(maxKg) {
				t.Fatalf("container %d gross %s, want %s within %s", i, c.GrossKg, gross, maxKg)
			}
			total = total.Add(units)
		}
		if !total.Equal(qty) {
			t.Fatalf("packed %s of %s", total, qty)
		}
	})
}
