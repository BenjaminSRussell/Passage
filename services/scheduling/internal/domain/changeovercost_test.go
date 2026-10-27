package domain

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/shopspring/decimal"
)

// costedMatrix is the allergen example of FR-23.15: PEANUT -> FREE is a 240
// min wet clean costing 180.50 with cleaning set WET-CLEAN; FREE -> PEANUT is
// a 20 min changeover costing 12.25 with no cleaning set.
func costedMatrix(t failer) SetupMatrix {
	m := mustMatrix(t,
		SetupEntry{FromGroup: "PEANUT", ToGroup: "FREE", Minutes: 240, Cost: decimal.RequireFromString("180.50"), CleaningSetID: "WET-CLEAN"},
		SetupEntry{FromGroup: "FREE", ToGroup: "PEANUT", Minutes: 20, Cost: decimal.RequireFromString("12.25")},
	)
	m.Currency = "EUR"
	return m
}

func wantCost(t *testing.T, p Placement, value, currency string) {
	t.Helper()
	if p.ChangeoverCost == nil {
		t.Fatalf("%s: no changeover cost, want %s %s", p.OperationID, value, currency)
	}
	if !p.ChangeoverCost.Value.Equal(decimal.RequireFromString(value)) || p.ChangeoverCost.Currency != currency {
		t.Fatalf("%s: changeover cost %s, want %s %s", p.OperationID, p.ChangeoverCost, value, currency)
	}
}

// FR-23.15: every changeover the heuristic lays in front of a job carries the
// cost and cleaning set of the matrix entry that priced it, in the matrix
// currency. A job that keeps the setup group carries neither.
func TestSchedule_ChangeoverCarriesMatrixCostAndCleaningSet(t *testing.T) {
	r0 := res("R", "R", true, allDays(0, 1440))
	r0.Matrix, r0.CurrentGroup = costedMatrix(t), "PEANUT"
	p := Problem{
		Resources: []Resource{r0},
		Operations: []Operation{
			{ID: "p1", OrderID: "o1", OperNumber: 10, Minutes: 60, DueAt: at(0, 2, 0), SetupGroup: "PEANUT", Candidates: []string{"R"}},
			{ID: "f1", OrderID: "o2", OperNumber: 10, Minutes: 60, DueAt: at(0, 9, 0), SetupGroup: "FREE", Candidates: []string{"R"}},
			{ID: "p2", OrderID: "o3", OperNumber: 10, Minutes: 60, DueAt: at(0, 12, 0), SetupGroup: "PEANUT", Candidates: []string{"R"}},
		},
		Start: monday, HorizonDays: 14,
	}
	r := Schedule(p)
	checkFeasible(t, p, r)
	same, wet, back := placementOf(t, r, "p1"), placementOf(t, r, "f1"), placementOf(t, r, "p2")
	if same.ChangeoverMinutes != 0 || same.ChangeoverCost != nil || same.CleaningSetID != "" {
		t.Fatalf("p1 = %+v, want no changeover, cost or cleaning set in the group the resource is in", same)
	}
	if wet.ChangeoverMinutes != 240 || wet.CleaningSetID != "WET-CLEAN" {
		t.Fatalf("f1 = %+v, want the 240 min wet clean with cleaning set WET-CLEAN", wet)
	}
	wantCost(t, wet, "180.50", "EUR")
	if back.ChangeoverMinutes != 20 || back.CleaningSetID != "" {
		t.Fatalf("p2 = %+v, want 20 min and no cleaning set", back)
	}
	wantCost(t, back, "12.25", "EUR")
}

// SCHED-0012: without a matrix entry the route setup is used and nothing
// priced it, so the job carries no cost (not a zero cost).
func TestSchedule_RouteSetupFallbackCarriesNoCost(t *testing.T) {
	r0 := res("R", "R", true, allDays(0, 1440))
	r0.Matrix, r0.CurrentGroup = costedMatrix(t), "PEANUT"
	r := Schedule(Problem{
		Resources: []Resource{r0},
		Operations: []Operation{
			{ID: "x", OrderID: "o1", OperNumber: 10, Minutes: 60, SetupGroup: "SESAME", SetupMinutes: 45, Candidates: []string{"R"}},
		},
		Start: monday, HorizonDays: 14,
	})
	x := placementOf(t, r, "x")
	if x.ChangeoverMinutes != 45 || x.ChangeoverCost != nil || x.CleaningSetID != "" {
		t.Fatalf("x = %+v, want the 45 min route setup with no cost or cleaning set", x)
	}
}

