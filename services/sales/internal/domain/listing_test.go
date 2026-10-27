package domain

import (
	"testing"
	"time"
)

func listingDay(s string) *time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return &t
}

func TestEvaluateListing_Precedence(t *testing.T) {
	const (
		cust  = "c9"
		group = "GRP-A"
		prod  = "FG-120"
		other = "FG-100"
	)
	order := listingDay("2027-03-01")
	cases := []struct {
		name    string
		entries []ListingEntry
		prod    string
		want    string
		allowed bool
	}{
		{"no entries is unrestricted", nil, prod, ReasonUnrestricted, true},
		{"customer DENY", []ListingEntry{{ScopeCustomer, cust, prod, ListingDeny, nil, nil}}, prod, ReasonCustomerDeny, false},
		{"customer DENY beats group ALLOW", []ListingEntry{
			{ScopeGroup, group, prod, ListingAllow, nil, nil},
			{ScopeCustomer, cust, prod, ListingDeny, nil, nil}}, prod, ReasonCustomerDeny, false},
		{"customer ALLOW beats group DENY", []ListingEntry{
			{ScopeGroup, group, prod, ListingDeny, nil, nil},
			{ScopeCustomer, cust, prod, ListingAllow, nil, nil}}, prod, ReasonCustomerAllow, true},
		{"group DENY", []ListingEntry{{ScopeGroup, group, prod, ListingDeny, nil, nil}}, prod, ReasonGroupDeny, false},
		{"group ALLOW", []ListingEntry{{ScopeGroup, group, prod, ListingAllow, nil, nil}}, prod, ReasonGroupAllow, true},
		{"ALLOW list for another customer excludes this one", []ListingEntry{{ScopeCustomer, "c1", prod, ListingAllow, nil, nil}}, prod, ReasonNotListed, false},
		{"DENY for another customer does not restrict", []ListingEntry{{ScopeCustomer, "c1", prod, ListingDeny, nil, nil}}, prod, ReasonUnrestricted, true},
		{"entry for another product is ignored", []ListingEntry{{ScopeCustomer, cust, other, ListingDeny, nil, nil}}, prod, ReasonUnrestricted, true},
		{"expired customer DENY is ignored", []ListingEntry{{ScopeCustomer, cust, prod, ListingDeny, listingDay("2026-01-01"), listingDay("2027-02-28")}}, prod, ReasonUnrestricted, true},
		{"DENY valid from order date", []ListingEntry{{ScopeCustomer, cust, prod, ListingDeny, listingDay("2027-03-01"), nil}}, prod, ReasonCustomerDeny, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := EvaluateListing(c.entries, cust, group, c.prod, *order)
			if got.Reason != c.want || got.Allowed != c.allowed {
				t.Fatalf("got %+v, want reason %s allowed %v", got, c.want, c.allowed)
			}
		})
	}
}
