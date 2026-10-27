package domain_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
)

func lineState(t testing.TB, no int, qty, price, delivery string) domain.LineState {
	return domain.LineState{LineNo: no, Qty: dec(t, qty), UnitPrice: dec(t, price), DeliveryDate: day(t, delivery)}
}

// TestReapprovalRequired covers AC2 and AC3: a quantity raised from 100 to 110
// needs re-approval under the 5 % rule, while a one-day delivery move does not.
func TestReapprovalRequired(t *testing.T) {
	base := []domain.LineState{lineState(t, 1, "100", "10", "2026-11-02")}
	tests := []struct {
		name   string
		after  []domain.LineState
		rules  []domain.ReapprovalRule
		expect bool
	}{
		{"qty up 10 % trips the 5 % rule (AC2)", []domain.LineState{lineState(t, 1, "110", "10", "2026-11-02")}, domain.DefaultReapprovalRules(), true},
		{"qty up exactly 5 % does not", []domain.LineState{lineState(t, 1, "105", "10", "2026-11-02")}, domain.DefaultReapprovalRules(), false},
		{"qty decrease never trips the increase rule", []domain.LineState{lineState(t, 1, "50", "10", "2026-11-02")}, domain.DefaultReapprovalRules(), false},
		{"delivery move excluded by the rules (AC3)", []domain.LineState{lineState(t, 1, "100", "10", "2026-11-03")}, domain.DefaultReapprovalRules(), false},
		{"delivery move under a delivery rule", []domain.LineState{lineState(t, 1, "100", "10", "2026-11-03")},
			[]domain.ReapprovalRule{{Kind: domain.RuleDeliveryDateChange}}, true},
		{"price up 6 % trips the price rule", []domain.LineState{lineState(t, 1, "100", "10.60", "2026-11-02")}, domain.DefaultReapprovalRules(), true},
		{"new line trips NEW_LINE", []domain.LineState{lineState(t, 1, "100", "10", "2026-11-02"), lineState(t, 2, "1", "1", "2026-11-02")}, domain.DefaultReapprovalRules(), true},
		{"new line ignored without NEW_LINE", []domain.LineState{lineState(t, 1, "100", "10", "2026-11-02"), lineState(t, 2, "1", "1", "2026-11-02")},
			[]domain.ReapprovalRule{{Kind: domain.RuleQtyIncreasePct, Threshold: dec(t, "5")}}, false},
		{"any change trips ANY_CHANGE on a delivery move", []domain.LineState{lineState(t, 1, "100", "10", "2026-11-03")},
			[]domain.ReapprovalRule{{Kind: domain.RuleAnyChange}}, true},
		{"removed line trips ANY_CHANGE only", nil, []domain.ReapprovalRule{{Kind: domain.RuleAnyChange}}, true},
		{"removed line ignored by threshold rules", nil, domain.DefaultReapprovalRules(), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.ReapprovalRequired(base, tc.after, tc.rules); got != tc.expect {
				t.Errorf("got %v, want %v", got, tc.expect)
			}
		})
	}
}

func TestReapprovalRequired_IncreaseFromZeroAlwaysTrips(t *testing.T) {
	before := []domain.LineState{lineState(t, 1, "0", "10", "2026-11-02")}
	after := []domain.LineState{lineState(t, 1, "1", "10", "2026-11-02")}
	if !domain.ReapprovalRequired(before, after, []domain.ReapprovalRule{{Kind: domain.RuleQtyIncreasePct, Threshold: dec(t, "99")}}) {
		t.Error("increase from zero should exceed any threshold")
	}
}
