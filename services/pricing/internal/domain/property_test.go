package domain_test

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

// TestResolve_PropertyDeterministic (AC-26.4, AC2): for random record sets and
// quantities, the explained result is byte-identical across reloads in any order.
func TestResolve_PropertyDeterministic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		recs := genRecords(rapid.IntRange(1, 40).Draw(t, "records"))
		qty := rapid.IntRange(1, 500).Draw(t, "qty")
		in := domain.Input{Header: header("USD"), Lines: []domain.Line{line(fmt.Sprint(qty))}, Records: recs}
		first, refFirst, err := domain.ExplainTrace(domain.Resolve(in))
		if err != nil {
			t.Fatal(err)
		}
		in.Records = rapid.Permutation(recs).Draw(t, "order")
		second, refSecond, _ := domain.ExplainTrace(domain.Resolve(in))
		if refFirst != refSecond || !bytes.Equal(first, second) {
			t.Fatalf("result changed with record order for qty %d", qty)
		}
	})
}

// randomRequest builds a seeded random document and record set over a small key
// space, so accesses, breaks, search_next, exclusive and discounts collide often.
func randomRequest(rng *rand.Rand) domain.Input {
	customers := []string{"C1", "C2"}
	groups := []string{"PG-A", "PG-B"}
	products := []string{"P1", "P2", "P3"}
	pgroups := []string{"G1", "G2"}
	currency := []string{"USD", "EUR", "JPY"}[rng.Intn(3)]
	h := domain.Header{CustomerID: customers[rng.Intn(2)], CustomerGroupID: groups[rng.Intn(2)], Currency: currency, PricingDate: pricingDate}
	in := domain.Input{Header: h}
	for i := 0; i < 1+rng.Intn(10); i++ {
		in.Lines = append(in.Lines, domain.Line{No: i + 1, ProductID: products[rng.Intn(3)], ProductGroupID: pgroups[rng.Intn(2)],
			Unit: "EA", Quantity: decimal.New(int64(1+rng.Intn(600)), -int32(rng.Intn(3)))})
	}
	types := []domain.ConditionType{domain.TypePrice, domain.TypePrice, domain.TypeLineDisc, domain.TypeTotalDisc}
	accesses := domain.DefaultAccesses()
	for i := 0; i < rng.Intn(30); i++ {
		acc := accesses[1+rng.Intn(len(accesses)-1)] // variant records need a variant; keep to the other eight
		r := domain.Record{ID: fmt.Sprintf("R%02d", i), Status: domain.StatusActive, Type: types[rng.Intn(len(types))],
			CustomerKind: acc.Customer, ProductKind: acc.Product, Currency: currency, Unit: []string{"EA", ""}[rng.Intn(2)],
			FromQuantity: decimal.NewFromInt(int64([]int{0, 0, 10, 100, 500}[rng.Intn(5)])),
			Amount:       decimal.New(int64(rng.Intn(20000)), -2), ValidFrom: validFrom, ValidTo: validTo,
			SearchNext: rng.Intn(4) == 0, Exclusive: rng.Intn(6) == 0}
		switch acc.Customer {
		case domain.CustomerKindCustomer:
			r.CustomerKey = customers[rng.Intn(2)]
		case domain.CustomerKindGroup:
			r.CustomerKey = groups[rng.Intn(2)]
		}
		switch acc.Product {
		case domain.ProductKindProduct:
			r.ProductKey = products[rng.Intn(3)]
		case domain.ProductKindGroup:
			r.ProductKey = pgroups[rng.Intn(2)]
		}
		if r.Type != domain.TypePrice {
			r.Amount = decimal.New(int64(rng.Intn(500)), -2)
			r.DiscPct1 = decimal.NewFromInt(int64(rng.Intn(101)))
			r.DiscPct2 = decimal.NewFromInt(int64(rng.Intn(30)))
		}
		in.Records = append(in.Records, r)
	}
	return in
}

