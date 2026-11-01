package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func validParty() OneTimeParty {
	return OneTimeParty{Name: "Walk-in Buyer GmbH", Line1: "Hauptstr. 1", City: "Berlin", PostalCode: "10115", Country: "DE"}
}

func TestOneTimePartyValidate(t *testing.T) {
	if f := validParty().Validate(); len(f) != 0 {
		t.Fatalf("valid party: %v", f)
	}
	bad := OneTimeParty{Name: "  ", Country: "Germany", TaxID: string(make([]byte, 41))}
	f := bad.Validate()
	for _, k := range []string{"name", "line1", "city", "postalCode", "country", "taxId"} {
		if f[k] == "" {
			t.Errorf("missing error for %s: %v", k, f)
		}
	}
}

func TestOneTimeLimit_DefaultsAndConfigured(t *testing.T) {
	p := DefaultParameters()
	if l, ok := p.OneTimeLimit("USD"); !ok || !l.Equal(decimal.NewFromInt(5000)) {
		t.Fatalf("USD default = %v %v", l, ok)
	}
	if l, ok := p.OneTimeLimit("EUR"); !ok || !l.Equal(decimal.NewFromInt(4500)) {
		t.Fatalf("EUR default = %v %v", l, ok)
	}
	if _, ok := p.OneTimeLimit("GBP"); ok {
		t.Fatal("GBP has no default limit")
	}
	lim, cur := decimal.NewFromInt(1000), "GBP"
	p.OneTimeOrderLimit, p.OneTimeOrderLimitCurrency = &lim, &cur
	if l, ok := p.OneTimeLimit("GBP"); !ok || !l.Equal(lim) {
		t.Fatalf("configured GBP = %v %v", l, ok)
	}
	if _, ok := p.OneTimeLimit("EUR"); ok {
		t.Fatal("a limit stated in GBP must not stand for EUR")
	}
}

func TestCheckOneTimeLimit_Edges(t *testing.T) {
	p := DefaultParameters()
	if err := p.CheckOneTimeLimit(decimal.NewFromInt(5000), "USD"); err != nil {
		t.Fatalf("equal to the limit passes: %v", err)
	}
	err := p.CheckOneTimeLimit(decimal.RequireFromString("5000.01"), "USD")
	var le *OneTimeLimitError
	if !errors.As(err, &le) || le.Detail != "One-time order exceeds limit 5000.00 USD; create a regular customer" {
		t.Fatalf("over the limit: %v", err)
	}
	if err := p.CheckOneTimeLimit(decimal.NewFromInt(1), "JPY"); err == nil {
		t.Fatal("a currency without a limit is refused")
	}
}

func TestParametersValidate_OneTimeLimit(t *testing.T) {
	p := DefaultParameters()
	lim := decimal.RequireFromString("100.005")
	p.OneTimeOrderLimit = &lim
	if f := p.Validate(); f["oneTimeOrderLimit"] == "" {
		t.Fatalf("amount without currency: %v", f)
	}
	cur := "usd"
	p.OneTimeOrderLimitCurrency = &cur
	f := p.Validate()
	if f["oneTimeOrderLimit"] == "" || f["oneTimeOrderLimitCurrency"] == "" {
		t.Fatalf("3 decimal places and lower-case currency: %v", f)
	}
	lim, cur = decimal.NewFromInt(0), "USD"
	if f := p.Validate(); len(f) != 0 {
		t.Fatalf("zero is allowed (no one-time orders): %v", f)
	}
}
