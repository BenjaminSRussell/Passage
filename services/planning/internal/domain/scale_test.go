package domain

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"testing"
	"time"
)

// EnvScaleItems sets the size of the scale run. Unset, a 5,000-item snapshot
// of the same shape keeps determinism covered in every test run; the issue's
// scale (AC2, AC-22.3) is PASSAGE_PLANNING_SCALE_ITEMS=50000, which peaks near
// 5.5 GB RSS and takes about 40 s, so it is opt-in.
const EnvScaleItems = "PASSAGE_PLANNING_SCALE_ITEMS"

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
	items := ScaleSnapshot(n)
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
