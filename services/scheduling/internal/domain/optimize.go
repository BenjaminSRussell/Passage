package domain

import "fmt"

// Solver statuses. OPTIMAL and FEASIBLE certify a solver order (BR-23.9);
// the others keep the nearest-neighbour sequence. Unavailable and TooLarge
// are set by the scheduling service when scheduler-opt was not asked or did
// not answer.
const (
	SolverOptimal     = "OPTIMAL"
	SolverFeasible    = "FEASIBLE"
	SolverUnknown     = "UNKNOWN"
	SolverUnavailable = "UNAVAILABLE"
	SolverTooLarge    = "TOO_LARGE"
)

// CodeOptimizationStatus is SCHED-0006: the optimizer ended and the
// schedule kept is reported.
const CodeOptimizationStatus = "SCHED-0006"

// MaxSolverCampaignOrders is the largest campaign scheduler-opt sequences
// with AddCircuit (FR-23.14).
const MaxSolverCampaignOrders = 50

// MaxSolverChangeoverMinutes is the largest matrix entry scheduler-opt
// accepts; it matches the service's one-week cap on setup minutes.
const MaxSolverChangeoverMinutes = 10080

// SolverProposal is scheduler-opt's answer for a campaign: the order ids in
// run order and the solver status.
type SolverProposal struct {
	OrderIDs []string
	Status   string
}

// ChangeoverMatrix builds the matrix scheduler-opt's campaign endpoint
// expects. Row and column 0 are the resource's current setup group; index
// i+1 is orders[i]. Entry [a][b] prices the step from a to b exactly as the
// sequencers do: zero within one setup group or from an unknown current
// group, the matrix entry otherwise, and the order's route setup when the
// matrix has no entry (SCHED-0012). Column 0 is zero: nothing returns to the
// current state.
func ChangeoverMatrix(m SetupMatrix, current string, orders []SetupOrder) [][]int {
	groups := make([]string, 0, len(orders)+1)
	groups = append(groups, current)
	for _, o := range orders {
		groups = append(groups, o.SetupGroup)
	}
	out := make([][]int, len(groups))
	for a := range groups {
		out[a] = make([]int, len(groups))
		for b := 1; b < len(groups); b++ {
			if a == b || groups[a] == groups[b] || groups[a] == "" {
				continue
			}
			e, _ := changeover(m, groups[a], orders[b-1])
			out[a][b] = e.Minutes
		}
	}
	return out
}

// SolverMessage is the SCHED-0006 text for a cp-sat plan.
func SolverMessage(status string, optimized bool) string {
	kept := "heuristic"
	if optimized {
		kept = "optimized"
	}
	return fmt.Sprintf("Optimization ended with status %s; %s schedule kept.", status, kept)
}

// chooseSequence applies BR-23.9 to a campaign: the solver's order replaces
// the nearest-neighbour sequence only when the solver certified it (OPTIMAL
// or FEASIBLE), it names every order exactly once, and, priced with this
// matrix, it is strictly cheaper. The matrix may have changed since the
// solver was asked, so its own minutes are never trusted.
func chooseSequence(m SetupMatrix, current string, orders []SetupOrder, p *SolverProposal) (Sequence, string, bool) {
	heuristic := SequenceNearest(m, current, orders)
	if p == nil {
		return heuristic, SolverUnavailable, false
	}
	if p.Status != SolverOptimal && p.Status != SolverFeasible {
		return heuristic, p.Status, false
	}
	ordered, ok := orderedBy(orders, p.OrderIDs)
	if !ok {
		return heuristic, p.Status, false
	}
	candidate := SequenceAsGiven(m, current, ordered)
	if candidate.TotalMinutes < heuristic.TotalMinutes {
		return candidate, p.Status, true
	}
	return heuristic, p.Status, false
}

// orderedBy returns orders in the order of ids, which must name every order
// exactly once.
func orderedBy(orders []SetupOrder, ids []string) ([]SetupOrder, bool) {
	if len(ids) != len(orders) {
		return nil, false
	}
	byID := make(map[string]SetupOrder, len(orders))
	for _, o := range orders {
		byID[o.OrderID] = o
	}
	out := make([]SetupOrder, 0, len(ids))
	for _, id := range ids {
		o, ok := byID[id]
		if !ok {
			return nil, false
		}
		delete(byID, id)
		out = append(out, o)
	}
	return out, true
}