// TestResolve_TenThousandRandomRequests (AC2, AC-26.4): 10,000 seeded random requests
// each resolve to byte-identical output when the records arrive in another order,
// and every result keeps the document invariants of BR-26.3 and BR-26.10.
func TestResolve_TenThousandRandomRequests(t *testing.T) {
	n := 10000
	if testing.Short() {
		n = 500
	}
	rng := rand.New(rand.NewSource(26))
	for i := 0; i < n; i++ {
		in := randomRequest(rng)
		res := domain.Resolve(in)
		first, ref, err := domain.ExplainTrace(res)
		if err != nil {
			t.Fatal(err)
		}
		shuffled := append([]domain.Record(nil), in.Records...)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		again, refAgain, _ := domain.ExplainTrace(domain.Resolve(domain.Input{Header: in.Header, Lines: in.Lines, Records: shuffled}))
		if ref != refAgain || !bytes.Equal(first, again) {
			t.Fatalf("request %d: output depends on record order", i)
		}

		exp := int32(2)
		if in.Header.Currency == "JPY" {
			exp = 0
		}
		sum := decimal.Zero
		for _, l := range res.Lines {
			if l.LineNet.IsNegative() || l.NetUnit.IsNegative() {
				t.Fatalf("request %d line %d: negative net %s (BR-26.10)", i, l.No, l.LineNet)
			}
			if !l.LineNet.Equal(l.LineNet.Round(exp)) {
				t.Fatalf("request %d line %d: line net %s not rounded to %d decimals (BR-26.3)", i, l.No, l.LineNet, exp)
			}
			if l.NetUnit.GreaterThan(l.UnitPrice) {
				t.Fatalf("request %d line %d: discounts raised the price %s above %s", i, l.No, l.NetUnit, l.UnitPrice)
			}
			sum = sum.Add(l.LineNet)
		}
		if !res.Subtotal.Equal(sum) || !res.OrderNet.Equal(res.Subtotal.Sub(res.TotalDiscount)) || res.TotalDiscount.IsNegative() || res.OrderNet.IsNegative() {
			t.Fatalf("request %d: subtotal %s (lines %s), discount %s, net %s do not reconcile", i, res.Subtotal, sum, res.TotalDiscount, res.OrderNet)
		}
	}
}

// TestSplitCommission_SumsExactly: shares always reconcile to the amount to the cent.
func TestSplitCommission_SumsExactly(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cents := rapid.Int64Range(-10000000, 10000000).Draw(t, "cents")
		n := rapid.IntRange(1, 5).Draw(t, "reps")
		shares := make([]domain.Share, n)
		for i := range shares {
			shares[i] = domain.Share{RepID: fmt.Sprint(i), SharePct: d(fmt.Sprint(rapid.IntRange(0, 100).Draw(t, "share")))}
		}
		amount := decimal.New(cents, -2)
		total := decimal.Zero
		for _, s := range domain.SplitCommission(amount, "USD", shares) {
			total = total.Add(s.Amount)
		}
		if !total.Equal(amount) {
			t.Fatalf("parts sum to %s, want %s", total, amount)
		}
	})
}

// BenchmarkResolve_TenLines measures the engine part of AC3 (p95 < 20 ms for a
// 10-line order): ten lines against 1,000 records with explain serialisation.
// Run: go test ./internal/domain -bench Resolve -run '^$'
func BenchmarkResolve_TenLines(b *testing.B) {
	rng := rand.New(rand.NewSource(3))
	in := randomRequest(rng)
	for len(in.Lines) < 10 {
		in = randomRequest(rng)
	}
	in.Lines = in.Lines[:10]
	for len(in.Records) < 1000 {
		extra := randomRequest(rng)
		for _, r := range extra.Records {
			r.ID = fmt.Sprintf("X%05d", len(in.Records))
			r.Currency = in.Header.Currency
			in.Records = append(in.Records, r)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := domain.ExplainTrace(domain.Resolve(in)); err != nil {
			b.Fatal(err)
		}
	}
}
