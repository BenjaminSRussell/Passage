package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestAllocateDeductions(t *testing.T) {
	tests := []struct {
		name      string
		available string
		claims    []Claim
		want      map[string][2]string // code -> taken, arrears
	}{
		{"fits in full", "1000", []Claim{{Code: "A", Priority: 1, Due: dec("200"), Policy: PolicyPartial}}, map[string][2]string{"A": {"200", "0"}}},
		{"PARTIAL takes up to net", "150", []Claim{{Code: "MEDICAL", Priority: 2, Due: dec("200"), Policy: PolicyPartial}}, map[string][2]string{"MEDICAL": {"150", "50"}}},
		{"ARREARS takes nothing", "150", []Claim{{Code: "LOAN", Priority: 2, Due: dec("200"), Policy: PolicyArrears}}, map[string][2]string{"LOAN": {"0", "200"}}},
		{"NO_ARREARS skips", "150", []Claim{{Code: "VOL", Priority: 2, Due: dec("200"), Policy: PolicyNoArrears}}, map[string][2]string{"VOL": {"0", "0"}}},
		{"priority decides who is short", "150", []Claim{
			{Code: "LOW", Priority: 9, Due: dec("100"), Policy: PolicyNoArrears},
			{Code: "HIGH", Priority: 1, Due: dec("100"), Policy: PolicyPartial},
		}, map[string][2]string{"HIGH": {"100", "0"}, "LOW": {"0", "0"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AllocateDeductions(dec(tc.available), tc.claims)
			if len(got) != len(tc.claims) {
				t.Fatalf("got %d allocations", len(got))
			}
			for _, a := range got {
				w := tc.want[a.Code]
				if !a.Taken.Equal(dec(w[0])) || !a.Arrears.Equal(dec(w[1])) {
					t.Errorf("%s: taken %s arrears %s; want %s %s", a.Code, a.Taken, a.Arrears, w[0], w[1])
				}
			}
		})
	}
}

func TestRecoveryDueCapsAtLimit(t *testing.T) {
	if got := RecoveryDue(dec("200"), dec("50"), dec("1")); !got.Equal(dec("50")) {
		t.Fatalf("balance under cap = %s", got)
	}
	if got := RecoveryDue(dec("200"), dec("300"), dec("0.5")); !got.Equal(dec("100")) {
		t.Fatalf("cap at 50%% = %s", got)
	}
}

func TestAllocateDeductionsConservesNet(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		available := decimal.New(rapid.Int64Range(0, 500_000).Draw(t, "availableCents"), -2)
		n := rapid.IntRange(1, 5).Draw(t, "n")
		policies := []ArrearsPolicy{PolicyArrears, PolicyNoArrears, PolicyPartial}
		claims := make([]Claim, n)
		for i := range claims {
			claims[i] = Claim{
				Code:     string(rune('A' + i)),
				Priority: rapid.IntRange(0, 9).Draw(t, "priority"),
				Due:      decimal.New(rapid.Int64Range(0, 100_000).Draw(t, "dueCents"), -2),
				Policy:   policies[rapid.IntRange(0, 2).Draw(t, "policy")],
			}
		}
		total := decimal.Zero
		for _, a := range AllocateDeductions(available, claims) {
			if a.Taken.IsNegative() || a.Arrears.IsNegative() {
				t.Fatalf("negative allocation %+v", a)
			}
			total = total.Add(a.Taken)
		}
		if total.GreaterThan(available) {
			t.Fatalf("took %s from %s available", total, available)
		}
	})
}
