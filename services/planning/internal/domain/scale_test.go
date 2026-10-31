package domain

import (
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// EnvScaleItems sets the size of the scale run. Unset, a 5,000-item snapshot
// of the same shape keeps determinism covered in every test run; the issue's
// scale (AC2, AC-22.3) is PASSAGE_PLANNING_SCALE_ITEMS=50000, which peaks near
// 5.5 GB RSS and takes about 40 s, so it is opt-in.
const EnvScaleItems = "PASSAGE_PLANNING_SCALE_ITEMS"

// mrpSnapshot generates the mrp-50k dataset of the PRD test plan (issue #22,
// AC2 and AC-22.3) at n items (a multiple of 20): a 5-level BOM holding 10%,
// 20%, 20%, 25% and 25% of the items, 4n sales lines over 12 monthly periods,
// open purchase orders, and a mix of coverage codes and lot rules; n = 50,000
// gives 200,000 sales lines. The generator is seeded, so every call with the
// same n returns the same snapshot.
func mrpSnapshot(n int) []Item {
	rng := rand.New(rand.NewPCG(22, 2026))
	levels := []int{n / 10, n / 5, n / 5, n / 4, n / 4}
	ids := make([][]string, len(levels))
	var items []Item
	for lvl, n := range levels {
		for i := 0; i < n; i++ {
			ids[lvl] = append(ids[lvl], fmt.Sprintf("L%d-%05d", lvl, i))
		}
	}
	qty := func(lo, hi int64) decimal.Decimal { return decimal.NewFromInt(lo + rng.Int64N(hi-lo+1)) }
	for lvl := range levels {
		for i, id := range ids[lvl] {
			it := Item{ID: id, OrderType: OrderPurchase, LeadTimeDays: rng.IntN(21), OnHand: qty(0, 400)}
			switch r := rng.IntN(100); {
			case r < 55:
				it.Coverage = CoverageRequirement
			case r < 80:
				it.Coverage, it.PeriodDays = CoveragePeriod, 7+rng.IntN(24)
			default:
				it.Coverage = CoverageMinMax
				it.ReorderMin = qty(10, 200)
				it.ReorderMax = it.ReorderMin.Add(qty(50, 800))
			}
			if rng.IntN(4) == 0 && it.Coverage != CoverageMinMax {
				it.SafetyStock = qty(5, 100)
			}
			switch rng.IntN(5) {
			case 0:
				it.MinOrderQty = qty(10, 300)
			case 1:
				it.MultipleQty = qty(5, 50)
			case 2:
				it.MaxOrderQty = qty(2000, 20000)
			}
			if lvl+1 < len(levels) {
				it.OrderType, it.LeadTimeDays = OrderProduction, 1+rng.IntN(10)
				used := map[string]bool{}
				add := func(c string) {
					if !used[c] {
						used[c] = true
						it.Components = append(it.Components, BOMLine{ItemID: c, Qty: qty(1, 3)})
					}
				}
				// The items of the next level are dealt out over this level in
				// order, so every one of them has a parent and its low-level
				// code is its level; the other components are random, some two
				// levels down.
				next := ids[lvl+1]
				for j := (i*len(next) + len(ids[lvl]) - 1) / len(ids[lvl]); j < len(next) && j*len(ids[lvl])/len(next) == i; j++ {
					add(next[j])
				}
				for n := 2 + rng.IntN(3); len(it.Components) < n; {
					c := next[rng.IntN(len(next))]
					if lvl+2 < len(levels) && rng.IntN(5) == 0 {
						c = ids[lvl+2][rng.IntN(len(ids[lvl+2]))]
					}
					add(c)
				}
			}
			if rng.IntN(5) == 0 {
				it.Receipts = append(it.Receipts, Requirement{Ref: "PO-" + id, RefType: "purchase-line", Date: rng.IntN(90), Qty: qty(10, 500)})
			}
			items = append(items, it)
		}
	}
	index := make(map[string]int, len(items))
	for i := range items {
		index[items[i].ID] = i
	}
	for k := 0; k < 4*n; k++ {
		var id string
		if rng.IntN(10) < 7 {
			id = ids[0][rng.IntN(levels[0])]
		} else {
			lvl := 1 + rng.IntN(len(levels)-1)
			id = ids[lvl][rng.IntN(levels[lvl])]
		}
		it := &items[index[id]]
		it.Demand = append(it.Demand, Requirement{Ref: fmt.Sprintf("SO-%06d", k), RefType: RefSales, Date: 30*rng.IntN(12) + rng.IntN(30), Qty: qty(1, 100), Priority: PrioritySales})
	}
	return items
}

// TestRun_Scale50kDeterministic is AC2 and AC-22.3 at engine level when run
// with PASSAGE_PLANNING_SCALE_ITEMS=50000: the 50k-item, 12-period, 5-level
// plan runs three times (the second with the snapshot in reverse order) well
// inside 10 minutes, with one plan hash, and the explain tree of every planned
// order covers its whole quantity (AC3). Without the variable it runs the same
// checks on a 5,000-item snapshot. Database persistence of the result is not
// part of this measurement.
func TestRun_Scale50kDeterministic(t *testing.T) {
	if testing.Short() {
		t.Skip("scale plan in -short mode")
	}
	n := 5000
	if v := os.Getenv(EnvScaleItems); v != "" {
		var err error
		if n, err = strconv.Atoi(v); err != nil || n < 20 || n%20 != 0 {
			t.Fatalf("%s=%q: want a positive multiple of 20", EnvScaleItems, v)
		}
	}
	items := mrpSnapshot(n)
	index := make(map[string]*Item, len(items))
	sales := 0
	for i := range items {
		index[items[i].ID] = &items[i]
		sales += len(items[i].Demand)
	}
	llc, err := lowLevelCodes(items, index)
	if err != nil {
		t.Fatal(err)
	}
	perLevel := map[int]int{}
	for _, it := range items {
		perLevel[llc[it.ID]]++
	}
	if len(items) != n || sales != 4*n || len(perLevel) != 5 {
		t.Fatalf("dataset: %d items, %d sales lines, low-level codes %v; want %d, %d and five levels", len(items), sales, perLevel, n, 4*n)
	}
	// The result of one run holds about 2 GiB (2.2 million planned orders);
	// a soft limit keeps the test runner's footprint near the PRD's 8 GB
	// budget for the planner instead of letting the heap double between GCs.
	defer debug.SetMemoryLimit(debug.SetMemoryLimit(4 << 30))
	const budget = 10 * time.Minute
	var hashes []string
	for run := 0; run < 3; run++ {
		in := items
		if run == 1 {
			in = make([]Item, len(items))
			for i := range items {
				in[len(items)-1-i] = items[i]
			}
		}
		began := time.Now()
		r, err := Run(0, in)
		took := time.Since(began)
		if err != nil {
			t.Fatal(err)
		}
		if took >= budget {
			t.Fatalf("run %d took %s; want < %s", run+1, took, budget)
		}
		t.Logf("run %d: %s, %d planned orders, %d pegs, %d messages, hash %s", run+1, took.Round(time.Millisecond), len(r.Orders), len(r.Pegging), len(r.Messages), r.Hash[:16])
		hashes = append(hashes, r.Hash)
		if run == 0 {
			if len(r.Orders) == 0 {
				t.Fatal("the dataset planned no orders")
			}
			assertPeggingExplainsOrders(t, fmt.Sprintf("mrp-%d", n), r)
		}
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	t.Logf("memory obtained from the OS: %d MiB", mem.Sys>>20)
	if hashes[0] != hashes[1] || hashes[1] != hashes[2] {
		t.Fatalf("plan hashes differ across runs: %v", hashes)
	}
}
