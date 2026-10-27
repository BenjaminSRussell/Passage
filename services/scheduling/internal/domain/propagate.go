package domain

import "slices"

// CodePropagationTruncated is SCHED-0007: the dependent walk stopped at a
// BR-23.10 limit, so orders further down the pegging were not re-planned.
const CodePropagationTruncated = "SCHED-0007"

// BR-23.10 propagation limits (tenant parameter defaults, PRD 4.23.13).
const (
	DefaultPropagationOrders = 500
	DefaultPropagationLevels = 5
)

// Peg says the dependent order consumes the output of the parent order
// (FR-23.8): the dependent cannot start before the parent ends.
type Peg struct {
	ParentOrderID    string
	DependentOrderID string
}

// PropagationLimit bounds the dependent walk. Zero fields take the defaults.
type PropagationLimit struct {
	Orders int
	Levels int
}

func (l PropagationLimit) normalized() PropagationLimit {
	if l.Orders <= 0 {
		l.Orders = DefaultPropagationOrders
	}
	if l.Levels <= 0 {
		l.Levels = DefaultPropagationLevels
	}
	return l
}

func childrenOf(pegs []Peg) map[string][]string {
	out := map[string][]string{}
	for _, p := range pegs {
		out[p.ParentOrderID] = append(out[p.ParentOrderID], p.DependentOrderID)
	}
	for k := range out {
		slices.Sort(out[k])
	}
	return out
}

// DependentsOf walks the pegs breadth-first from root and returns the orders
// reached, nearest level first. truncated reports that an order was left out
// because the walk reached the order or level limit (BR-23.10, SCHED-0007).
func DependentsOf(root string, pegs []Peg, lim PropagationLimit) (orders []string, truncated bool) {
	lim = lim.normalized()
	children := childrenOf(pegs)
	type node struct {
		id    string
		level int
	}
	queue := []node{{root, 0}}
	seen := map[string]bool{root: true}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, c := range children[n.id] {
			if seen[c] {
				continue
			}
			if n.level >= lim.Levels || len(orders) >= lim.Orders {
				return orders, true
			}
			seen[c] = true
			orders = append(orders, c)
			queue = append(queue, node{c, n.level + 1})
		}
	}
	return orders, false
}

// WouldCycle reports whether pegging dependent to parent would close a loop:
// whether parent is the dependent itself or already depends on it.
func WouldCycle(pegs []Peg, parent, dependent string) bool {
	children := childrenOf(pegs)
	stack := []string{dependent}
	seen := map[string]bool{}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == parent {
			return true
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		stack = append(stack, children[n]...)
	}
	return false
}

// PropagationOrder lists the orders reachable from root through pegs in an
// order where every parent comes before its dependents (Kahn's algorithm,
// ties by order id), root excluded. Pegs must not form a cycle; orders on a
// cycle are left out.
func PropagationOrder(root string, pegs []Peg) []string {
	children := childrenOf(pegs)
	reach := map[string]bool{root: true}
	stack := []string{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, c := range children[n] {
			if !reach[c] {
				reach[c] = true
				stack = append(stack, c)
			}
		}
	}
	indegree := map[string]int{}
	for _, p := range pegs {
		if reach[p.ParentOrderID] && reach[p.DependentOrderID] {
			indegree[p.DependentOrderID]++
		}
	}
	ready := []string{root}
	var out []string
	for len(ready) > 0 {
		slices.Sort(ready)
		n := ready[0]
		ready = ready[1:]
		if n != root {
			out = append(out, n)
		}
		for _, c := range children[n] {
			if indegree[c]--; indegree[c] == 0 {
				ready = append(ready, c)
			}
		}
	}
	return out
}
