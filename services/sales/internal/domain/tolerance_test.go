package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func tdec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestDeliveryToleranceValidate(t *testing.T) {
	cases := []struct {
		name string
		in   DeliveryTolerance
		keys []string
	}{
		{"defaults", DefaultTolerance(), nil},
		{"top of range", DeliveryTolerance{tdec("100"), tdec("100.00"), PartialOneShipment}, nil},
		{"negative over", DeliveryTolerance{tdec("-0.01"), tdec("0"), PartialAllowed}, []string{"overdeliveryPct"}},
		{"over 100", DeliveryTolerance{tdec("0"), tdec("100.01"), PartialAllowed}, []string{"underdeliveryPct"}},
		{"three places", DeliveryTolerance{tdec("5.005"), tdec("0"), PartialAllowed}, []string{"overdeliveryPct"}},
		{"unknown policy", DeliveryTolerance{tdec("0"), tdec("0"), "SOMETIMES"}, []string{"partialDelivery"}},
		{"empty policy", DeliveryTolerance{}, []string{"partialDelivery"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.Validate()
			if len(got) != len(tc.keys) {
				t.Fatalf("fields = %v, want %v", got, tc.keys)
			}
			for _, k := range tc.keys {
				if got[k] == "" {
					t.Errorf("missing field error %q in %v", k, got)
				}
			}
		})
	}
}

func TestDeliveryToleranceRange(t *testing.T) {
	cases := []struct {
		name         string
		over, under  string
		ordered      string
		wantMin, max string
	}{
		{"no tolerance is exact", "0", "0", "100", "100", "100"},
		{"5 over, 2.5 under", "5", "2.5", "1000", "975", "1050"},
		{"cut to 6 places, upper never above exact", "10", "10", "0.3333333", "0.300000", "0.366666"},
		{"under 100 percent allows nothing", "0", "100", "40", "0", "40"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tol := DeliveryTolerance{tdec(tc.over), tdec(tc.under), PartialAllowed}
			lo, hi := tol.Range(tdec(tc.ordered))
			if !lo.Equal(tdec(tc.wantMin)) || !hi.Equal(tdec(tc.max)) {
				t.Fatalf("range = %s..%s, want %s..%s", lo, hi, tc.wantMin, tc.max)
			}
		})
	}
}

func TestResolveTolerance(t *testing.T) {
	cust, other := "c-1", "c-2"
	own := ToleranceRule{ID: "own", CustomerID: &cust, DeliveryTolerance: DeliveryTolerance{tdec("1"), tdec("2"), PartialCompleteOnly}}
	all := ToleranceRule{ID: "all", DeliveryTolerance: DeliveryTolerance{tdec("5"), tdec("5"), PartialAllowed}}
	rules := []ToleranceRule{all, own}
	cases := []struct {
		name         string
		rules        []ToleranceRule
		customer     string
		wantSource   string
		wantID       string
		wantOver     string
		wantPartials string
	}{
		{"own rule replaces the all-customers rule", rules, cust, ToleranceCustomer, "own", "1", PartialCompleteOnly},
		{"another customer gets the all-customers rule", rules, other, ToleranceAllCustomers, "all", "5", PartialAllowed},
		{"another customer's rule does not apply", []ToleranceRule{own}, other, ToleranceDefault, "", "0", PartialAllowed},
		{"no rules is the default", nil, cust, ToleranceDefault, "", "0", PartialAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, id, source := ResolveTolerance(tc.rules, tc.customer, "")
			if source != tc.wantSource || id != tc.wantID || !got.OverdeliveryPct.Equal(tdec(tc.wantOver)) || got.PartialDelivery != tc.wantPartials {
				t.Fatalf("got %+v id=%q source=%s", got, id, source)
			}
		})
	}
}

func TestResolveToleranceEntityDefault(t *testing.T) {
	got, id, source := ResolveTolerance(nil, "c", PartialCompleteOnly)
	if got.PartialDelivery != PartialCompleteOnly || !got.OverdeliveryPct.IsZero() || id != "" || source != ToleranceDefault {
		t.Fatalf("entity default: %+v %q %q", got, id, source)
	}
	if got, _, _ := ResolveTolerance(nil, "c", ""); got.PartialDelivery != PartialAllowed {
		t.Fatalf("empty default is ALLOWED: %+v", got)
	}
}

func TestCompleteOnlyFor(t *testing.T) {
	for policy, want := range map[string]bool{PartialAllowed: false, PartialCompleteOnly: true, PartialOneShipment: true, "": false} {
		if CompleteOnlyFor(policy) != want {
			t.Fatalf("%q: want %v", policy, want)
		}
	}
}
