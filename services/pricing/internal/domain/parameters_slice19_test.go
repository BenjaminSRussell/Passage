package domain_test

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

func TestParseThresholds(t *testing.T) {
	got, faults := domain.ParseThresholds(map[string]string{"USD": "250", "GBP": "0", "EUR": "200.500000"})
	if faults != nil || !got["USD"].Equal(decimal.NewFromInt(250)) || !got["GBP"].IsZero() || got["EUR"].String() != "200.5" {
		t.Fatalf("valid table = %v, %v", got, faults)
	}
	if trailing, faults := domain.ParseThresholds(map[string]string{"CHF": "75.123456000"}); faults != nil || trailing["CHF"].String() != "75.123456" {
		t.Fatalf("trailing zeros past six places = %v, %v; want accepted as 75.123456", trailing, faults)
	}
	if c := domain.CanonicalThresholds(got); c["EUR"] != "200.5" || c["GBP"] != "0" {
		t.Fatalf("canonical = %v; want 200.5 and 0", c)
	}
	for key, in := range map[string]map[string]string{
		"smallOrderThresholds[usd]": {"usd": "1"},
		"smallOrderThresholds[US]":  {"US": "1"},
		"smallOrderThresholds[USD]": {"USD": "-0.01"},
		"smallOrderThresholds[EUR]": {"EUR": "0.0000001"},
		"smallOrderThresholds[GBP]": {"GBP": "abc"},
		"smallOrderThresholds[JPY]": {"JPY": "1000000001"},
	} {
		if _, faults := domain.ParseThresholds(in); faults[key] == "" {
			t.Errorf("%v: faults = %v; want %s", in, faults, key)
		}
	}
	many := map[string]string{}
	for a := 'A'; a < 'A'+31; a++ {
		many["A"+string(a)+"Z"] = "1"
	}
	if _, faults := domain.ParseThresholds(many); faults["smallOrderThresholds"] == "" {
		t.Errorf("31 currencies: faults = %v; want the table refused", faults)
	}
}

func TestThresholdFor_TableOverReferenceOverNone(t *testing.T) {
	table := map[string]string{"USD": "100", "GBP": "0", "CHF": "75.5"}
	for cur, want := range map[string]string{"USD": "100", "GBP": "0", "CHF": "75.5", "EUR": "200", "JPY": "0"} {
		if got := domain.ThresholdFor(table, cur); got.String() != want {
			t.Errorf("%s = %s; want %s", cur, got, want)
		}
	}
	if got := domain.ThresholdFor(nil, "USD"); got.String() != "250" {
		t.Errorf("no table: USD = %s; want the reference 250", got)
	}
}

func TestTenantParameterEnums(t *testing.T) {
	for n, want := range map[int]bool{9: false, 10: true, 60: true, 600: true, 601: false, 0: false} {
		if domain.ValidCacheTTL(n) != want {
			t.Errorf("ValidCacheTTL(%d) = %v; want %v", n, !want, want)
		}
	}
	if !domain.ValidKillEffect("NEXT_RESOLVE") || !domain.ValidKillEffect("IMMEDIATE_CACHE_FLUSH") || domain.ValidKillEffect("") || domain.ValidKillEffect("NOW") {
		t.Error("kill switch effect set is wrong")
	}
	if !domain.ValidSearchMode("FIRST_MATCH") || !domain.ValidSearchMode("SEARCH_NEXT") || domain.ValidSearchMode("next") {
		t.Error("search mode set is wrong")
	}
}
