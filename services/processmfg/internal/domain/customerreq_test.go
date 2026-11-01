package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestValidateRequirement(t *testing.T) {
	days, tooMany, yes := 60, MaxSellableDays+1, true
	lo, hi := decimal.NewFromInt(40), decimal.NewFromInt(60)
	ok := []Requirement{
		{Level: ScopeCustomer, SellableDays: &days},
		{Level: ScopeItemDefault, SameBatch: &yes},
		{Level: ScopeGroup, Attribute: "potency", Min: &lo},
		{Level: ScopeGroup, Attribute: "potency", Min: &lo, Max: &hi},
	}
	for i, r := range ok {
		if err := ValidateRequirement(r); err != nil {
			t.Errorf("valid %d: %v", i, err)
		}
	}
	bad := map[string]Requirement{
		"nothing set":   {Level: ScopeCustomer},
		"days too high": {Level: ScopeCustomer, SellableDays: &tooMany},
		"range no name": {Level: ScopeCustomer, Min: &lo},
		"name no range": {Level: ScopeCustomer, Attribute: "potency"},
		"min above max": {Level: ScopeCustomer, Attribute: "potency", Min: &hi, Max: &lo},
		"unknown scope": {Level: 4, SellableDays: &days},
	}
	for name, r := range bad {
		err := ValidateRequirement(r)
		if de, isDE := err.(*Error); !isDE || de.Code != CodeInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Deciding names the rows Resolve takes each field from: the customer's days
// win over the item default, the first of two group rows wins the flag, and a
// customer row that only sets a range leaves the days to the others.
func TestDeciding(t *testing.T) {
	d10, d60, d90, yes, no := 10, 60, 90, true, false
	lo := decimal.NewFromInt(40)
	rows := []Requirement{
		{Level: ScopeCustomer, SellableDays: &d60},                // 0: days
		{Level: ScopeCustomer, SellableDays: &d90},                // 1: same level, later: overridden
		{Level: ScopeGroup, SameBatch: &yes},                      // 2: flag
		{Level: ScopeGroup, SameBatch: &no},                       // 3: overridden
		{Level: ScopeItemDefault, SellableDays: &d10},             // 4: overridden by 0
		{Level: ScopeItemDefault, Attribute: "potency", Min: &lo}, // 5: only potency range
	}
	got := Deciding(rows)
	want := []int{0, 2, 5}
	if len(got) != len(want) || got[0] != 0 || got[1] != 2 || got[2] != 5 {
		t.Fatalf("deciding %v want %v", got, want)
	}
	res, err := Resolve(rows)
	if err != nil || res.SellableDays != 60 || !res.SameBatch || res.Ranges["potency"].Min == nil {
		t.Fatalf("resolve %+v err=%v", res, err)
	}
	if got := Deciding(nil); len(got) != 0 {
		t.Fatalf("no rows: %v", got)
	}
}
