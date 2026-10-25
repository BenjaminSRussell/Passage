package domain_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

type allocationFixtures struct {
	Allocate []struct {
		Name   string
		Source decimal.Decimal
		Scale  int32
		Shares []domain.Share
		Want   map[string]decimal.Decimal
	}
	Reciprocal []struct {
		Name   string
		Direct []decimal.Decimal
		Matrix [][]decimal.Decimal
		Scale  int32
		Want   []decimal.Decimal
	}
}

func loadAllocationFixtures(t *testing.T) allocationFixtures {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/alloc-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var f allocationFixtures
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestAllocate_Fixtures(t *testing.T) {
	for _, tc := range loadAllocationFixtures(t).Allocate {
		t.Run(tc.Name, func(t *testing.T) {
			parts, err := domain.Allocate(tc.Source, tc.Shares, tc.Scale)
			if err != nil {
				t.Fatal(err)
			}
			sum := decimal.Zero
			for _, p := range parts {
				sum = sum.Add(p.Amount)
				if want := tc.Want[p.Key]; !p.Amount.Equal(want) {
					t.Errorf("%s: got %s, want %s", p.Key, p.Amount, want)
				}
			}
			if !sum.Equal(tc.Source) {
				t.Errorf("parts sum to %s, want %s", sum, tc.Source)
			}
		})
	}
}

func TestAllocate_RentFixture(t *testing.T) {
	shares := []domain.Share{{Key: "A", Weight: decimal.NewFromInt(3)}, {Key: "B", Weight: decimal.NewFromInt(5)}, {Key: "C", Weight: decimal.NewFromInt(2)}}
	parts, err := domain.Allocate(decimal.RequireFromString("10000.01"), shares, 2)
	if err != nil || parts[1].Amount.String() != "5000.01" {
		t.Fatalf("got %v, %v; the largest share must carry the penny", parts, err)
	}
}

func TestAllocate_Errors(t *testing.T) {
	one := decimal.NewFromInt(1)
	tests := []struct {
		name    string
		shares  []domain.Share
		wantErr string
	}{
		{"no destinations", nil, domain.CodeValidation},
		{"negative basis", []domain.Share{{Key: "A", Weight: one}, {Key: "B", Weight: one.Neg()}}, domain.CodeValidation},
		{"all-zero basis", []domain.Share{{Key: "A"}, {Key: "B"}}, domain.CodeZeroBasis},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parts, err := domain.Allocate(decimal.NewFromInt(100), tc.shares, 2)
			if code(err) != tc.wantErr || parts != nil {
				t.Fatalf("got %v, %v; want code %s and no parts", parts, err, tc.wantErr)
			}
		})
	}
}

func TestAllocate_SumsToSourceProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		source := decimal.New(rapid.Int64Range(-1_000_000_00, 1_000_000_00).Draw(t, "cents"), -2)
		n := rapid.IntRange(1, 8).Draw(t, "n")
		shares := make([]domain.Share, n)
		for i := range shares {
			shares[i] = domain.Share{Key: string(rune('A' + i)), Weight: decimal.NewFromInt(rapid.Int64Range(1, 1000).Draw(t, "weight"))}
		}
		parts, err := domain.Allocate(source, shares, 2)
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		for _, p := range parts {
			sum = sum.Add(p.Amount)
		}
		if !sum.Equal(source) {
			t.Fatalf("parts sum to %s, want %s", sum, source)
		}
	})
}

func TestReciprocal_TwoDepartments(t *testing.T) {
	for _, tc := range loadAllocationFixtures(t).Reciprocal {
		t.Run(tc.Name, func(t *testing.T) {
			got, err := domain.Reciprocal(tc.Direct, tc.Matrix, tc.Scale, 100)
			if err != nil {
				t.Fatal(err)
			}
			for i := range tc.Want {
				if !got[i].Equal(tc.Want[i]) {
					t.Errorf("department %d: got %s, want %s", i, got[i], tc.Want[i])
				}
			}
		})
	}
}

func TestReciprocal_ConvergesProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(2, 6).Draw(t, "departments")
		limit := int64(44 / (n - 1))
		direct := make([]decimal.Decimal, n)
		matrix := make([][]decimal.Decimal, n)
		for i := range matrix {
			direct[i] = decimal.New(rapid.Int64Range(0, 10_000_000).Draw(t, "direct"), -2)
			matrix[i] = make([]decimal.Decimal, n)
			for j := range matrix[i] {
				if i != j {
					matrix[i][j] = decimal.New(rapid.Int64Range(0, limit).Draw(t, "percent"), -2)
				}
			}
		}
		totals, err := domain.Reciprocal(direct, matrix, 2, 100)
		if err != nil {
			t.Fatal(err)
		}
		bound := decimal.RequireFromString("0.02")
		for i := range totals {
			expected := direct[i]
			for j := range totals {
				if j != i {
					expected = expected.Add(totals[j].Mul(matrix[j][i]))
				}
			}
			if totals[i].Sub(expected).Abs().GreaterThan(bound) {
				t.Fatalf("department %d total %s violates its equation (%s)", i, totals[i], expected)
			}
		}
	})
}

func TestReciprocal_NoConvergence(t *testing.T) {
	one := decimal.NewFromInt(1)
	matrix := [][]decimal.Decimal{{decimal.Zero, one}, {one, decimal.Zero}}
	totals, err := domain.Reciprocal([]decimal.Decimal{decimal.NewFromInt(1000), decimal.NewFromInt(500)}, matrix, 2, 100)
	if code(err) != domain.CodeNoConvergence || totals != nil {
		t.Fatalf("got %v, %v; want %s", totals, err, domain.CodeNoConvergence)
	}
}

func TestReciprocal_RejectsRaggedMatrix(t *testing.T) {
	if _, err := domain.Reciprocal([]decimal.Decimal{decimal.Zero, decimal.Zero}, [][]decimal.Decimal{{decimal.Zero}, {decimal.Zero}}, 2, 100); code(err) != domain.CodeValidation {
		t.Fatalf("got %v", err)
	}
}
