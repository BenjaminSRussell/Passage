package domain

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// PlanningMember is an entity of an IC planning group. Sequence is the group's
// explicit run position; zero means not set.
type PlanningMember struct {
	EntityID string
	Sequence int
}

// PlanningEdge says that Buyer buys from Supplier, with the IC transport lead time.
type PlanningEdge struct {
	Buyer        string
	Supplier     string
	LeadTimeDays int
}

// PlanRunOrder returns the entities in run order: buyers before suppliers
// (FR-40.15), each entity once (BR-40.13). Entities that are ready together run
// by explicit sequence, then by id.
//
// A cycle in the partner graph (a strongly connected group of entities that buy
// from each other) is broken by the group's explicit sequence: every entity on
// the cycle must carry one, and they run in sequence order. Edges into and out of
// the cycle are still respected. A cycle with an entity that has no sequence
// fails the run with IC-0816.
func PlanRunOrder(members []PlanningMember, edges []PlanningEdge) ([]string, error) {
	byID := make(map[string]PlanningMember, len(members))
	for _, m := range members {
		switch {
		case m.EntityID == "":
			return nil, invalid("planning member entity is required")
		case m.Sequence < 0:
			return nil, invalid("planning sequence must not be negative")
		}
		if _, dup := byID[m.EntityID]; dup {
			return nil, invalid(fmt.Sprintf("entity %s appears more than once in the planning group", m.EntityID))
		}
		byID[m.EntityID] = m
	}
	next := map[string][]string{}
	for _, e := range edges {
		if err := validEdge(byID, e); err != nil {
			return nil, err
		}
		next[e.Buyer] = append(next[e.Buyer], e.Supplier)
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		sort.Strings(next[id])
	}

	comps, compOf := stronglyConnected(ids, next)
	for _, c := range comps {
		if len(c) < 2 {
			continue
		}
		for _, id := range c {
			if byID[id].Sequence == 0 {
				return nil, &Error{Code: CodePlanningCycle, Detail: fmt.Sprintf("planning group cycle %v has no explicit sequence for %s", c, id)}
			}
		}
		sort.Slice(c, func(i, j int) bool { return before(byID[c[i]], byID[c[j]]) })
	}

	// Kahn's algorithm over the condensed graph; each component's members run
	// together in their sorted order.
	indeg := make([]int, len(comps))
	succ := make([]map[int]bool, len(comps))
	for i := range comps {
		succ[i] = map[int]bool{}
	}
	for _, id := range ids {
		for _, s := range next[id] {
			a, b := compOf[id], compOf[s]
			if a != b && !succ[a][b] {
				succ[a][b] = true
				indeg[b]++
			}
		}
	}
	order := make([]string, 0, len(ids))
	placed := make([]bool, len(comps))
	for range comps {
		pick := -1
		for i := range comps {
			if placed[i] || indeg[i] != 0 {
				continue
			}
			if pick < 0 || before(byID[comps[i][0]], byID[comps[pick][0]]) {
				pick = i
			}
		}
		placed[pick] = true
		order = append(order, comps[pick]...)
		for s := range succ[pick] {
			indeg[s]--
		}
	}
	return order, nil
}

func validEdge(byID map[string]PlanningMember, e PlanningEdge) error {
	if _, ok := byID[e.Buyer]; !ok {
		return invalid(fmt.Sprintf("buyer %s is not a planning member", e.Buyer))
	}
	if _, ok := byID[e.Supplier]; !ok {
		return invalid(fmt.Sprintf("supplier %s is not a planning member", e.Supplier))
	}
	if e.Buyer == e.Supplier {
		return invalid("an entity cannot buy from itself")
	}
	if e.LeadTimeDays < 0 {
		return invalid("lead time must not be negative")
	}
	return nil
}

// stronglyConnected returns the strongly connected components of the graph
// (Tarjan) and the component index of every id. ids and adjacency are sorted,
// so the result is deterministic.
func stronglyConnected(ids []string, next map[string][]string) ([][]string, map[string]int) {
	index := map[string]int{}
	low := map[string]int{}
	onStack := map[string]bool{}
	compOf := map[string]int{}
	var stack []string
	var comps [][]string
	counter := 0
	var visit func(v string)
	visit = func(v string) {
		index[v], low[v] = counter, counter
		counter++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range next[v] {
			if _, seen := index[w]; !seen {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			var c []string
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				compOf[w] = len(comps)
				c = append(c, w)
				if w == v {
					break
				}
			}
			sort.Strings(c)
			comps = append(comps, c)
		}
	}
	for _, id := range ids {
		if _, seen := index[id]; !seen {
			visit(id)
		}
	}
	return comps, compOf
}

// before orders candidates by explicit sequence (unset last), then by id.
func before(a, b PlanningMember) bool {
	ka, kb := seqKey(a), seqKey(b)
	if ka != kb {
		return ka < kb
	}
	return a.EntityID < b.EntityID
}

func seqKey(m PlanningMember) int {
	if m.Sequence == 0 {
		return math.MaxInt
	}
	return m.Sequence
}

// PlannedICOrder is a planned purchase order a buyer's MRP creates for a supplier.
type PlannedICOrder struct {
	Buyer         string
	Supplier      string
	Item          string
	Quantity      decimal.Decimal
	RequestedDate time.Time
}

// ICDemand is the demand the supplier entity sees from a planned IC order.
type ICDemand struct {
	Supplier string
	Item     string
	Quantity decimal.Decimal
	DueDate  time.Time
}

// ICDemandFor converts a planned IC order into supplier demand. The due date is
// the requested date minus the IC transport lead time of the buyer-supplier edge
// (FR-40.15). The AC-40.16 case: 2026-12-10 with 3 days gives 2026-12-07.
func ICDemandFor(order PlannedICOrder, edges []PlanningEdge) (ICDemand, error) {
	if !order.Quantity.IsPositive() {
		return ICDemand{}, invalid("planned IC quantity must be positive")
	}
	if order.RequestedDate.IsZero() {
		return ICDemand{}, invalid("planned IC order needs a requested date")
	}
	for _, e := range edges {
		if e.Buyer == order.Buyer && e.Supplier == order.Supplier {
			if e.LeadTimeDays < 0 {
				return ICDemand{}, invalid("lead time must not be negative")
			}
			return ICDemand{
				Supplier: order.Supplier,
				Item:     order.Item,
				Quantity: order.Quantity,
				DueDate:  order.RequestedDate.AddDate(0, 0, -e.LeadTimeDays),
			}, nil
		}
	}
	return ICDemand{}, invalid(fmt.Sprintf("no planning edge from %s to %s", order.Buyer, order.Supplier))
}

// StaleSuppliers returns the direct suppliers of a replanned buyer whose plan
// already ran (IC-0815): their IC demand came from the buyer's previous plan, so
// the group must be rerun. planned lists the entities planned so far, in any
// order; after a completed run it is the whole run order.
func StaleSuppliers(planned []string, edges []PlanningEdge, replanned string) []string {
	done := make(map[string]bool, len(planned))
	for _, id := range planned {
		done[id] = true
	}
	seen := map[string]bool{}
	var stale []string
	for _, e := range edges {
		if e.Buyer == replanned && done[e.Supplier] && !seen[e.Supplier] {
			seen[e.Supplier] = true
			stale = append(stale, e.Supplier)
		}
	}
	sort.Strings(stale)
	return stale
}
