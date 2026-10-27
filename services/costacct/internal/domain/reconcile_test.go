package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestReconcile(t *testing.T) {
	ranges := []AccountRange{{ElementID: "E-PAY", From: "600000", To: "609999"}, {ElementID: "E-SUP", From: "610000", To: "619999"}}
	journal := []JournalBalance{
		{MainAccount: "600100", Amount: dec("100.00")},
		{MainAccount: "600200", Amount: dec("50.25")},
		{MainAccount: "610000", Amount: dec("30.00")},
		{MainAccount: "400000", Amount: dec("-999.00")}, // revenue: not a primary cost
	}
	t.Run("AC-38.6 matching month reconciles with 0.00 difference", func(t *testing.T) {
		got := Reconcile(ranges, journal, map[string]decimal.Decimal{"E-PAY": dec("150.25"), "E-SUP": dec("30")})
		if !got.Reconciled || len(got.Lines) != 2 || len(got.UnmappedAccounts) != 1 || got.UnmappedAccounts[0].MainAccount != "400000" {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("a missing mirror and an orphan entry are both differences", func(t *testing.T) {
		got := Reconcile(ranges, journal, map[string]decimal.Decimal{"E-PAY": dec("100.00"), "E-OLD": dec("5")})
		want := map[string]string{"E-PAY": "50.25", "E-SUP": "30", "E-OLD": "-5"}
		if got.Reconciled || len(got.Lines) != 3 {
			t.Fatalf("%+v", got)
		}
		for _, l := range got.Lines {
			if !l.Difference.Equal(dec(want[l.ElementID])) {
				t.Errorf("%s difference = %s, want %s", l.ElementID, l.Difference, want[l.ElementID])
			}
		}
	})
}
