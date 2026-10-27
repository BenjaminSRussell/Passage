package domain

import (
	"strings"
	"testing"
)

func TestValidateIBAN_Table(t *testing.T) {
	tests := []struct {
		name string
		iban string
		ok   bool
	}{
		{"valid German IBAN", "DE89 3704 0044 0532 0130 00", true},
		{"valid GB IBAN lower case", "gb82west12345698765432", true},
		{"check digits wrong", "DE89370400440532013001", false},
		{"too short", "DE8937", false},
		{"bad country shape", "1289370400440532013000", false},
		{"invalid character", "DE89370400440532013O00", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateIBAN(tc.iban)
			if (err == nil) != tc.ok {
				t.Fatalf("ValidateIBAN(%q) = %v; want ok=%v", tc.iban, err, tc.ok)
			}
		})
	}
}

func TestMaskIBAN_KeepsCountryAndLastFour(t *testing.T) {
	if got, want := MaskIBAN("DE89 3704 0044 0532 0130 00"), "DE"+strings.Repeat("*", 16)+"3000"; got != want {
		t.Fatalf("MaskIBAN = %q", got)
	}
	if got := Last4("DE89370400440532013000"); got != "3000" {
		t.Fatalf("Last4 = %q", got)
	}
}

func TestAccountTransitions(t *testing.T) {
	tests := []struct {
		from, to AccountStatus
		ok       bool
	}{
		{AccountDraft, AccountActive, true},
		{AccountActive, AccountInactive, true},
		{AccountInactive, AccountActive, true},
		{AccountInactive, AccountClosed, true},
		{AccountDraft, AccountClosed, false},
		{AccountClosed, AccountActive, false},
	}
	for _, tc := range tests {
		if got := CanTransition(tc.from, tc.to); got != tc.ok {
			t.Fatalf("CanTransition(%s, %s) = %v; want %v", tc.from, tc.to, got, tc.ok)
		}
	}
}

func TestValidBIC(t *testing.T) {
	for bic, ok := range map[string]bool{"DEUTDEFF": true, "DEUTDEFF500": true, "DEUT1EFF": false, "DEUTDE": false} {
		if ValidBIC(bic) != ok {
			t.Fatalf("ValidBIC(%q) = %v", bic, !ok)
		}
	}
}
