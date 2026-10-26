package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// balancedEntry draws 2..n lines whose debit and credit totals are equal in
// cents by construction: debit lines are drawn first, then credits split their
// sum.
func balancedEntry(t *rapid.T) []LineInput {
	nDebits := rapid.IntRange(1, 6).Draw(t, "debits")
	debits := make([]int64, nDebits)
	var total int64
	for i := range debits {
		debits[i] = rapid.Int64Range(1, 1_000_000).Draw(t, "debit")
		total += debits[i]
	}
	var credits []int64
	if total >= 2 && rapid.Bool().Draw(t, "split") {
		cut := rapid.Int64Range(1, total-1).Draw(t, "cut")
		credits = []int64{cut, total - cut}
	} else {
		credits = []int64{total}
	}
	var lines []LineInput
	for _, c := range debits {
		lines = append(lines, LineInput{Account: "1000", Side: SideDebit, Amount: decimal.New(c, -2), Currency: "USD"})
	}
	for _, c := range credits {
		lines = append(lines, LineInput{Account: "2000", Side: SideCredit, Amount: decimal.New(c, -2), Currency: "USD"})
	}
	return lines
}

func TestProperty_BalancedEntriesPostAndUnbalancedAreRejected(t *testing.T) {
	pd := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	rapid.Check(t, func(t *rapid.T) {
		lines := balancedEntry(t)
		res := ValidateEntry(testRules(), testAccounts(), pd, today, lines)
		if !res.Valid() {
			t.Fatalf("balanced entry rejected: %+v", res.Violations)
		}
		if !res.Debit.Equal(res.Credit) {
			t.Fatalf("debit %s != credit %s", res.Debit, res.Credit)
		}
		// Shift one debit by a cent: must be rejected as unbalanced, with no other violation.
		i := rapid.IntRange(0, len(lines)-1).Draw(t, "mutate")
		lines[i].Amount = lines[i].Amount.Add(decimal.New(1, -2))
		bad := ValidateEntry(testRules(), testAccounts(), pd, today, lines)
		if len(bad.Violations) != 1 || bad.Violations[0].Code != "LEDGER_UNBALANCED" {
			t.Fatalf("mutated entry: want only LEDGER_UNBALANCED, got %+v", bad.Violations)
		}
	})
}

func TestProperty_AggregationPreservesTotals(t *testing.T) {
	pd := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	rapid.Check(t, func(t *rapid.T) {
		lines := balancedEntry(t)
		res := ValidateEntry(testRules(), testAccounts(), pd, today, lines)
		if !res.Valid() {
			t.Fatalf("unexpected violations: %+v", res.Violations)
		}
		deltas := AggregateBalances(PeriodKey(pd), res.Lines)
		var debit, credit decimal.Decimal
		debit, credit = decimal.Zero, decimal.Zero
		var count int64
		for _, d := range deltas {
			debit = debit.Add(d.Debit)
			credit = credit.Add(d.Credit)
			count += d.LineCount
		}
		if !debit.Equal(res.Debit) || !credit.Equal(res.Credit) || count != int64(len(lines)) {
			t.Fatalf("aggregate totals drift: debit %s/%s credit %s/%s lines %d/%d",
				debit, res.Debit, credit, res.Credit, count, len(lines))
		}
		if !debit.Sub(credit).IsZero() {
			t.Fatalf("projection net must sum to zero, got %s", debit.Sub(credit))
		}
	})
}
