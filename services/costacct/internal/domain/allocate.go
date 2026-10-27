package domain

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

// ErrConservation is CO-0612: the step's cost in and cost out differ, so the run rolls back.
var ErrConservation = errors.New("allocation totals differ")

// Cycle is one reciprocal allocation step. Basis[j][i] is the (non-negative)
// statistical or fixed basis of receiver i for sender j; the diagonal is ignored.
type Cycle struct {
	Centers []string
	Costs   []decimal.Decimal
	Basis   [][]decimal.Decimal
}

// Flow is the cents-exact amount one sender posts to one receiver.
type Flow struct {
	From   string          `json:"from"`
	To     string          `json:"to"`
	Amount decimal.Decimal `json:"amount"`
}

// Posting is the net effect of the step on one center: received minus sent.
type Posting struct {
	CenterID string          `json:"centerId"`
	Amount   decimal.Decimal `json:"amount"`
}

// Result is the outcome of one allocation step. TotalIn is the cost the
// allocating senders bring into the step; TotalOut is the cost the final
// receivers (centers that send nothing) take out of it. After the residual
// sweep every sender ends at zero, so the two are equal to the cent.
type Result struct {
	Method      string          `json:"method"`
	Iterations  int             `json:"iterations"`
	Residual    decimal.Decimal `json:"residual"`
	Converged   bool            `json:"converged"`
	Swept       decimal.Decimal `json:"swept"`
	TotalIn     decimal.Decimal `json:"totalIn"`
	TotalOut    decimal.Decimal `json:"totalOut"`
	Flows       []Flow          `json:"flows"`
	Postings    []Posting       `json:"postings"`
	Unallocated []string        `json:"unallocated"`
}

// Allocate runs a reciprocal step and returns cents-exact flows (FR-38.7,
// BR-38.6, BR-38.8).
//
//   - Senders are centers with a positive basis to another center; a center
//     with no basis keeps its cost and is reported as unallocated.
//   - A closed cycle (a sender from which no final receiver is reachable) is
//     ErrSingular: its cost could never leave the step.
//   - Each sender's solved total is rounded to cents and split by share, with
//     the rounding on its largest receiver. Whatever a sender still holds
//     afterwards (cents from rounding, or the iteration residual when the
//     iterative path stopped at its limit) is swept to its largest receiver and
//     on until a final receiver takes it, so every sender ends at exactly zero.
func Allocate(c Cycle, opt SolveOptions) (Result, error) {
	n := len(c.Centers)
	if len(c.Costs) != n || len(c.Basis) != n {
		return Result{}, fmt.Errorf("cycle needs %d costs and basis rows, got %d and %d", n, len(c.Costs), len(c.Basis))
	}
	seen := make(map[string]bool, n)
	for _, id := range c.Centers {
		if seen[id] {
			return Result{}, fmt.Errorf("center %s listed twice", id)
		}
		seen[id] = true
	}
	shares := make([][]decimal.Decimal, n)
	sender := make([]bool, n)
	var unallocated []string
	for j := range c.Basis {
		if len(c.Basis[j]) != n {
			return Result{}, fmt.Errorf("basis row %d has %d columns for %d centers", j, len(c.Basis[j]), n)
		}
		total := decimal.Zero
		shares[j] = make([]decimal.Decimal, n)
		for i, b := range c.Basis[j] {
			if i == j {
				continue
			}
			if b.Sign() < 0 {
				return Result{}, fmt.Errorf("basis %s -> %s is negative", c.Centers[j], c.Centers[i])
			}
			total = total.Add(b)
		}
		if total.IsZero() {
			if !c.Costs[j].IsZero() {
				unallocated = append(unallocated, c.Centers[j])
			}
			continue
		}
		sender[j] = true
		for i, b := range c.Basis[j] {
			if i != j {
				shares[j][i] = b.Div(total).Round(solvePlaces)
			}
		}
	}
	routes, err := sweepRoutes(shares, sender)
	if err != nil {
		return Result{Unallocated: unallocated}, err
	}

	sol, err := SolveReciprocal(c.Costs, shares, opt)
	converged := err == nil
	if err != nil && !errors.Is(err, ErrNotConverged) {
		return Result{Method: sol.Method, Iterations: sol.Iterations, Residual: sol.Residual, Unallocated: unallocated}, err
	}

	// flowAt[j][i] indexes the flow from sender j to receiver i.
	flowAt := make([]map[int]int, n)
	var flows []Flow
	for j := 0; j < n; j++ {
		if !sender[j] {
			continue
		}
		flowAt[j] = map[int]int{}
		out := sol.Totals[j].Round(2)
		largest := largestReceiver(shares[j])
		remaining := out
		for i := 0; i < n; i++ {
			if i == largest || !shares[j][i].IsPositive() {
				continue
			}
			amt := out.Mul(shares[j][i]).Round(2)
			remaining = remaining.Sub(amt)
			flowAt[j][i] = len(flows)
			flows = append(flows, Flow{From: c.Centers[j], To: c.Centers[i], Amount: amt})
		}
		flowAt[j][largest] = len(flows)
		flows = append(flows, Flow{From: c.Centers[j], To: c.Centers[largest], Amount: remaining})
	}

	// Residual sweep: move what each sender still holds along its route.
	held := balances(c, flows, flowAt, n)
	swept := decimal.Zero
	for j := 0; j < n; j++ {
		if !sender[j] || held[j].IsZero() {
			continue
		}
		swept = swept.Add(held[j].Abs())
		from := j
		for _, to := range routes[j] {
			f := &flows[flowAt[from][to]]
			f.Amount = f.Amount.Add(held[j])
			from = to
		}
	}

	net := make([]decimal.Decimal, n)
	idx := make(map[string]int, n)
	for i, id := range c.Centers {
		idx[id] = i
	}
	for _, f := range flows {
		net[idx[f.To]] = net[idx[f.To]].Add(f.Amount)
		net[idx[f.From]] = net[idx[f.From]].Sub(f.Amount)
	}
	res := Result{Method: sol.Method, Iterations: sol.Iterations, Residual: sol.Residual, Converged: converged, Swept: swept,
		TotalIn: decimal.Zero, TotalOut: decimal.Zero, Flows: flows, Unallocated: unallocated}
	sum := decimal.Zero
	conserved := true
	for i, id := range c.Centers {
		res.Postings = append(res.Postings, Posting{CenterID: id, Amount: net[i]})
		sum = sum.Add(net[i])
		if sender[i] {
			res.TotalIn = res.TotalIn.Add(c.Costs[i])
			conserved = conserved && c.Costs[i].Add(net[i]).IsZero()
		} else {
			res.TotalOut = res.TotalOut.Add(net[i])
		}
	}
	if !conserved || !sum.IsZero() || !res.TotalIn.Equal(res.TotalOut) {
		return res, ErrConservation
	}
	return res, nil
}

