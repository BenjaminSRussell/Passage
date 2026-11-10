package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func taxOf(t *testing.T, got []TaxReversal) decimal.Decimal {
	t.Helper()
	total := decimal.Zero
	for _, r := range got {
		total = total.Add(r.Tax)
	}
	return total
}

func TestReverseTax_FullReturnReversesExactlyTheBilledTax(t *testing.T) {
	lines := []TaxBasisLine{{LineNo: 1, ProductID: "A", TaxCode: "DE19", Qty: dec("10"), Tax: dec("190.00")}}
	got := ReverseTax("USD", lines, map[string]decimal.Decimal{"A": dec("10")})
	if len(got) != 1 || !got[0].Tax.Equal(dec("190.00")) || got[0].TaxCode != "DE19" || got[0].InvoiceLineNo != 1 || !got[0].Qty.Equal(dec("10")) {
		t.Fatalf("full return: %+v", got)
	}
}

func TestReverseTax_PartialReturnIsProRataAndRoundsHalfEvenToTheCent(t *testing.T) {
	lines := []TaxBasisLine{{LineNo: 1, ProductID: "A", TaxCode: "DE7", Qty: dec("3"), Tax: dec("7.00")}}
	cases := []struct {
		qty, want string
	}{{"1", "2.33"}, {"2", "4.67"}}
	for _, c := range cases {
		got := ReverseTax("USD", lines, map[string]decimal.Decimal{"A": dec(c.qty)})
		if len(got) != 1 || !got[0].Tax.Equal(dec(c.want)) {
			t.Fatalf("%s of 3: want %s got %+v", c.qty, c.want, got)
		}
	}
	half := []TaxBasisLine{{LineNo: 1, ProductID: "A", TaxCode: "X", Qty: dec("2"), Tax: dec("0.05")}}
	if got := ReverseTax("USD", half, map[string]decimal.Decimal{"A": dec("1")}); !got[0].Tax.Equal(dec("0.02")) {
		t.Fatalf("0.025 rounds half even to 0.02: %+v", got)
	}
	whole := []TaxBasisLine{{LineNo: 1, ProductID: "A", TaxCode: "X", Qty: dec("2"), Tax: dec("500")}}
	if got := ReverseTax("JPY", whole, map[string]decimal.Decimal{"A": dec("1")}); !got[0].Tax.Equal(dec("250")) {
		t.Fatalf("JPY has no minor unit: %+v", got)
	}
}

func TestReverseTax_SuccessivePartialReturnsSumToTheBilledTax(t *testing.T) {
	line := TaxBasisLine{LineNo: 1, ProductID: "A", TaxCode: "DE7", Qty: dec("3"), Tax: dec("7.00")}
	total := decimal.Zero
	for i := 0; i < 3; i++ {
		got := ReverseTax("USD", []TaxBasisLine{line}, map[string]decimal.Decimal{"A": dec("1")})
		if len(got) != 1 {
			t.Fatalf("return %d: %+v", i+1, got)
		}
		line.ReturnedQty, line.ReturnedTax = line.ReturnedQty.Add(got[0].Qty), line.ReturnedTax.Add(got[0].Tax)
		total = total.Add(got[0].Tax)
	}
	if !total.Equal(dec("7.00")) || !line.ReturnedTax.Equal(dec("7.00")) {
		t.Fatalf("three single-unit returns of 7.00: %s", total)
	}
	if got := ReverseTax("USD", []TaxBasisLine{line}, map[string]decimal.Decimal{"A": dec("1")}); len(got) != 0 {
		t.Fatalf("nothing is left to reverse: %+v", got)
	}
}

func TestReverseTax_MultiRateLinesKeepTheirOwnCode(t *testing.T) {
	lines := []TaxBasisLine{
		{LineNo: 1, ProductID: "A", TaxCode: "DE19", Qty: dec("10"), Tax: dec("190.00")},
		{LineNo: 2, ProductID: "B", TaxCode: "DE7", Qty: dec("4"), Tax: dec("28.00")},
		{LineNo: 3, ProductID: "C", TaxCode: "EXEMPT", Qty: dec("1"), Tax: dec("0")},
	}
	got := ReverseTax("USD", lines, map[string]decimal.Decimal{"A": dec("3"), "B": dec("1"), "C": dec("1")})
	if len(got) != 3 || got[0].TaxCode != "DE19" || !got[0].Tax.Equal(dec("57.00")) ||
		got[1].TaxCode != "DE7" || !got[1].Tax.Equal(dec("7.00")) || got[2].TaxCode != "EXEMPT" || !got[2].Tax.IsZero() {
		t.Fatalf("multi-rate: %+v", got)
	}
	if !taxOf(t, got).Equal(dec("64.00")) {
		t.Fatalf("total %s", taxOf(t, got))
	}
}

func TestReverseTax_ProductOnSeveralInvoiceLinesFillsThemInOrder(t *testing.T) {
	lines := []TaxBasisLine{
		{LineNo: 1, ProductID: "A", TaxCode: "DE19", Qty: dec("2"), Tax: dec("38.00")},
		{LineNo: 2, ProductID: "A", TaxCode: "DE7", Qty: dec("4"), Tax: dec("28.00")},
	}
	got := ReverseTax("USD", lines, map[string]decimal.Decimal{"A": dec("3")})
	if len(got) != 2 || !got[0].Qty.Equal(dec("2")) || !got[0].Tax.Equal(dec("38.00")) || !got[1].Qty.Equal(dec("1")) || !got[1].Tax.Equal(dec("7.00")) {
		t.Fatalf("fill in line order: %+v", got)
	}
}

func TestReverseTax_BillingCreditsAndEarlierRMAsAreNotReversedTwice(t *testing.T) {
	line := TaxBasisLine{LineNo: 1, ProductID: "A", TaxCode: "DE19", Qty: dec("10"), Tax: dec("190.00"),
		CreditedQty: dec("2"), CreditedTax: dec("38.00"), ReturnedQty: dec("3"), ReturnedTax: dec("57.00")}
	got := ReverseTax("USD", []TaxBasisLine{line}, map[string]decimal.Decimal{"A": dec("5")})
	if len(got) != 1 || !got[0].Tax.Equal(dec("95.00")) {
		t.Fatalf("5 left of 10 with 38.00 and 57.00 already reversed: %+v", got)
	}
	over := ReverseTax("USD", []TaxBasisLine{line}, map[string]decimal.Decimal{"A": dec("9")})
	if len(over) != 1 || !over[0].Qty.Equal(dec("5")) {
		t.Fatalf("a return cannot take more than the line has left: %+v", over)
	}
}
