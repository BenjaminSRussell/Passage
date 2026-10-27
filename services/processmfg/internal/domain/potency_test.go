package domain

import (
	"encoding/csv"
	"os"
	"strconv"
	"testing"

	"github.com/shopspring/decimal"
)

// TestConsumePotency_Fixture runs the pmf-potency-20 fixture (AC-24.2, BR-24.2).
func TestConsumePotency_Fixture(t *testing.T) {
	f, err := os.Open("../../testdata/pmf-potency-20.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	rows = rows[1:]
	if len(rows) != 20 {
		t.Fatalf("fixture has %d rows, want 20", len(rows))
	}
	for i, row := range rows {
		d := func(col int) decimal.Decimal { return decimal.RequireFromString(row[col]) }
		res, err := ConsumePotency(d(0), d(1), d(2), d(3))
		wantErr, _ := strconv.ParseBool(row[6])
		if wantErr {
			if err == nil {
				t.Fatalf("row %d: want PMF-0002, got %+v", i+1, res)
			}
			continue
		}
		if err != nil {
			t.Fatalf("row %d: %v", i+1, err)
		}
		if !res.Qty.Equal(d(4)) || !res.CompensatingQty.Equal(d(5)) {
			t.Fatalf("row %d: got qty %s comp %s, want %s and %s", i+1, res.Qty, res.CompensatingQty, row[4], row[5])
		}
	}
}

func TestConsumePotency_WorkedExample(t *testing.T) {
	// BR-24.2: 100 kg at 50% standard, batch at 40% -> 125 kg, filler reduced by 25 kg.
	res, err := ConsumePotency(dec("100"), dec("50"), dec("40"), dec("100"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Qty.Equal(dec("125")) || !res.CompensatingQty.Equal(dec("75")) {
		t.Fatalf("got %+v", res)
	}
}

func TestConsumePotency_Rejects(t *testing.T) {
	tests := []struct {
		name                           string
		planned, standard, batch, comp string
		code                           string
	}{
		{"zero batch potency", "100", "50", "0", "100", CodeInvalid},
		{"negative standard potency", "100", "-1", "40", "100", CodeInvalid},
		{"negative planned", "-1", "50", "40", "100", CodeInvalid},
		{"filler would go negative", "100", "50", "20", "100", CodeCompNegative},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ConsumePotency(dec(tc.planned), dec(tc.standard), dec(tc.batch), dec(tc.comp))
			de, ok := err.(*Error)
			if !ok || de.Code != tc.code {
				t.Fatalf("err = %v, want code %s", err, tc.code)
			}
		})
	}
}

func TestPotencyInRange(t *testing.T) {
	tests := []struct {
		actual, lo, hi string
		want           bool
	}{
		{"40", "40", "50", true},
		{"50", "40", "50", true},
		{"39.9999", "40", "50", false},
		{"50.0001", "40", "50", false},
	}
	for _, tc := range tests {
		if got := PotencyInRange(dec(tc.actual), dec(tc.lo), dec(tc.hi)); got != tc.want {
			t.Fatalf("PotencyInRange(%s,%s,%s) = %v", tc.actual, tc.lo, tc.hi, got)
		}
	}
}

// dec parses a decimal literal in tests; the empty string is zero.
func dec(s string) decimal.Decimal {
	if s == "" {
		return decimal.Zero
	}
	return decimal.RequireFromString(s)
}

// readFixture reads a CSV fixture from testdata, drops the header and checks
// every row has cols columns.
func readFixture(t *testing.T, name string, cols int) [][]string {
	t.Helper()
	f, err := os.Open("../../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range rows {
		if len(r) != cols {
			t.Fatalf("%s row %d has %d columns, want %d", name, i, len(r), cols)
		}
	}
	return rows[1:]
}
