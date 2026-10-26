package domain_test

import (
	"encoding/csv"
	"errors"
	"os"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
)

func TestRound_Fixture(t *testing.T) {
	f, err := os.Open("../../testdata/rounding.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows[1:] {
		method, err := domain.ParseMethod(r[2])
		if err != nil {
			t.Fatal(err)
		}
		if got := domain.Round(dec(t, r[0]), dec(t, r[1]), method); !got.Equal(dec(t, r[3])) {
			t.Errorf("Round(%s, %s, %s) = %s, want %s", r[0], r[1], r[2], got, r[3])
		}
	}
}

func TestRound_NonPositiveUnitReturnsAmount(t *testing.T) {
	for _, unit := range []decimal.Decimal{{}, dec(t, "-0.05")} {
		if got := domain.Round(dec(t, "1.23"), unit, domain.HalfUp); !got.Equal(dec(t, "1.23")) {
			t.Errorf("unit %s: got %s", unit, got)
		}
	}
}

func TestValidateUnit(t *testing.T) {
	tests := []struct {
		unit  string
		minor int32
		ok    bool
	}{
		{"0.05", 2, true},
		{"0.25", 2, true},
		{"0.50", 2, true},
		{"1", 2, true},
		{"100", 2, true},
		{"1000", 2, true},
		{"0.000001", 2, false},
		{"0.001", 2, false},
		{"0.01", 0, false},
		{"0.01", 2, true},
		{"1.8446744073709551617", 2, false},
		{"1", 0, true},
		{"0.03", 2, false},
		{"0.3", 2, false},
		{"0", 2, false},
		{"-0.01", 2, false},
		{"0.0000005", 2, false},
		{"1001", 2, false},
		{"1", -1, false},
		{"1", 5, false},
	}
	for _, tc := range tests {
		err := domain.ValidateUnit(dec(t, tc.unit), tc.minor)
		if tc.ok {
			if err != nil {
				t.Errorf("%s/%d: unexpected error %v", tc.unit, tc.minor, err)
			}
			continue
		}
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeRoundingUnit {
			t.Errorf("%s/%d: got %v, want %s", tc.unit, tc.minor, err, domain.CodeRoundingUnit)
		}
	}
}

func TestParseMethod(t *testing.T) {
	for _, name := range []string{"half_even", "half_up", "down", "up"} {
		if m, err := domain.ParseMethod(name); err != nil || string(m) != name {
			t.Errorf("%s: got %q, %v", name, m, err)
		}
	}
	for _, name := range []string{"", "HALF_UP", "Half_Up", "nearest", "1"} {
		if _, err := domain.ParseMethod(name); err == nil {
			t.Errorf("%q: expected error", name)
		}
	}
}

func TestRound_Properties(t *testing.T) {
	units := []string{"0.000001", "0.01", "0.05", "0.25", "0.5", "1", "25", "100", "1000"}
	methods := []domain.Method{domain.HalfEven, domain.HalfUp, domain.Down, domain.Up}
	rapid.Check(t, func(rt *rapid.T) {
		amount := decimal.New(rapid.Int64Range(-1_000_000_000_000_000, 1_000_000_000_000_000).Draw(rt, "amount"), -6)
		unit, err := decimal.NewFromString(rapid.SampledFrom(units).Draw(rt, "unit"))
		if err != nil {
			rt.Fatal(err)
		}
		method := rapid.SampledFrom(methods).Draw(rt, "method")
		got := domain.Round(amount, unit, method)
		if again := domain.Round(got, unit, method); !again.Equal(got) {
			rt.Fatalf("not idempotent: %s then %s", got, again)
		}
		multiple := got.DivRound(unit, 28)
		if !multiple.Equal(multiple.Truncate(0)) {
			rt.Fatalf("%s is not a multiple of %s", got, unit)
		}
		if got.Sub(amount).Abs().GreaterThan(unit) {
			rt.Fatalf("|%s - %s| exceeds %s", got, amount, unit)
		}
	})
}

func TestParsePurpose(t *testing.T) {
	for _, name := range []string{"general", "price", "purchase", "sales", "tax", "cash"} {
		if got, err := domain.ParsePurpose(name); err != nil || string(got) != name {
			t.Errorf("%q: got %q, %v", name, got, err)
		}
	}
	for _, name := range []string{"", "General", "retail"} {
		if _, err := domain.ParsePurpose(name); err == nil {
			t.Errorf("%q: expected error", name)
		}
	}
}
