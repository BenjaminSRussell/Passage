package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func suppDec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func suppStrp(s string) *string { return &s }

func TestEvaluateSupplementary(t *testing.T) {
	manual := SupplementaryRule{ID: "r1", ParentProductID: "FG-100", SupplementaryProduct: "MANUAL-EN",
		Requirement: SuppMandatory, BaseQuantity: suppDec("10"), QuantityPerBase: suppDec("1"), Pricing: SuppFree}
	c9 := manual
	c9.ID, c9.CustomerID = "r2", suppStrp("C9")
	c9.Requirement, c9.Pricing, c9.UnitPrice = SuppOptional, SuppPriced, suppDec("2.50")
	cases := []struct {
		name     string
		rules    []SupplementaryRule
		customer string
		qty      string
		want     []SupplementaryLine
	}{
		{"AC-27.10: 25 units give 2 free mandatory", []SupplementaryRule{manual}, "C1", "25",
			[]SupplementaryLine{{RuleID: "r1", ProductID: "MANUAL-EN", Requirement: SuppMandatory,
				Quantity: suppDec("2"), UnitPrice: decimal.Zero, SupplementaryOf: "FG-100"}}},
		{"below base quantity gives nothing", []SupplementaryRule{manual}, "C1", "9", nil},
		{"fractional parent floors to whole multiples", []SupplementaryRule{manual}, "C1", "19.999999",
			[]SupplementaryLine{{RuleID: "r1", ProductID: "MANUAL-EN", Requirement: SuppMandatory,
				Quantity: suppDec("1"), UnitPrice: decimal.Zero, SupplementaryOf: "FG-100"}}},
		{"customer-specific rule applies to its customer", []SupplementaryRule{c9}, "C9", "10",
			[]SupplementaryLine{{RuleID: "r2", ProductID: "MANUAL-EN", Requirement: SuppOptional,
				Quantity: suppDec("1"), UnitPrice: suppDec("2.50"), SupplementaryOf: "FG-100"}}},
		{"customer-specific rule skipped for other customers", []SupplementaryRule{c9}, "C1", "10", nil},
		{"large parent quantity scales linearly", []SupplementaryRule{manual}, "C1", "100",
			[]SupplementaryLine{{RuleID: "r1", ProductID: "MANUAL-EN", Requirement: SuppMandatory,
				Quantity: suppDec("10"), UnitPrice: decimal.Zero, SupplementaryOf: "FG-100"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := EvaluateSupplementary(tc.rules, "FG-100", tc.customer, suppDec(tc.qty))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d lines, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i].RuleID != tc.want[i].RuleID || got[i].ProductID != tc.want[i].ProductID ||
					got[i].Requirement != tc.want[i].Requirement || !got[i].Quantity.Equal(tc.want[i].Quantity) ||
					!got[i].UnitPrice.Equal(tc.want[i].UnitPrice) || got[i].SupplementaryOf != tc.want[i].SupplementaryOf {
					t.Fatalf("line %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestEvaluateSupplementary_CustomerRuleBeatsAllCustomersRule(t *testing.T) {
	all := SupplementaryRule{ID: "all", ParentProductID: "FG-100", SupplementaryProduct: "MANUAL-EN", Unit: "EA",
		Requirement: SuppMandatory, BaseQuantity: suppDec("10"), QuantityPerBase: suppDec("1"), Pricing: SuppFree}
	mine := all
	mine.ID, mine.CustomerID, mine.QuantityPerBase = "c9", suppStrp("C9"), suppDec("2")
	other := all
	other.ID, other.SupplementaryProduct = "cable", "CABLE"

	got, err := EvaluateSupplementary([]SupplementaryRule{all, mine, other}, "FG-100", "C9", suppDec("20"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].RuleID != "c9" || !got[0].Quantity.Equal(suppDec("4")) || got[1].RuleID != "cable" || got[1].Unit != "EA" {
		t.Fatalf("C9 must get its own MANUAL-EN rule (4) and the shared CABLE rule, not both MANUAL-EN rules: %+v", got)
	}
	got, err = EvaluateSupplementary([]SupplementaryRule{all, mine, other}, "FG-100", "C1", suppDec("20"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].RuleID != "all" || got[1].RuleID != "cable" {
		t.Fatalf("other customers get the all-customers rules: %+v", got)
	}
	got, err = EvaluateSupplementary([]SupplementaryRule{all}, "FG-200", "C1", suppDec("20"))
	if err != nil || len(got) != 0 {
		t.Fatalf("rules of another parent product must not apply: %v %+v", err, got)
	}
}

func TestEvaluateSupplementary_RejectsNonPositiveParent(t *testing.T) {
	if _, err := EvaluateSupplementary(nil, "FG-100", "C1", suppDec("0")); err != ErrNoParent {
		t.Fatalf("got %v", err)
	}
}

func TestValidateSupplementaryRule(t *testing.T) {
	ok := SupplementaryRule{ParentProductID: "FG-100", SupplementaryProduct: "MANUAL-EN", Unit: "EA", Requirement: SuppOptional,
		BaseQuantity: suppDec("10"), QuantityPerBase: suppDec("1"), Pricing: SuppFree}
	if f := ValidateSupplementaryRule(ok); len(f) != 0 {
		t.Fatalf("valid rule rejected: %v", f)
	}
	bad := ok
	bad.Requirement, bad.BaseQuantity, bad.UnitPrice = "SOMETIMES", suppDec("0"), suppDec("1")
	bad.SupplementaryProduct = "FG-100"
	f := ValidateSupplementaryRule(bad)
	for _, key := range []string{"requirement", "baseQuantity", "unitPrice", "supplementaryProductId"} {
		if _, ok := f[key]; !ok {
			t.Errorf("missing field error %q in %v", key, f)
		}
	}
	priced := ok
	priced.Pricing, priced.Unit, priced.QuantityPerBase = SuppPriced, "", suppDec("0.0000001")
	f = ValidateSupplementaryRule(priced)
	for _, key := range []string{"unitPrice", "unit", "quantityPerBase"} {
		if _, ok := f[key]; !ok {
			t.Errorf("PRICED rule without price, unit or with 7 decimals: missing %q in %v", key, f)
		}
	}
}
