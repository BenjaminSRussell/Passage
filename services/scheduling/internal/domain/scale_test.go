package domain

import (
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// randomProblem builds a plant of resources with full-day calendars and
// operations that each have a few candidate resources.
func randomProblem(seed int64, resources, operations, orders, horizonDays int) Problem {
	rng := rand.New(rand.NewSource(seed))
	rs := make([]Resource, 0, resources)
	for i := 0; i < resources; i++ {
		rs = append(rs, res(fmt.Sprintf("r%04d", i), fmt.Sprintf("R%04d", i), true, allDays(0, 1440)))
	}
	ops := make([]Operation, 0, operations)
	for i := 0; i < operations; i++ {
		cands := make([]string, 0, 3)
		seen := map[int]bool{}
		for len(cands) < 3 {
			j := rng.Intn(resources)
			if !seen[j] {
				seen[j] = true
				cands = append(cands, rs[j].ID)
			}
		}
		ops = append(ops, Operation{
			ID:         fmt.Sprintf("op%06d", i),
			OrderID:    fmt.Sprintf("o%05d", i%orders),
			OperNumber: 10 * (1 + i/orders),
			Minutes:    30 + rng.Intn(240),
			DueAt:      monday.AddDate(0, 0, 1+rng.Intn(horizonDays)),
			Candidates: cands,
		})
	}
	return Problem{Resources: rs, Operations: ops, Start: monday, HorizonDays: horizonDays}
}

// TestSchedule_NoDoubleBookingAt10kJobs covers AC2: 10k scheduled jobs on
// finite resources never overlap and each lies in open capacity.
func TestSchedule_NoDoubleBookingAt10kJobs(t *testing.T) {
	p := randomProblem(42, 50, 10000, 2000, 120)
	r := Schedule(p)
	checkFeasible(t, p, r)
	if len(r.Placements) < 9000 {
		t.Fatalf("only %d of 10000 operations placed", len(r.Placements))
	}
}

// TestSchedule_FiveThousandOperationsUnderBudget covers AC4 (5k operations in
// under 60 s). The bound is generous so CI machines do not flake.
func TestSchedule_FiveThousandOperationsUnderBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("performance check skipped in -short mode")
	}
	p := randomProblem(7, 200, 5000, 1000, 180)
	began := time.Now()
	r := Schedule(p)
	elapsed := time.Since(began)
	if elapsed > 60*time.Second {
		t.Fatalf("5000 operations took %s, budget is 60s", elapsed)
	}
	checkFeasible(t, p, r)
	if len(r.Placements) == 0 {
		t.Fatal("no operations placed")
	}
}
