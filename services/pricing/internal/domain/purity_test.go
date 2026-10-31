package domain_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

// fullRequest builds a seeded random request over every condition type the engine prices
// (PRICE, LINE_DISC, MULTILINE_DISC, TOTAL_DISC, SURCHARGE), all nine accesses including
// variants, campaign records, quantity breaks, search_next, exclusive, units, and records
// the header must filter out (other currency, DRAFT / EXPIRED / WITHDRAWN, out of validity).
func fullRequest(rng *rand.Rand) domain.Input {
	pick := func(xs ...string) string { return xs[rng.Intn(len(xs))] }
	currency := pick("USD", "EUR", "JPY")
	day := func(s string) time.Time { d, _ := time.Parse("2006-01-02", s); return d }
	h := domain.Header{CustomerID: pick("C1", "C2"), CustomerGroupID: pick("CG-A", "CG-B", ""), Currency: currency, PricingDate: day("2027-03-10")}
	in := domain.Input{Header: h, SmallOrderThreshold: domain.SmallOrderThreshold(currency)}
	for i, n := 0, 1+rng.Intn(10); i < n; i++ {
		in.Lines = append(in.Lines, domain.Line{No: i + 1, ProductID: pick("P1", "P2", "P3"), ProductGroupID: pick("G1", "G2", ""),
			Variant: pick("", "RED"), Unit: pick("EA", "EA", "BOX"), Quantity: decimal.New(int64(1+rng.Intn(600)), -int32(rng.Intn(3)))})
	}
	types := []domain.ConditionType{domain.TypePrice, domain.TypePrice, domain.TypeLineDisc, domain.TypeMultiline, domain.TypeTotalDisc, domain.TypeSurcharge}
	ids := rng.Perm(60) // unique ids (they are primary keys) in an order unrelated to creation
	// Keys mostly name this request's customer, group and lines, so most lines price and
	// records compete; the rest are near misses.
	for i, n := 0, rng.Intn(40); i < n; i++ {
		acc := domain.DefaultAccesses()[rng.Intn(9)]
		l := in.Lines[rng.Intn(len(in.Lines))]
		own := rng.Intn(4) != 0
		r := domain.Record{ID: fmt.Sprintf("R%02d", ids[i]), Status: pick(domain.StatusActive, domain.StatusActive, domain.StatusActive, domain.StatusDraft, domain.StatusExpired, domain.StatusWithdrawn),
			Type: types[rng.Intn(len(types))], CustomerKind: acc.Customer, ProductKind: acc.Product,
			Currency: pick(currency, currency, currency, "USD"), Unit: pick("EA", "", "BOX"),
			FromQuantity: decimal.NewFromInt(int64([]int{0, 0, 10, 100, 500}[rng.Intn(5)])),
			Amount:       decimal.New(int64(rng.Intn(20000)), -2), DiscPct1: decimal.NewFromInt(int64(rng.Intn(101))),
			ValidFrom: day(pick("2027-01-01", "2027-03-10", "2027-03-11")), ValidTo: day(pick("2027-12-31", "2027-03-10", "2027-03-09")),
			SearchNext: rng.Intn(4) == 0, Exclusive: rng.Intn(8) == 0, CampaignID: pick("", "", "", "CMP-1")}
		switch acc.Customer {
		case domain.CustomerKindCustomer:
			r.CustomerKey = pick("C1", "C2")
			if own {
				r.CustomerKey = h.CustomerID
			}
		case domain.CustomerKindGroup:
			r.CustomerKey = pick("CG-A", "CG-B")
			if own && h.CustomerGroupID != "" {
				r.CustomerKey = h.CustomerGroupID
			}
		}
		switch acc.Product {
		case domain.ProductKindProduct:
			r.ProductKey = pick("P1", "P2", "P3")
			if own {
				r.ProductKey = l.ProductID
			}
		case domain.ProductKindGroup:
			r.ProductKey = pick("G1", "G2")
			if own && l.ProductGroupID != "" {
				r.ProductKey = l.ProductGroupID
			}
		case domain.ProductKindVariant:
			r.ProductKey, r.Variant = pick("P1", "P2"), pick("RED", "BLUE")
			if own && l.Variant != "" {
				r.ProductKey, r.Variant = l.ProductID, l.Variant
			}
		}
		if r.Type == domain.TypePrice {
			r.DiscPct1 = decimal.NewFromInt(int64([]int{0, 0, 0, 10, 50}[rng.Intn(5)]))
		} else {
			r.Amount = decimal.New(int64(rng.Intn(500)), -2)
		}
		if r.Type != domain.TypeSurcharge {
			r.DiscPct2 = decimal.NewFromInt(int64(rng.Intn(30)))
		}
		if r.Type == domain.TypeTotalDisc || r.Type == domain.TypeSurcharge {
			r.ProductKind, r.ProductKey, r.Variant = domain.ProductKindAll, "", ""
		}
		in.Records = append(in.Records, r)
	}
	return in
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestResolve_PureAndDeterministicOverEveryConditionType (Wave 26 AC2, FR-26.4): for 10,000
// seeded random requests over every condition type, the whole result (lines, totals,
// surcharge, applied conditions, trace) is byte-identical when the request is resolved
// again, with its records in another order, and from a shared pre-bucketed RecordSet (the
// snapshot cache path); resolving never changes the input. Every 50th request is also
// resolved from one RecordSet by 8 goroutines at once, which must all agree (no shared
// mutable state; run with -race to check).
func TestResolve_PureAndDeterministicOverEveryConditionType(t *testing.T) {
	n := 10000
	if testing.Short() {
		n = 500
	}
	rng := rand.New(rand.NewSource(2604))
	applied := map[domain.ConditionType]int{}
	for i := 0; i < n; i++ {
		in := fullRequest(rng)
		before := mustJSON(t, in)
		first := mustJSON(t, domain.Resolve(in))
		if again := mustJSON(t, domain.Resolve(in)); !bytes.Equal(first, again) {
			t.Fatalf("request %d: a second resolve of the same input differs", i)
		}
		if after := mustJSON(t, in); !bytes.Equal(before, after) {
			t.Fatalf("request %d: Resolve changed its input", i)
		}
		shuffled := in
		shuffled.Records = append([]domain.Record(nil), in.Records...)
		rng.Shuffle(len(shuffled.Records), func(a, b int) { shuffled.Records[a], shuffled.Records[b] = shuffled.Records[b], shuffled.Records[a] })
		if got := mustJSON(t, domain.Resolve(shuffled)); !bytes.Equal(first, got) {
			t.Fatalf("request %d: result depends on record order", i)
		}
		set := domain.NewRecordSet(shuffled.Records)
		viaSet := domain.Input{Header: in.Header, Lines: in.Lines, Set: set, SmallOrderThreshold: in.SmallOrderThreshold}
		if got := mustJSON(t, domain.Resolve(viaSet)); !bytes.Equal(first, got) {
			t.Fatalf("request %d: the RecordSet path differs from the record list", i)
		}
		if i%50 == 0 {
			var wg sync.WaitGroup
			out := make([][]byte, 8)
			for g := range out {
				wg.Add(1)
				go func() {
					defer wg.Done()
					b, _ := json.Marshal(domain.Resolve(viaSet))
					out[g] = b
				}()
			}
			wg.Wait()
			for g, b := range out {
				if !bytes.Equal(first, b) {
					t.Fatalf("request %d: concurrent resolve %d on a shared RecordSet differs", i, g)
				}
			}
		}
		res := domain.Resolve(in)
		for _, l := range res.Lines {
			for _, a := range l.Applied {
				applied[a.Type]++
			}
		}
		for _, a := range res.Applied {
			applied[a.Type]++
		}
	}
	// The generator must keep exercising every condition type (counts are logged, not tuned).
	t.Logf("applied conditions over %d requests: %v", n, applied)
	for _, ct := range []domain.ConditionType{domain.TypePrice, domain.TypeLineDisc, domain.TypeMultiline, domain.TypeTotalDisc, domain.TypeSurcharge} {
		if applied[ct] == 0 {
			t.Errorf("no request applied %s; the generator no longer covers it", ct)
		}
	}
}
