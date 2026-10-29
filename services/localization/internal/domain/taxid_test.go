package domain

import (
	"testing"

	"pgregory.net/rapid"
)

func TestValidateTaxID_Table(t *testing.T) {
	tests := []struct {
		scheme, value string
		ok            bool
	}{
		{"GB_VAT", "GB123456789", true},
		{"GB_VAT", "GB12345", false},
		{"DE_USTID", "DE123456789", true},
		{"DE_USTID", "DE12345678", false},
		{"FR_VAT", "FR40303265045", true},
		{"FR_VAT", "FR4030326504", false},
		{"MX_RFC", "XAXX010101000", true},
		{"MX_RFC", "xx", false},
		{"CA_BN", "123456789RT0001", true},
		{"CA_BN", "12345678", false},
		{"US_EIN", "12-3456789", true},
		{"US_EIN", "123456789", true},
		{"US_EIN", "1234", false},
		{"IN_GSTIN", "27AAPFU0939F1ZV", true},
		{"IN_GSTIN", "27AAPFU0939F1ZW", false},
		{"IN_GSTIN", "27aapfu0939f1zv", true},
		{"XX", "1", false},
	}
	for _, tc := range tests {
		err := ValidateTaxID(tc.scheme, tc.value)
		if (err == nil) != tc.ok {
			t.Errorf("%s %q: valid=%v, want %v (%v)", tc.scheme, tc.value, err == nil, tc.ok, err)
		}
	}
}

// Property: every 14-character GSTIN body gets a check character that the
// validator accepts, and changing that character breaks validation.
func TestGSTIN_PropertyCheckCharacter(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		body := rapid.StringMatching(`[0-9]{2}[A-Z]{5}[0-9]{4}[A-Z][1-9A-Z]Z`).Draw(t, "body")
		check := GSTINCheckChar(body)
		if err := ValidateTaxID("IN_GSTIN", body+string(check)); err != nil {
			t.Fatalf("%s%c rejected: %v", body, check, err)
		}
		wrong := byte('0')
		if check == wrong {
			wrong = '1'
		}
		if err := ValidateTaxID("IN_GSTIN", body+string(wrong)); err == nil {
			t.Fatalf("%s%c accepted with wrong check", body, wrong)
		}
	})
}
