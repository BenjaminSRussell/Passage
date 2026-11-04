package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func TestValidateElection(t *testing.T) {
	v := func(s string) *decimal.Decimal { x := decimal.RequireFromString(s); return &x }
	for _, c := range []struct {
		name string
		in   ElectionInput
		ok   bool
	}{
		{"none", ElectionInput{Election: "", TermMonths: 60}, true},
		{"none with value", ElectionInput{Election: ElectionNone, TermMonths: 60, UnderlyingValue: v("100"), UnderlyingCurrency: "USD"}, false},
		{"none with a purchase option", ElectionInput{Election: ElectionNone, TermMonths: 12, PurchaseOption: true}, true},
		{"short 12", ElectionInput{Election: ElectionShortTerm, TermMonths: 12}, true},
		{"short 13", ElectionInput{Election: ElectionShortTerm, TermMonths: 13}, false},
		{"short with a purchase option", ElectionInput{Election: ElectionShortTerm, TermMonths: 6, PurchaseOption: true}, false},
		{"short with value", ElectionInput{Election: ElectionShortTerm, TermMonths: 6, UnderlyingValue: v("100"), UnderlyingCurrency: "USD"}, false},
		{"short with a value currency", ElectionInput{Election: ElectionShortTerm, TermMonths: 6, UnderlyingCurrency: "USD"}, false},
		{"low value", ElectionInput{Election: ElectionLowValue, TermMonths: 36, UnderlyingValue: v("4999.99"), UnderlyingCurrency: "USD"}, true},
		{"low value at limit", ElectionInput{Election: ElectionLowValue, TermMonths: 36, UnderlyingValue: v("5000"), UnderlyingCurrency: "USD"}, true},
		{"low value over limit", ElectionInput{Election: ElectionLowValue, TermMonths: 36, UnderlyingValue: v("5000.01"), UnderlyingCurrency: "USD"}, false},
		{"low value in EUR", ElectionInput{Election: ElectionLowValue, TermMonths: 36, UnderlyingValue: v("5000"), UnderlyingCurrency: "EUR"}, true},
		{"low value over the EUR limit", ElectionInput{Election: ElectionLowValue, TermMonths: 36, UnderlyingValue: v("5000.01"), UnderlyingCurrency: "EUR"}, false},
		// 4,999 yen is far below the threshold but is not stated in a currency the threshold is set in; 700,000 yen
		// (about USD 4,600) would be refused if it were compared with 5,000 in the lease currency.
		{"low value in JPY", ElectionInput{Election: ElectionLowValue, TermMonths: 36, UnderlyingValue: v("4999"), UnderlyingCurrency: "JPY"}, false},
		{"low value without currency", ElectionInput{Election: ElectionLowValue, TermMonths: 36, UnderlyingValue: v("100")}, false},
		{"low value missing", ElectionInput{Election: ElectionLowValue, TermMonths: 36, UnderlyingCurrency: "USD"}, false},
		{"low value zero", ElectionInput{Election: ElectionLowValue, TermMonths: 36, UnderlyingValue: v("0"), UnderlyingCurrency: "USD"}, false},
		{"low value sub-cent", ElectionInput{Election: ElectionLowValue, TermMonths: 36, UnderlyingValue: v("10.005"), UnderlyingCurrency: "USD"}, false},
		{"low value with a purchase option", ElectionInput{Election: ElectionLowValue, TermMonths: 36, PurchaseOption: true, UnderlyingValue: v("900"), UnderlyingCurrency: "USD"}, true},
		{"unknown", ElectionInput{Election: "Other", TermMonths: 12}, false},
	} {
		err := ValidateElection(c.in)
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
		var de *Error
		if err != nil && (!errors.As(err, &de) || de.Code != CodeLeaseElection) {
			t.Errorf("%s: code %v", c.name, err)
		}
	}
}

// A lease under an election has no liability or ROU asset and expenses each payment; the period journal
// balances and moves only the expense and clearing accounts.
func TestComputeExemptLease(t *testing.T) {
	s, err := ComputeExemptLease(LeaseTerms{Payment: dd("500"), PaymentsPerYear: 4, TermMonths: 9 + 3, AnnualRate: dd("0.05"), Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Liability.IsZero() || !s.ROUAsset.IsZero() || len(s.Lines) != 4 {
		t.Fatalf("%+v", s)
	}
	for i, l := range s.Lines {
		if l.PeriodNo != i+1 || !l.Payment.Equal(dd("500")) || !l.Interest.IsZero() || !l.Opening.IsZero() || !l.Closing.IsZero() || !l.ROUDepreciation.IsZero() {
			t.Fatalf("line %d: %+v", i, l)
		}
	}
	net := LeaseExemptPeriodNet(leaseAcc, dd("500"))
	if !net["6830"].Equal(dd("500")) || !net["2110"].Equal(dd("-500")) || !sumNet(net).IsZero() || len(net) != 2 {
		t.Fatalf("%v", net)
	}
	if _, err := ComputeExemptLease(LeaseTerms{Payment: dd("500"), PaymentsPerYear: 4, TermMonths: 10, AnnualRate: dd("0.05"), Currency: "USD"}); err == nil {
		t.Fatal("a term that is not a whole number of payments was accepted")
	}
}
