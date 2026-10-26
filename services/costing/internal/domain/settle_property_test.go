package domain

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

var allMethods = []Method{FIFO, LIFO, LIFODate, WeightedAvgRunning, WeightedAvgPeriodic, MovingAvg, Standard}

// genStream draws receipts in the first half of January and issues in the
// second half, with total issued quantity never above total received, so stock
// always covers every issue. Values have two decimals, so conservation is exact.
func genStream(t *rapid.T) ([]Txn, decimal.Decimal) {
	standard := dec("12.50")
	nR := rapid.IntRange(1, 8).Draw(t, "receipts")
	var txns []Txn
	total := 0
	for i := 0; i < nR; i++ {
		q := rapid.IntRange(1, 100).Draw(t, "receiptQty")
		cents := rapid.IntRange(100, 2000).Draw(t, "receiptCents")
		total += q
		txns = append(txns, Txn{ID: fmt.Sprintf("R%02d", i), Kind: Receipt, Qty: decimal.NewFromInt(int64(q)),
			Value: decimal.New(int64(q*cents), -2), FinancialDate: jan(1 + i%15)})
	}
	remaining := total
	nI := rapid.IntRange(0, 6).Draw(t, "issues")
	for i := 0; i < nI && remaining > 0; i++ {
		q := rapid.IntRange(1, remaining).Draw(t, "issueQty")
		remaining -= q
		txns = append(txns, Txn{ID: fmt.Sprintf("I%02d", i), Kind: Issue, Qty: decimal.NewFromInt(int64(q)), FinancialDate: jan(16 + i%12)})
	}
	return txns, standard
}

func settleFor(t *rapid.T, txns []Txn, m Method, standard decimal.Decimal) Result {
	res, err := Settle(txns, Options{Method: m, Scale: 2, Standard: standard})
	if err != nil {
		t.Fatalf("%s: %v", m, err)
	}
	return res
}

// Property: nothing is created or lost. With stock covering every issue,
// the value received equals the value issued plus the value remaining, and each
// issue is settled for exactly its quantity.
func TestProperty_ValueIsConserved(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		txns, standard := genStream(t)
		for _, m := range allMethods {
			res := settleFor(t, txns, m, standard)
			if len(res.Exceptions) != 0 {
				t.Fatalf("%s: unexpected exceptions %+v", m, res.Exceptions)
			}
			in := decimal.Zero
			for _, x := range txns {
				if x.Kind == Receipt {
					if m == Standard {
						in = in.Add(x.Qty.Mul(standard))
					} else {
						in = in.Add(x.Value)
					}
				}
			}
			out := res.RemainingValue
			for _, c := range res.IssueCosts {
				out = out.Add(c)
			}
			if !in.Equal(out) {
				t.Fatalf("%s: received %s != issued+remaining %s", m, in, out)
			}
			settledQty := map[string]decimal.Decimal{}
			for _, s := range res.Settlements {
				settledQty[s.IssueID] = settledQty[s.IssueID].Add(s.Qty)
			}
			for _, x := range txns {
				if x.Kind == Issue && !settledQty[x.ID].Equal(x.Qty) {
					t.Fatalf("%s: issue %s settled %s of %s", m, x.ID, settledQty[x.ID], x.Qty)
				}
			}
		}
	})
}

// Property: the settlement sequence is a function of the multiset of inputs
// (BR-20.2). A permutation of the input yields identical settlements.
func TestProperty_SettlementIsDeterministic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		txns, standard := genStream(t)
		shuffled := append([]Txn{}, txns...)
		for i := len(shuffled) - 1; i > 0; i-- {
			j := rapid.IntRange(0, i).Draw(t, "swap")
			shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
		}
		for _, m := range allMethods {
			a := settleFor(t, txns, m, standard)
			b := settleFor(t, shuffled, m, standard)
			if fmt.Sprint(a.Settlements) != fmt.Sprint(b.Settlements) {
				t.Fatalf("%s: settlements differ after permutation", m)
			}
			if !a.RemainingValue.Equal(b.RemainingValue) {
				t.Fatalf("%s: remaining value differs after permutation", m)
			}
		}
	})
}

// genShortStream draws receipts and issues in any order with no bound on the
// issued quantity, so issues can run stock negative.
func genShortStream(t *rapid.T) []Txn {
	n := rapid.IntRange(1, 20).Draw(t, "txns")
	txns := make([]Txn, 0, n)
	for i := 0; i < n; i++ {
		q := int64(rapid.IntRange(1, 100).Draw(t, "qty"))
		day := jan(rapid.IntRange(1, 28).Draw(t, "day"))
		if rapid.Bool().Draw(t, "receipt") {
			cents := int64(rapid.IntRange(0, 2000).Draw(t, "cents"))
			txns = append(txns, Txn{ID: fmt.Sprintf("T%02d", i), Kind: Receipt, Qty: decimal.NewFromInt(q), Value: decimal.New(q*cents, -2), FinancialDate: day})
		} else {
			txns = append(txns, Txn{ID: fmt.Sprintf("T%02d", i), Kind: Issue, Qty: decimal.NewFromInt(q), FinancialDate: day})
		}
	}
	return txns
}

// Property: with shortages (BR-20.3) every issue is settled or listed for its
// whole quantity, receipt value is either settled or still on hand, stock
// quantity ties to receipts minus issues, and stock is empty while any
// exception remains.
func TestProperty_ShortagesBalance(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		txns := genShortStream(t)
		standard := dec("12.50")
		for _, m := range allMethods {
			res := settleFor(t, txns, m, standard)
			in, net := decimal.Zero, decimal.Zero
			for _, x := range txns {
				if x.Kind == Receipt {
					net = net.Add(x.Qty)
					if m == Standard {
						in = in.Add(x.Qty.Mul(standard))
					} else {
						in = in.Add(x.Value)
					}
				} else {
					net = net.Sub(x.Qty)
				}
			}
			covered, costs := map[string]decimal.Decimal{}, map[string]decimal.Decimal{}
			settledValue, shortQty := decimal.Zero, decimal.Zero
			for _, s := range res.Settlements {
				covered[s.IssueID] = covered[s.IssueID].Add(s.Qty)
				costs[s.IssueID] = costs[s.IssueID].Add(s.Value)
				settledValue = settledValue.Add(s.Value)
			}
			for _, x := range res.Exceptions {
				covered[x.IssueID] = covered[x.IssueID].Add(x.Qty)
				costs[x.IssueID] = costs[x.IssueID].Add(x.Value)
				shortQty = shortQty.Add(x.Qty)
			}
			if !in.Equal(settledValue.Add(res.RemainingValue)) {
				t.Fatalf("%s: received %s != settled %s + remaining %s", m, in, settledValue, res.RemainingValue)
			}
			if !res.RemainingQty.Sub(shortQty).Equal(net) {
				t.Fatalf("%s: remaining %s - short %s != receipts - issues %s", m, res.RemainingQty, shortQty, net)
			}
			if shortQty.IsPositive() && !res.RemainingQty.IsZero() {
				t.Fatalf("%s: %s left in stock while %s is short", m, res.RemainingQty, shortQty)
			}
			for _, x := range txns {
				if x.Kind == Issue && (!covered[x.ID].Equal(x.Qty) || !costs[x.ID].Equal(res.IssueCosts[x.ID])) {
					t.Fatalf("%s: issue %s covered %s of %s, cost %s vs rows %s", m, x.ID, covered[x.ID], x.Qty, res.IssueCosts[x.ID], costs[x.ID])
				}
			}
		}
	})
}