// balances is what each center holds after the flows: its cost plus received minus sent.
func balances(c Cycle, flows []Flow, flowAt []map[int]int, n int) []decimal.Decimal {
	held := make([]decimal.Decimal, n)
	copy(held, c.Costs)
	for j := 0; j < n; j++ {
		for i, k := range flowAt[j] {
			held[i] = held[i].Add(flows[k].Amount)
			held[j] = held[j].Sub(flows[k].Amount)
		}
	}
	return held
}

func largestReceiver(row []decimal.Decimal) int {
	largest := -1
	for i := range row {
		if row[i].IsPositive() && (largest < 0 || row[i].GreaterThan(row[largest])) {
			largest = i
		}
	}
	return largest
}

// sweepRoutes gives every sender the path its residual travels to a final
// receiver: its largest receiver, then that one's largest receiver, until a
// center that sends nothing. When the largest-receiver chain loops back, the
// shortest path over any positive share is used instead. A sender with no path
// at all sits in a closed cycle (ErrSingular).
func sweepRoutes(shares [][]decimal.Decimal, sender []bool) ([][]int, error) {
	n := len(shares)
	routes := make([][]int, n)
	for j := 0; j < n; j++ {
		if !sender[j] {
			continue
		}
		visited := map[int]bool{j: true}
		var path []int
		at := j
		for sender[at] {
			next := largestReceiver(shares[at])
			if visited[next] {
				path = nil
				break
			}
			visited[next] = true
			path = append(path, next)
			at = next
		}
		if path == nil {
			path = shortestExit(shares, sender, j)
		}
		if path == nil {
			return nil, ErrSingular
		}
		routes[j] = path
	}
	return routes, nil
}

// shortestExit is a breadth-first search from start to the nearest center that
// sends nothing, over positive shares; nil when none is reachable.
func shortestExit(shares [][]decimal.Decimal, sender []bool, start int) []int {
	n := len(shares)
	prev := make([]int, n)
	for i := range prev {
		prev[i] = -2
	}
	prev[start] = -1
	queue := []int{start}
	for len(queue) > 0 {
		at := queue[0]
		queue = queue[1:]
		for i := 0; i < n; i++ {
			if prev[i] != -2 || !shares[at][i].IsPositive() {
				continue
			}
			prev[i] = at
			if !sender[i] {
				var path []int
				for k := i; k != start; k = prev[k] {
					path = append([]int{k}, path...)
				}
				return path
			}
			queue = append(queue, i)
		}
	}
	return nil
}
