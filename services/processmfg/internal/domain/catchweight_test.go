package domain

import (
	"strconv"
	"testing"
)

// The AC-24.5 fixture uses a per-unit minimum of 9.8 kg. With the BR-24.5 rule
// (min × cw ≤ qty ≤ max × cw) a minimum of 9 kg would accept 95 kg for 10
// units, so the example is only consistent with a per-unit minimum above 9.5 kg.
func TestValidateCatchWeight_AC245(t *testing.T) {
	spec := CatchWeight{Nominal: dec("10"), Min: dec("9.8"), Max: dec("11")}
	if err := ValidateCatchWeight(spec, 10, dec("95")); err == nil || err.(*Error).Code != CodeCatchWeight {
		t.Fatalf("95 kg for 10 cases should fail with PMF-0004, got %v", err)
	}
	if err := ValidateCatchWeight(spec, 10, dec("102")); err != nil {
		t.Fatalf("102 kg for 10 cases should pass: %v", err)
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
		{"zero units", 0, "10", CodeInvalid},
		{"negative weight", 1, "-1", CodeInvalid},
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

// pmf-catchweight-10 (PRD 4.24.9): BR-24.5 bounds min × units ≤ weight ≤ max × units.
func TestValidateCatchWeight_Fixture(t *testing.T) {
	rows := readFixture(t, "pmf-catchweight-10.csv", 6)
	if len(rows) != 10 {
		t.Fatalf("pmf-catchweight-10 has %d rows", len(rows))
	}
	for _, r := range rows {
		cw, err := strconv.Atoi(r[3])
		if err != nil {
			t.Fatal(err)
		}
		err = ValidateCatchWeight(CatchWeight{Nominal: dec(r[0]), Min: dec(r[1]), Max: dec(r[2])}, cw, dec(r[4]))
		if ok := err == nil; ok != (r[5] == "true") {
			t.Errorf("%s units at %s: err = %v, want ok=%s", r[3], r[4], err, r[5])
		}
		if err != nil && err.(*Error).Code != CodeCatchWeight {
			t.Errorf("%s units at %s: code %s, want PMF-0004", r[3], r[4], err.(*Error).Code)
		}
	}
}
