package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func TestSupplementaryQuantity(t *testing.T) {
	cases := []struct{ parent, base, per, want string }{
		{"25", "10", "1", "2"},       // AC-27.10
		{"10", "10", "1", "1"},       // exactly one base quantity
		{"9.999999", "10", "1", "0"}, // below the base: the line is removed (BR-27.14)
		{"30", "10", "2.5", "7.5"},   // fractional quantity per base
		{"7", "2.5", "1", "2"},       // fractional base
		{"0", "10", "1", "0"},        // no parent quantity
		{"25", "0", "1", "0"},        // a zero base never divides
	}
	for _, c := range cases {
		got := SupplementaryQuantity(suppDec(c.parent), suppDec(c.base), suppDec(c.per))
		if !got.Equal(suppDec(c.want)) {
			t.Errorf("SupplementaryQuantity(%s, %s, %s) = %s, want %s", c.parent, c.base, c.per, got, c.want)
		}
	}
}

func TestMandatorySupplementary(t *testing.T) {
	manual := SupplementaryRule{ID: "manual", ParentProductID: "FG-100", SupplementaryProduct: "MANUAL-EN", Unit: "EA",
		Requirement: SuppMandatory, BaseQuantity: suppDec("10"), QuantityPerBase: suppDec("1"), Pricing: SuppFree}
	poster := SupplementaryRule{ID: "poster", ParentProductID: "FG-100", SupplementaryProduct: "POSTER", Unit: "EA",
		Requirement: SuppOptional, BaseQuantity: suppDec("1"), QuantityPerBase: suppDec("1"), Pricing: SuppPriced, UnitPrice: suppDec("2.50")}
	cable := SupplementaryRule{ID: "cable", ParentProductID: "FG-100", SupplementaryProduct: "CABLE", Unit: "M",
		Requirement: SuppMandatory, BaseQuantity: suppDec("5"), QuantityPerBase: suppDec("2"), Pricing: SuppPriced, UnitPrice: suppDec("3.25")}
	rules := []SupplementaryRule{manual, poster, cable}

	got, err := MandatorySupplementary(rules, "C1", []SupplementaryParent{
		{LineNo: 1, ProductID: "FG-100", Quantity: suppDec("25")},
		{LineNo: 2, ProductID: "FG-200", Quantity: suppDec("50")},
		{LineNo: 3, ProductID: "FG-100", Quantity: suppDec("4")},
		{LineNo: 4, ProductID: "FG-100", Quantity: suppDec("10")},
	})
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		parent           int
		rule, unit       string
		qty, price, base string
	}
	wants := []want{
		{1, "manual", "EA", "2", "0", "10"},
		{1, "cable", "M", "10", "3.25", "5"},
		{4, "manual", "EA", "1", "0", "10"},
		{4, "cable", "M", "4", "3.25", "5"},
	}
	if len(got) != len(wants) {
		t.Fatalf("got %d lines, want %d (optional POSTER, other product FG-200 and the 4-unit line add nothing): %+v", len(got), len(wants), got)
	}
	for i, w := range wants {
		g := got[i]
		if g.ParentLineNo != w.parent || g.RuleID != w.rule || g.Unit != w.unit || !g.Quantity.Equal(suppDec(w.qty)) ||
			!g.UnitPrice.Equal(suppDec(w.price)) || !g.BaseQuantity.Equal(suppDec(w.base)) || g.Requirement != SuppMandatory {
			t.Errorf("line %d = %+v, want %+v", i, g, w)
		}
	}
}

func TestMandatorySupplementary_RejectsNonPositiveParent(t *testing.T) {
	rules := []SupplementaryRule{{ID: "m", ParentProductID: "P", SupplementaryProduct: "Q", Requirement: SuppMandatory,
		BaseQuantity: decimal.NewFromInt(1), QuantityPerBase: decimal.NewFromInt(1), Pricing: SuppFree}}
	_, err := MandatorySupplementary(rules, "c", []SupplementaryParent{{LineNo: 1, ProductID: "P", Quantity: decimal.Zero}})
	if !errors.Is(err, ErrNoParent) {
		t.Fatalf("err = %v, want ErrNoParent", err)
	}
}

// TestMandatorySupplementary_CustomerOptionalReplacesSharedMandatory keeps
// commit consistent with the evaluation: a customer's own OPTIONAL rule for
// an item replaces the all-customers MANDATORY rule, so nothing is added for
// that customer while other customers still get the item.
func TestMandatorySupplementary_CustomerOptionalReplacesSharedMandatory(t *testing.T) {
	shared := SupplementaryRule{ID: "shared", ParentProductID: "FG-100", SupplementaryProduct: "MANUAL-EN", Unit: "EA",
		Requirement: SuppMandatory, BaseQuantity: suppDec("10"), QuantityPerBase: suppDec("1"), Pricing: SuppFree}
	own := shared
	own.ID, own.CustomerID, own.Requirement = "own", suppStrp("C9"), SuppOptional
	parents := []SupplementaryParent{{LineNo: 1, ProductID: "FG-100", Quantity: suppDec("25")}}

	got, err := MandatorySupplementary([]SupplementaryRule{shared, own}, "C9", parents)
	if err != nil || len(got) != 0 {
		t.Fatalf("C9 opted for the optional prompt; got %+v err %v", got, err)
	}
	got, err = MandatorySupplementary([]SupplementaryRule{shared, own}, "C1", parents)
	if err != nil || len(got) != 1 || got[0].RuleID != "shared" {
		t.Fatalf("other customers keep the mandatory item; got %+v err %v", got, err)
	}
}
