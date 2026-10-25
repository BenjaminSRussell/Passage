package domain_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func op(no, next int, priority string, setup, run string) domain.RouteOperation {
	return domain.RouteOperation{No: no, NextNo: next, Priority: priority, SetupH: dec(setup), RunH: dec(run), ProcessQty: dec("1")}
}

func TestLeadTimeFixtures(t *testing.T) {
	var fixtures []struct {
		Name          string
		Quantity      string
		HoursPerDay   string
		Operations    []domain.RouteOperation
		ExpectedHours string
		ExpectedDays  int
	}
	raw, err := os.ReadFile("../../testdata/routing-leadtime.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 12 {
		t.Fatalf("fixtures = %d, want 12", len(fixtures))
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			got, err := domain.LeadTime(f.Operations, dec(f.Quantity), dec(f.HoursPerDay))
			if err != nil {
				t.Fatal(err)
			}
			if !got.Hours.Equal(dec(f.ExpectedHours)) || got.CalendarDays != f.ExpectedDays {
				t.Fatalf("got %s h / %d d, want %s h / %d d", got.Hours, got.CalendarDays, f.ExpectedHours, f.ExpectedDays)
			}
		})
	}
}

func TestLeadTimeErrors(t *testing.T) {
	zeroProcessQty := op(10, 0, "", "1", "1")
	zeroProcessQty.ProcessQty = decimal.Zero
	negative := op(10, 0, "", "-1", "1")
	tests := []struct {
		name string
		ops  []domain.RouteOperation
		qty  string
		want string
	}{
		{"missing next operation", []domain.RouteOperation{op(10, 99, "", "1", "1")}, "100", domain.CodeRouteNextMiss},
		{"secondary without primary at its join", []domain.RouteOperation{op(10, 40, "", "1", "1"), op(20, 30, "secondary1", "1", "1"), op(30, 40, "", "1", "1"), op(40, 0, "", "1", "1")}, "100", domain.CodeRouteNoPrimary},
		{"cycle", []domain.RouteOperation{op(10, 20, "", "1", "1"), op(20, 10, "", "1", "1")}, "100", domain.CodeRouteCycle},
		{"zero process quantity", []domain.RouteOperation{zeroProcessQty}, "100", domain.CodeRouteQty},
		{"negative time", []domain.RouteOperation{negative}, "100", domain.CodeRouteQty},
		{"negative quantity", []domain.RouteOperation{op(10, 0, "", "1", "1")}, "-5", domain.CodeRouteQty},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := domain.LeadTime(tt.ops, dec(tt.qty), dec("8")); !domain.HasCode(err, tt.want) {
				t.Fatalf("err = %v, want %s", err, tt.want)
			}
		})
	}
}

func TestLeadTimeNeverShrinksWithQuantity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 5).Draw(t, "operations")
		ops := make([]domain.RouteOperation, n)
		for i := range ops {
			ops[i] = domain.RouteOperation{
				No: (i + 1) * 10, SetupH: decimal.NewFromInt(int64(rapid.IntRange(0, 5).Draw(t, "setup"))),
				RunH: decimal.NewFromInt(int64(rapid.IntRange(0, 5).Draw(t, "run"))), ProcessQty: decimal.NewFromInt(int64(rapid.IntRange(1, 20).Draw(t, "pq"))),
			}
		}
		small := int64(rapid.IntRange(1, 500).Draw(t, "small"))
		large := small + int64(rapid.IntRange(0, 500).Draw(t, "extra"))
		a, errA := domain.LeadTime(ops, decimal.NewFromInt(small), dec("8"))
		b, errB := domain.LeadTime(ops, decimal.NewFromInt(large), dec("8"))
		if errA != nil || errB != nil {
			t.Fatal(errA, errB)
		}
		if b.Hours.LessThan(a.Hours) {
			t.Fatalf("qty %d gives %s h but qty %d gives %s h", small, a.Hours, large, b.Hours)
		}
	})
}
