package domain

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func members(ids ...string) []PlanningMember {
	out := make([]PlanningMember, 0, len(ids))
	for _, id := range ids {
		out = append(out, PlanningMember{EntityID: id})
	}
	return out
}

func TestPlanRunOrder(t *testing.T) {
	tests := []struct {
		name    string
		members []PlanningMember
		edges   []PlanningEdge
		want    []string
		code    string
	}{
		{
			name:    "buyers run before suppliers",
			members: members("C", "B", "A"),
			edges:   []PlanningEdge{{Buyer: "A", Supplier: "B"}, {Buyer: "B", Supplier: "C"}},
			want:    []string{"A", "B", "C"},
		},
		{
			name:    "independent entities are ordered by id",
			members: members("Z", "M", "A"),
			want:    []string{"A", "M", "Z"},
		},
		{
			name:    "explicit sequence breaks ties",
			members: []PlanningMember{{EntityID: "A", Sequence: 2}, {EntityID: "B", Sequence: 1}},
			want:    []string{"B", "A"},
		},
		{
			name:    "cycle without explicit sequence fails",
			members: members("A", "B"),
			edges:   []PlanningEdge{{Buyer: "A", Supplier: "B"}, {Buyer: "B", Supplier: "A"}},
			code:    CodePlanningCycle,
		},
		{
			name:    "cycle broken by explicit sequence",
			members: []PlanningMember{{EntityID: "A", Sequence: 2}, {EntityID: "B", Sequence: 1}},
			edges:   []PlanningEdge{{Buyer: "A", Supplier: "B"}, {Buyer: "B", Supplier: "A"}},
			want:    []string{"B", "A"},
		},
		{
			// Only the cycle needs a sequence: D buys into it and C supplies it,
			// and both keep their place around the cycle (the old sort failed here).
			name: "cycle with unsequenced buyer and supplier around it",
			members: []PlanningMember{
				{EntityID: "A", Sequence: 2}, {EntityID: "B", Sequence: 1}, {EntityID: "C"}, {EntityID: "D"},
			},
			edges: []PlanningEdge{
				{Buyer: "A", Supplier: "B"}, {Buyer: "B", Supplier: "A"},
				{Buyer: "B", Supplier: "C"}, {Buyer: "D", Supplier: "A"},
			},
			want: []string{"D", "B", "A", "C"},
		},
		{
			name: "a sequenced entity outside the cycle does not jump ahead of its buyer",
			members: []PlanningMember{
				{EntityID: "A", Sequence: 5}, {EntityID: "B", Sequence: 6}, {EntityID: "C", Sequence: 1},
			},
			edges: []PlanningEdge{{Buyer: "A", Supplier: "B"}, {Buyer: "B", Supplier: "A"}, {Buyer: "A", Supplier: "C"}},
			want:  []string{"A", "B", "C"},
		},
		{
			name:    "three-entity cycle with one unsequenced member fails",
			members: []PlanningMember{{EntityID: "A", Sequence: 1}, {EntityID: "B", Sequence: 2}, {EntityID: "C"}},
			edges:   []PlanningEdge{{Buyer: "A", Supplier: "B"}, {Buyer: "B", Supplier: "C"}, {Buyer: "C", Supplier: "A"}},
			code:    CodePlanningCycle,
		},
		{
			name:    "negative lead time is invalid",
			members: members("A", "B"),
			edges:   []PlanningEdge{{Buyer: "A", Supplier: "B", LeadTimeDays: -1}},
			code:    CodeInvalid,
		},
		{
			name:    "duplicate member is invalid",
			members: members("A", "A"),
			code:    CodeInvalid,
		},
		{
			name:    "edge to unknown entity is invalid",
			members: members("A"),
			edges:   []PlanningEdge{{Buyer: "A", Supplier: "X"}},
			code:    CodeInvalid,
		},
		{
			name:    "self purchase is invalid",
			members: members("A"),
			edges:   []PlanningEdge{{Buyer: "A", Supplier: "A"}},
			code:    CodeInvalid,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PlanRunOrder(tc.members, tc.edges)
			if tc.code != "" {
				de, ok := err.(*Error)
				if !ok || de.Code != tc.code {
					t.Fatalf("want code %s, got %v", tc.code, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("order = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPlanRunOrder_RespectsEveryEdge checks on random acyclic partner graphs that
// each entity appears once and every buyer precedes its suppliers.
func TestPlanRunOrder_RespectsEveryEdge(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(t, "entities")
		ms := make([]PlanningMember, n)
		for i := range ms {
			ms[i] = PlanningMember{EntityID: fmt.Sprintf("e%d", i)}
		}
		var edges []PlanningEdge
		for i := 0; i < n; i++ {
			for j := i + 1; j < n; j++ {
				if rapid.Bool().Draw(t, "edge") {
					// Edges only go from lower to higher index, so the graph is acyclic.
					edges = append(edges, PlanningEdge{Buyer: ms[i].EntityID, Supplier: ms[j].EntityID})
				}
			}
		}
		order, err := PlanRunOrder(ms, edges)
		if err != nil {
			t.Fatalf("acyclic graph failed: %v", err)
		}
		if len(order) != n {
			t.Fatalf("order has %d entries, want %d", len(order), n)
		}
		pos := map[string]int{}
		for i, id := range order {
			if _, dup := pos[id]; dup {
				t.Fatalf("entity %s planned twice", id)
			}
			pos[id] = i
		}
		for _, e := range edges {
			if pos[e.Buyer] >= pos[e.Supplier] {
				t.Fatalf("buyer %s planned after supplier %s", e.Buyer, e.Supplier)
			}
		}
	})
}

// TestPlanRunOrder_CyclesBrokenBySequence checks on random partner graphs, cycles
// included, where every entity carries a distinct explicit sequence: each entity
// appears once, every edge between entities not on one cycle keeps buyers first,
// and entities on one cycle run in sequence order.
func TestPlanRunOrder_CyclesBrokenBySequence(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(t, "entities")
		seqs := rapid.Permutation([]int{1, 2, 3, 4, 5, 6, 7, 8}).Draw(t, "sequences")
		ms := make([]PlanningMember, n)
		for i := range ms {
			ms[i] = PlanningMember{EntityID: fmt.Sprintf("e%d", i), Sequence: seqs[i]}
		}
		var edges []PlanningEdge
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				if i != j && rapid.IntRange(0, 3).Draw(t, "edge") == 0 {
					edges = append(edges, PlanningEdge{Buyer: ms[i].EntityID, Supplier: ms[j].EntityID})
				}
			}
		}
		order, err := PlanRunOrder(ms, edges)
		if err != nil {
			t.Fatalf("fully sequenced graph failed: %v", err)
		}
		pos := map[string]int{}
		for i, id := range order {
			if _, dup := pos[id]; dup {
				t.Fatalf("entity %s planned twice", id)
			}
			pos[id] = i
		}
		if len(pos) != n {
			t.Fatalf("order has %d entities, want %d", len(pos), n)
		}
		reach := func(from, to string) bool {
			seen := map[string]bool{from: true}
			queue := []string{from}
			for len(queue) > 0 {
				v := queue[0]
				queue = queue[1:]
				for _, e := range edges {
					if e.Buyer == v && !seen[e.Supplier] {
						if e.Supplier == to {
							return true
						}
						seen[e.Supplier] = true
						queue = append(queue, e.Supplier)
					}
				}
			}
			return false
		}
		seqOf := map[string]int{}
		for _, m := range ms {
			seqOf[m.EntityID] = m.Sequence
		}
		for _, e := range edges {
			onCycle := reach(e.Supplier, e.Buyer)
			switch {
			case !onCycle && pos[e.Buyer] >= pos[e.Supplier]:
				t.Fatalf("buyer %s planned after supplier %s", e.Buyer, e.Supplier)
			case onCycle && (seqOf[e.Buyer] < seqOf[e.Supplier]) != (pos[e.Buyer] < pos[e.Supplier]):
				t.Fatalf("cycle members %s and %s not in sequence order", e.Buyer, e.Supplier)
			}
		}
	})
}

// TestICDemandFor_LeadTime is AC-40.16: 100 units for 2026-12-10 with 3 days lead
// time become supplier demand on 2026-12-07.
func TestICDemandFor_LeadTime(t *testing.T) {
	edges := []PlanningEdge{{Buyer: "A", Supplier: "B", LeadTimeDays: 3}}
	order := PlannedICOrder{
		Buyer: "A", Supplier: "B", Item: "PUMP-1",
		Quantity:      decimal.NewFromInt(100),
		RequestedDate: time.Date(2026, 12, 10, 0, 0, 0, 0, time.UTC),
	}
	got, err := ICDemandFor(order, edges)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 12, 7, 0, 0, 0, 0, time.UTC); !got.DueDate.Equal(want) {
		t.Fatalf("due date = %s, want %s", got.DueDate, want)
	}
	if !got.Quantity.Equal(decimal.NewFromInt(100)) || got.Supplier != "B" {
		t.Fatalf("demand = %+v", got)
	}
	order.Quantity = decimal.Zero
	if _, err := ICDemandFor(order, edges); err == nil {
		t.Fatal("zero quantity must fail")
	}
	order.Quantity = decimal.NewFromInt(1)
	undated := order
	undated.RequestedDate = time.Time{}
	if _, err := ICDemandFor(undated, edges); err == nil {
		t.Fatal("an order without a requested date must fail")
	}
	order.Supplier = "C"
	if _, err := ICDemandFor(order, edges); err == nil {
		t.Fatal("missing edge must fail")
	}
}

func TestStaleSuppliers(t *testing.T) {
	edges := []PlanningEdge{
		{Buyer: "A", Supplier: "B"},
		{Buyer: "A", Supplier: "C"},
		{Buyer: "A", Supplier: "C"},
		{Buyer: "B", Supplier: "C"},
	}
	// After a completed run A, B, C, replanning buyer A makes both its suppliers stale (IC-0815), once each.
	if got := StaleSuppliers([]string{"A", "B", "C"}, edges, "A"); !reflect.DeepEqual(got, []string{"B", "C"}) {
		t.Fatalf("stale after full run = %v", got)
	}
	// Mid-run, only suppliers already planned are stale.
	if got := StaleSuppliers([]string{"A", "B"}, edges, "A"); !reflect.DeepEqual(got, []string{"B"}) {
		t.Fatalf("stale mid-run = %v", got)
	}
	// A supplier with no buyer replanned is never stale.
	if got := StaleSuppliers([]string{"A", "B", "C"}, edges, "C"); len(got) != 0 {
		t.Fatalf("stale for a pure supplier = %v", got)
	}
}
