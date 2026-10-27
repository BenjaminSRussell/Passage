package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// Property: bucket totals always sum to the layer total, whatever the ages.
func TestPropertyAgingBucketsSumToTotal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		asOf := jan(31)
		n := rapid.IntRange(0, 30).Draw(t, "layers")
		layers := make([]Layer, n)
		want := decimal.Zero
		for i := range layers {
			days := rapid.IntRange(0, 1000).Draw(t, "days")
			qty := rapid.IntRange(1, 500).Draw(t, "qty")
			val := rapid.IntRange(0, 100000).Draw(t, "val")
			layers[i] = Layer{ReceiptID: "L", ReceivedOn: asOf.Add(-time.Duration(days) * 24 * time.Hour),
				Qty: decimal.NewFromInt(int64(qty)), Value: decimal.New(int64(val), -2)}
			want = want.Add(layers[i].Value)
		}
		rep, err := Aging(layers, asOf, DefaultBuckets())
		if err != nil {
			t.Fatal(err)
		}
		sum := decimal.Zero
		for _, b := range rep.Buckets {
			sum = sum.Add(b.Value)
		}
		if !sum.Equal(want) || !rep.TotalValue.Equal(want) {
			t.Fatalf("buckets %s, total %s, want %s", sum, rep.TotalValue, want)
		}
	})
}

// Property: the primary ledger's result is identical whatever other ledgers
// are configured beside it, because ledgers share only the sequence.
func TestPropertyPrimaryLedgerIndependent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		txns := fr201()
		rate := decimal.New(int64(rapid.IntRange(1, 300).Draw(t, "rate")), -2)
		alone, err := SettleLedgers(txns, []LedgerSpec{{ID: "P", Method: WeightedAvgPeriodic, Currency: "EUR", Scale: 2, Rate: decimal.NewFromInt(1), Primary: true}})
		if err != nil {
			t.Fatal(err)
		}
		withGroup, err := SettleLedgers(txns, []LedgerSpec{
			{ID: "P", Method: WeightedAvgPeriodic, Currency: "EUR", Scale: 2, Rate: decimal.NewFromInt(1), Primary: true},
			{ID: "G", Method: FIFO, Currency: "USD", Scale: 2, Rate: rate},
		})
		if err != nil {
			t.Fatal(err)
		}
		if !alone[0].Result.IssueCosts["I1"].Equal(withGroup[0].Result.IssueCosts["I1"]) {
			t.Fatalf("primary cost changed: %s vs %s", alone[0].Result.IssueCosts["I1"], withGroup[0].Result.IssueCosts["I1"])
		}
	})
}

// Property: marking only moves value between issues. With random marks on a
// stream whose stock covers every issue, nothing is created or lost under any
// method, no exception appears, and each issue settles exactly its quantity.
func TestPropertyMarkingConservesValue(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		txns, standard := genStream(t)
		var receipts []string
		for _, x := range txns {
			if x.Kind == Receipt {
				receipts = append(receipts, x.ID)
			}
		}
		for i := range txns {
			if txns[i].Kind == Issue && rapid.Bool().Draw(t, "marked") {
				txns[i].MarkedReceiptID = rapid.SampledFrom(receipts).Draw(t, "receipt")
			}
		}
		for _, m := range allMethods {
			res := settleFor(t, txns, m, standard)
			if len(res.Exceptions) != 0 {
				t.Fatalf("%s: unexpected exceptions %+v", m, res.Exceptions)
			}
			in, out := decimal.Zero, res.RemainingValue
			for _, x := range txns {
				if x.Kind == Receipt {
					if m == Standard {
						in = in.Add(x.Qty.Mul(standard))
					} else {
						in = in.Add(x.Value)
					}
				}
			}
			settledQty := map[string]decimal.Decimal{}
			for _, s := range res.Settlements {
				out = out.Add(s.Value)
				settledQty[s.IssueID] = settledQty[s.IssueID].Add(s.Qty)
			}
			if !in.Equal(out) {
				t.Fatalf("%s: received %s != settled+remaining %s", m, in, out)
			}
			for _, x := range txns {
				if x.Kind == Issue && !settledQty[x.ID].Equal(x.Qty) {
					t.Fatalf("%s: issue %s settled %s of %s", m, x.ID, settledQty[x.ID], x.Qty)
				}
			}
		}
	})
}
