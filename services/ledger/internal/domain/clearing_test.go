package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestClearingOutcome_AC11_12(t *testing.T) {
	debit, credit, net := ClearingTotals([]decimal.Decimal{dec("100.00"), dec("-99.98")})
	if !net.Equal(dec("0.02")) || !debit.Equal(dec("100")) || !credit.Equal(dec("99.98")) {
		t.Fatalf("totals %s %s %s", debit, credit, net)
	}
	user := &Tolerance{Debit: dec("0.05"), Credit: dec("0.05")}
	if got := ClearingOutcomeOf(net, debit, credit, user, false); got != OutcomeDifference {
		t.Fatalf("tolerance 0.05: %s", got)
	}
	tight := &Tolerance{Debit: dec("0.01"), Credit: dec("0.01")}
	if got := ClearingOutcomeOf(net, debit, credit, tight, true); got != OutcomeResidual {
		t.Fatalf("tolerance 0.01 with residual item: %s", got)
	}
	if got := ClearingOutcomeOf(net, debit, credit, tight, false); got != OutcomeUnbalanced {
		t.Fatalf("tolerance 0.01 without residual item: %s", got)
	}
	if got := ClearingOutcomeOf(net, debit, credit, nil, false); got != OutcomeUnbalanced {
		t.Fatalf("no tolerance: %s", got)
	}
}

func TestClearingOutcome_ZeroAndPercent(t *testing.T) {
	_, _, net := ClearingTotals([]decimal.Decimal{dec("50"), dec("-50")})
	if got := ClearingOutcomeOf(net, dec("50"), dec("50"), nil, false); got != OutcomeBalanced {
		t.Fatalf("zero net: %s", got)
	}
	// The percentage applies to Σ|amount| = 1,999.00: 1.00 is 0.05% of it, so a
	// 0.05% limit (0.9995) refuses it and a 0.06% limit (1.1994) accepts it.
	if WithinTolerance(dec("1.00"), dec("1000"), dec("999"), Tolerance{Debit: dec("5"), Credit: dec("5"), Percent: dec("0.05")}) {
		t.Fatal("percentage limit must apply")
	}
	if !WithinTolerance(dec("1.00"), dec("1000"), dec("999"), Tolerance{Debit: dec("5"), Credit: dec("5"), Percent: dec("0.06")}) {
		t.Fatal("the percentage is of debit plus credit, not of the larger side")
	}
	// Each side has its own absolute limit (BR-11.12 abs_debit / abs_credit).
	sided := Tolerance{Debit: dec("0.05"), Credit: dec("0.01")}
	if !WithinTolerance(dec("0.02"), dec("100"), dec("99.98"), sided) {
		t.Fatal("a debit residual uses the debit limit")
	}
	if WithinTolerance(dec("-0.02"), dec("99.98"), dec("100"), sided) {
		t.Fatal("a credit residual uses the credit limit")
	}
	if WithinTolerance(dec("0.01"), dec("10"), dec("9.99"), Tolerance{}) {
		t.Fatal("a zero limit writes nothing off")
	}
}

func TestValidToleranceCode(t *testing.T) {
	for code, want := range map[string]bool{"STD": true, "T_1": true, "": false, "lower": false, "TOOLONGCODE1": false} {
		if ValidToleranceCode(code) != want {
			t.Errorf("%q", code)
		}
	}
}
