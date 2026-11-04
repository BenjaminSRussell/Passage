package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestValidateCatchWeight_AC245(t *testing.T) {
	spec := CatchWeight{Nominal: dec("10"), Min: dec("9.8"), Max: dec("11")}
	if err := ValidateCatchWeight(spec, 10, dec("95")); err == nil || err.(*Error).Code != CodeCatchWeight {
		t.Fatalf("95 kg for 10 cases should fail with RCV-CW-001, got %v", err)
	}
	if err := ValidateCatchWeight(spec, 10, dec("102")); err != nil {
		t.Fatalf("102 kg for 10 cases should pass: %v", err)
	}
}

func TestIsWeightUnit_OrgMassUnitsAndUNECECodes(t *testing.T) {
	for _, u := range []string{"kg", "G", "mg", "t", "lb", "oz", "st", "ct", "KGM", "grm", "MGM", "TNE", "LBR", "ONZ", "STI", "CTM"} {
		if !IsWeightUnit(u) {
			t.Errorf("%s is a mass unit", u)
		}
	}
	for _, u := range []string{"CS", "EA", "PCE", "L", "m", ""} {
		if IsWeightUnit(u) {
			t.Errorf("%s is not a mass unit", u)
		}
	}
}

func TestValidateCatchWeight(t *testing.T) {
	spec := CatchWeight{Nominal: dec("10"), Min: dec("9"), Max: dec("11")}
	tests := []struct {
		name   string
		cw     int
		weight string
		code   string
	}{
		{"lower bound inclusive", 2, "18", ""},
		{"upper bound inclusive", 2, "22", ""},
		{"below lower bound", 2, "17.99", CodeCatchWeight},
		{"above upper bound", 2, "22.01", CodeCatchWeight},
		{"zero units", 0, "10", CodeInvalidReceipt},
		{"negative weight", 1, "-1", CodeInvalidReceipt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCatchWeight(spec, tc.cw, dec(tc.weight))
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || err.(*Error).Code != tc.code {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
		})
	}
}