// An entry with zero cost still prices the changeover: the job carries a
// zero amount in the matrix currency, which is different from "not priced".
func TestSchedule_ZeroCostEntryIsStillPriced(t *testing.T) {
	r0 := res("R", "R", true, allDays(0, 1440))
	r0.Matrix = mustMatrix(t, SetupEntry{FromGroup: "A", ToGroup: "B", Minutes: 10})
	r0.Matrix.Currency, r0.CurrentGroup = "USD", "A"
	r := Schedule(Problem{
		Resources:  []Resource{r0},
		Operations: []Operation{{ID: "b", OrderID: "o1", OperNumber: 10, Minutes: 60, SetupGroup: "B", Candidates: []string{"R"}}},
		Start:      monday, HorizonDays: 14,
	})
	wantCost(t, placementOf(t, r, "b"), "0", "USD")
}

// A move reprices the moved job's changeover cost and cleaning set from its
// new neighbour: c after b is B -> C; moved to the front of the line it
// follows the resource's current group A and takes the A -> C entry instead.
func TestPlanMove_RepricesChangeoverCostAndCleaningSet(t *testing.T) {
	in, ops := sequencedLine(t)
	r := in.Resources[0]
	r.Matrix = mustMatrix(t,
		SetupEntry{FromGroup: "A", ToGroup: "B", Minutes: 30, Cost: decimal.RequireFromString("40")},
		SetupEntry{FromGroup: "A", ToGroup: "C", Minutes: 90, Cost: decimal.RequireFromString("300.125"), CleaningSetID: "CIP-FULL"},
		SetupEntry{FromGroup: "B", ToGroup: "C", Minutes: 20, Cost: decimal.RequireFromString("25")},
		SetupEntry{FromGroup: "C", ToGroup: "B", Minutes: 60, Cost: decimal.RequireFromString("70")},
	)
	r.Matrix.Currency = "GBP"
	in.Resources[0] = r
	in.Order, in.Moved, in.ResourceID, in.At = []Operation{ops["c"]}, "c", "R", at(0, 5, 0)
	plan := PlanMove(in)
	if plan.Conflict != nil || len(plan.Changes) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	got := plan.Changes[0]
	if got.ChangeoverMinutes != 90 || got.CleaningSetID != "CIP-FULL" {
		t.Fatalf("moved c = %+v, want the A -> C changeover of 90 min with cleaning set CIP-FULL", got)
	}
	wantCost(t, got, "300.125", "GBP")
}

// sameChangeover decides whether a pushed successor is rewritten: a change of
// cost, currency or cleaning set alone counts, even at the same minutes.
func TestSameChangeover(t *testing.T) {
	priced := func(v, cur, set string) Placement {
		return Placement{ChangeoverMinutes: 30, ChangeoverCost: &money.Amount{Value: decimal.RequireFromString(v), Currency: cur}, CleaningSetID: set}
	}
	cases := []struct {
		name string
		a, b Placement
		want bool
	}{
		{"both unpriced", Placement{ChangeoverMinutes: 30}, Placement{ChangeoverMinutes: 30}, true},
		{"equal decimals with different scale", priced("12.5", "EUR", ""), priced("12.500000", "EUR", ""), true},
		{"minutes differ", Placement{ChangeoverMinutes: 30}, Placement{ChangeoverMinutes: 20}, false},
		{"priced vs unpriced", priced("0", "EUR", ""), Placement{ChangeoverMinutes: 30}, false},
		{"cost differs", priced("12.5", "EUR", ""), priced("13", "EUR", ""), false},
		{"currency differs", priced("12.5", "EUR", ""), priced("12.5", "USD", ""), false},
		{"cleaning set differs", priced("12.5", "EUR", "WET"), priced("12.5", "EUR", ""), false},
	}
	for _, tc := range cases {
		if got := sameChangeover(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: sameChangeover = %v, want %v", tc.name, got, tc.want)
		}
	}
}
