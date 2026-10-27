package domain_test

import (
	"encoding/csv"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
)

// TestApprovalMatrix is AC1: 30 requisitions across six amounts and five
// categories route to the chain in testdata/approval-matrix.csv.
func TestApprovalMatrix(t *testing.T) {
	f, err := os.Open("../../testdata/approval-matrix.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(rows) - 1; got != 30 {
		t.Fatalf("matrix has %d rows, want 30", got)
	}
	policy := domain.DefaultApprovalPolicy()
	policy.NoAutoCategories = map[string]bool{"IT_HARDWARE": true}
	for _, r := range rows[1:] {
		amount := dec(t, r[0])
		var want []string
		if r[2] != "" {
			want = strings.Split(r[2], "|")
		}
		if got := policy.Route(amount, r[1]); !slices.Equal(got, want) {
			t.Errorf("amount %s category %s: chain %v, want %v", r[0], r[1], got, want)
		}
	}
}

func TestApprovalPolicy_BandBoundariesAreInclusive(t *testing.T) {
	p := domain.DefaultApprovalPolicy()
	tests := []struct {
		amount string
		chain  int
	}{
		{"0.01", 0}, {"1000", 0}, {"1000.01", 1}, {"10000", 1}, {"10000.01", 2}, {"100000", 2}, {"100000.01", 3},
	}
	for _, tc := range tests {
		if got := len(p.Route(decimal.RequireFromString(tc.amount), "OFFICE")); got != tc.chain {
			t.Errorf("amount %s: chain length %d, want %d", tc.amount, got, tc.chain)
		}
	}
}
