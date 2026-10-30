package domain

import (
	"fmt"
	"sort"
	"time"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// DefaultNetworkDelayDays is the BR-23.11 limit on delaying a predecessor to
// meet a network's maximum wait when the problem does not set one.
const DefaultNetworkDelayDays = 7

// networkStep is the smallest step a predecessor is delayed by per retry, so
// a chain that cannot meet its wait limit gives up in bounded time.
const networkStep = 5 * time.Minute

// usable is the capacity op may use on the resource: open time, less the
// blocks the job does not match (BR-23.15).
func (c capacity) usable(op Operation) []Interval {
	if len(c.res.Blocks) == 0 {
		return c.iv
	}
	return Placeable(c.iv, c.res.Blocks, op.Attributes)
}

// sequenced reports whether changeovers apply to op on the resource: a finite
// resource with a setup matrix in a finite run, and an operation with a setup
// group (FR-23.15).
func sequenced(c capacity, op Operation, infinite bool) bool {
	return c.res.Finite && !infinite && op.SetupGroup != "" && c.res.Matrix.Len() > 0
}

// groupBefore is the setup group the resource is in at t: the group of the
// last booked job with a setup group that ends at or before t, else the
// resource's current group. A job without a setup group leaves the setup as
// it was. bs is sorted and does not overlap itself, so ends are sorted too.
func groupBefore(c capacity, bs []Busy, t time.Time) string {
	for i := sort.Search(len(bs), func(k int) bool { return bs[k].End.After(t) }) - 1; i >= 0; i-- {
		if bs[i].Group != "" {
			return bs[i].Group
		}
	}
	return c.res.CurrentGroup
}

// changeoverNeed is the changeover a job of group `to` needs after group
// `from` (FR-23.15). The same group, an unknown previous group and a job
// without a group need none. Without a matrix entry the route setup time is
// used and missing is true (SCHED-0012).
func changeoverNeed(m SetupMatrix, from, to string, routeSetup int) (minutes int, missing bool) {
	if to == "" || m.Len() == 0 {
		return 0, false
	}
	e, ok := m.Lookup(from, to)
	if !ok {
		return routeSetup, true
	}
	return e.Minutes, false
}

// follower returns the first booked segment with a setup group that starts
// at or after t: the job whose changeover a job ending at t sets up.
func follower(bs []Busy, t time.Time) (Busy, bool) {
	for i := sort.Search(len(bs), func(k int) bool { return !bs[k].Start.Before(t) }); i < len(bs); i++ {
		if bs[i].Group != "" {
			return bs[i], true
		}
	}
	return Busy{}, false
}

// slot is a candidate placement of one job on one resource. cost and
// cleaningSetID come from the matrix entry that priced the changeover; cost is
// nil when none did.
type slot struct {
	segs          []Segment
	changeover    int
	from          string
	missing       bool
	cost          *money.Amount
	cleaningSetID string
}

// fillSlot lays op from t on a resource, with the changeover from the group
// the resource is in laid in front of the process minutes when seq is set. The
// changeover is never split across closed time (BR-23.3): when it does not fit
// before the open interval closes, the job moves to the next open interval
// (an exact slot is refused instead). A slot is also refused when the booked
// job after it would be left too short a changeover (its stored changeover no
// longer covers the step from op's group).
func fillSlot(c capacity, iv []Interval, op Operation, bs []Busy, t time.Time, exact, seq bool) (slot, *Conflict) {
	if !seq {
		segs, conf := fill(iv, bs, t, op.Minutes, exact)
		return slot{segs: segs}, conf
	}
	var segs []Segment
	var from string
	var co int
	var missing bool
	start := ceilMinute(t)
	for {
		from = groupBefore(c, bs, start)
		co, missing = changeoverNeed(c.res.Matrix, from, op.SetupGroup, op.SetupMinutes)
		var conf *Conflict
		if segs, conf = fill(iv, bs, start, op.Minutes+co, exact); conf != nil {
			return slot{}, conf
		}
		if s := segs[0].Start; s.After(start) && groupBefore(c, bs, s) != from {
			// The job begins later than start (closed or blocked time in
			// between), so another booked job now comes before it: reprice.
			start = s
			continue
		}
		if co > 0 && minutesBetween(segs[0].Start, segs[0].End) < co {
			if exact {
				return slot{}, &Conflict{Code: CodeOutsideCapacity,
					Detail: fmt.Sprintf("the %d min changeover does not fit before the open interval closes and is never split (BR-23.3)", co)}
			}
			next, ok := nextOpen(iv, segs[0].End)
			if !ok {
				return slot{}, &Conflict{Code: CodeOutsideCapacity, Detail: "no open interval left in the planning horizon holds the changeover"}
			}
			start = next
			continue
		}
		break
	}
	if next, ok := follower(bs, segs[len(segs)-1].End); ok && op.SetupGroup != "" {
		if need, _ := changeoverNeed(c.res.Matrix, op.SetupGroup, next.Group, next.Setup); need > next.Changeover {
			return slot{}, &Conflict{Code: CodeOverlap, JobID: next.JobID,
				Detail: fmt.Sprintf("the job after this slot would need a %d min changeover but has %d min", need, next.Changeover)}
		}
	}
	out := slot{segs: segs, changeover: co, from: from, missing: missing}
	if e, ok := c.res.Matrix.Priced(from, op.SetupGroup); ok {
		out.cost = &money.Amount{Value: e.Cost, Currency: c.res.Matrix.Currency}
		out.cleaningSetID = e.CleaningSetID
	}
	return out, nil
}

// nextOpen is the start of the first open interval that begins at or after t.
func nextOpen(iv []Interval, t time.Time) (time.Time, bool) {
	i := sort.Search(len(iv), func(k int) bool { return !iv[k].Start.Before(t) })
	if i == len(iv) {
		return time.Time{}, false
	}
	return iv[i].Start, true
}

// placeOn finds the earliest slot for op on one resource at or after ready.
// Only ready and the ends of booked segments can be earliest feasible starts.
func placeOn(c capacity, op Operation, bs []Busy, ready time.Time, seq bool) (slot, *Conflict) {
	iv := c.usable(op)
	var last *Conflict
	for _, t := range startCandidates(bs, ready) {
		s, conf := fillSlot(c, iv, op, bs, t, false, seq)
		if conf == nil {
			return s, nil
		}
		last = conf
	}
	return slot{}, last
}

// checkSequence verifies that every booked job on a sequenced resource that
// starts at or after from still carries the changeover its predecessor needs.
func checkSequence(c capacity, bs []Busy, from time.Time) *Conflict {
	if !c.res.Finite || c.res.Matrix.Len() == 0 {
		return nil
	}
	group := c.res.CurrentGroup
	for _, b := range bs {
		if !b.Start.Before(from) {
			if need, _ := changeoverNeed(c.res.Matrix, group, b.Group, b.Setup); need > b.Changeover {
				return &Conflict{Code: CodeOverlap, JobID: b.JobID,
					Detail: fmt.Sprintf("job %s would need a %d min changeover from setup group %s but has %d min", b.JobID, need, group, b.Changeover)}
			}
		}
		if b.Group != "" {
			group = b.Group
		}
	}
	return nil
}

func latest(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.After(out) {
			out = t
		}
	}
	return out
}

func minutes(n int) time.Duration { return time.Duration(n) * time.Minute }

func spanMinutes(segs []Segment) int {
	total := 0
	for _, s := range segs {
		total += minutesBetween(s.Start, s.End)
	}
	return total
}

// engine holds the mutable state of one Schedule run.
type engine struct {
	caps     map[string]capacity
	busy     map[string][]Busy
	booked   map[string]int
	nets     map[string]ResourceNetwork
	infinite bool
	delay    time.Duration
	days     int
}

func (e *engine) holds(rid string) bool { return e.caps[rid].res.Finite && !e.infinite }

func (e *engine) busyOn(rid string) []Busy {
	if e.holds(rid) {
		return e.busy[rid]
	}
	return nil
}

func (e *engine) book(op Operation, pl Placement) {
	if e.holds(pl.ResourceID) {
		e.busy[pl.ResourceID] = insertBusy(e.busy[pl.ResourceID], busyOf(op, pl)...)
	}
	e.booked[pl.ResourceID] += spanMinutes(pl.Segments)
}

func (e *engine) unbook(pl Placement) {
	if e.holds(pl.ResourceID) {
		e.busy[pl.ResourceID] = removeJob(e.busy[pl.ResourceID], pl.OperationID)
	}
	e.booked[pl.ResourceID] -= spanMinutes(pl.Segments)
}

// stop says why an attempt to place an order's operations ended early. A
// network stop with retry asks for the operation before idx to start no
// earlier than until and the chain to be tried again (BR-23.11); without retry
// it flags the order NETWORK_INFEASIBLE.
type stop struct {
	idx     int
	reason  string
	network bool
	retry   bool
	until   time.Time
}

// placeChain places the operations of one order. While a network's maximum
// wait cannot be met, the predecessor is delayed and the chain tried again,
// up to the delay limit from the predecessor's first start; past it the whole
// order is unplaced as NETWORK_INFEASIBLE (SCHED-0009). A non-zero seed is the
// earliest start of the first operation (backward scheduling, BR-23.2).
func (e *engine) placeChain(chain []Operation, fixed []Placement, start, seed time.Time, res *Result) {
	notBefore := make([]time.Time, len(chain))
	notBefore[0] = seed
	first := make([]time.Time, len(chain))
	for {
		pls, ws, st := e.attempt(chain, fixed, start, notBefore)
		for k, pl := range pls {
			if first[k].IsZero() {
				first[k] = pl.Start
			}
		}
		if st != nil && st.network {
			for _, pl := range pls {
				e.unbook(pl)
			}
			if st.retry && !st.until.After(first[st.idx-1].Add(e.delay)) {
				notBefore[st.idx-1] = st.until
				continue
			}
			failed := chain[st.idx]
			reason := st.reason
			if st.retry {
				reason = fmt.Sprintf("%s %s: no allowed successor resource in network %s for operation %s within %d days",
					CodeNetworkInfeasible, FlagNetworkInfeasible, failed.Network, failed.ID, e.days)
			}
			for k, op := range chain {
				why := reason
				if k != st.idx {
					why = fmt.Sprintf("%s %s: operation %s of the order has no allowed successor resource in network %s",
						CodeNetworkInfeasible, FlagNetworkInfeasible, failed.ID, failed.Network)
				}
				res.Unplaced = append(res.Unplaced, Unplaced{op.ID, why})
			}
			return
		}
		res.Placements = append(res.Placements, pls...)
		res.Warnings = append(res.Warnings, ws...)
		if st == nil {
			return
		}
		for k := st.idx; k < len(chain); k++ {
			reason := "predecessor operation is unplaced"
			if k == st.idx {
				reason = st.reason
			}
			res.Unplaced = append(res.Unplaced, Unplaced{chain[k].ID, reason})
		}
		return
	}
}

// attempt places a chain in operation order, booking each placement as it
// goes so the caller can unbook a failed attempt. Each operation starts after
// every fixed predecessor and the previous placed one, at or after its ready
// time, the run start and its notBefore.
func (e *engine) attempt(chain []Operation, fixed []Placement, start time.Time, notBefore []time.Time) ([]Placement, []Warning, *stop) {
	var pls []Placement
	var ws []Warning
	for k, op := range chain {
		if op.Minutes < 1 {
			return pls, ws, &stop{idx: k, reason: "duration must be at least one minute"}
		}
		ready := latest(start, op.ReadyAt, notBefore[k])
		var prev *Placement
		frozen := false
		for i := range fixed {
			f := fixed[i]
			if f.OperNumber >= op.OperNumber {
				continue
			}
			ready = latest(ready, f.End)
			if prev == nil || f.OperNumber > prev.OperNumber {
				prev, frozen = &fixed[i], true
			}
		}
		if k > 0 {
			p := &pls[k-1]
			ready = latest(ready, p.End)
			if prev == nil || p.OperNumber > prev.OperNumber {
				prev, frozen = p, false
			}
		}
		var net *ResourceNetwork
		if op.Network != "" && prev != nil {
			n, ok := e.nets[op.Network]
			if !ok {
				return pls, ws, &stop{idx: k,
					reason: fmt.Sprintf("%s: network %s is not defined or not active on the run date", CodeNetworkInfeasible, op.Network)}
			}
			net = &n
		}
		pl, w, st := e.placeOp(op, ready, prev, frozen, net)
		if st != nil {
			st.idx = k
			return pls, ws, st
		}
		e.book(op, pl)
		pls = append(pls, pl)
		ws = append(ws, w...)
	}
	return pls, ws, nil
}

// placeOp chooses the best candidate for one operation (BR-23.8). With a
// network, only resources with an edge from the predecessor's resource are
// candidates, each no earlier than the transfer time, and each must start
// within the edge's maximum wait (BR-23.11). When no candidate meets the wait,
// the stop asks for the predecessor to be delayed by the smallest overrun; a
// frozen predecessor cannot move, so the best edge candidate is taken with a
// SCHED-0010 warning instead.
func (e *engine) placeOp(op Operation, ready time.Time, prev *Placement, frozen bool, net *ResourceNetwork) (Placement, []Warning, *stop) {
	var best, over Placement
	var bestSlot, overSlot slot
	found, overFound, sawEdge := false, false, false
	var overEdge NetworkEdge
	shortest := time.Duration(-1)
	for _, rid := range op.Candidates {
		c, ok := e.caps[rid]
		if !ok {
			continue
		}
		r := ready
		var edge NetworkEdge
		if net != nil {
			if edge, ok = net.Edge(prev.ResourceID, rid); !ok {
				continue
			}
			sawEdge = true
			r = latest(r, prev.End.Add(minutes(edge.TransferMinutes)))
		}
		s, conf := placeOn(c, op, e.busyOn(rid), r, sequenced(c, op, e.infinite))
		if conf != nil {
			continue
		}
		cand := newPlacement(op, rid, s.segs).withSlot(s)
		if net != nil && !edge.WaitOK(prev.End, cand.Start) {
			if late := cand.Start.Sub(prev.End) - minutes(*edge.MaxWaitMinutes); shortest < 0 || late < shortest {
				shortest = late
			}
			if !overFound || betterCandidate(cand, rid, over, over.ResourceID, e.caps, e.booked) {
				over, overSlot, overEdge, overFound = cand, s, edge, true
			}
			continue
		}
		if !found || betterCandidate(cand, rid, best, best.ResourceID, e.caps, e.booked) {
			best, bestSlot, found = cand, s, true
		}
	}
	var ws []Warning
	if !found {
		switch {
		case net != nil && !sawEdge:
			return Placement{}, nil, &stop{network: true,
				reason: fmt.Sprintf("%s %s: no allowed successor resource in network %s for operation %s from resource %s",
					CodeNetworkInfeasible, FlagNetworkInfeasible, net.Code, op.ID, prev.ResourceID)}
		case overFound && frozen:
			best, bestSlot = over, overSlot
			ws = append(ws, Warning{Code: CodeNetworkWait, OperationID: op.ID,
				Detail: fmt.Sprintf("wait %d min exceeds network %s maximum %d min; the predecessor is frozen, move it later",
					minutesBetween(prev.End, over.Start), net.Code, *overEdge.MaxWaitMinutes)})
		case overFound:
			step := shortest
			if step < networkStep {
				step = networkStep
			}
			// prev is the operation before this one in the chain: a fixed
			// predecessor is frozen and took the case above.
			return Placement{}, nil, &stop{network: true, retry: true, until: prev.Start.Add(step)}
		default:
			return Placement{}, nil, &stop{reason: "no candidate resource has open capacity in the horizon"}
		}
	}
	if bestSlot.missing {
		ws = append(ws, Warning{Code: CodeMissingSetupEntry, OperationID: op.ID,
			Detail: fmt.Sprintf("setup matrix has no entry %s -> %s; route setup time %d min used", bestSlot.from, op.SetupGroup, op.SetupMinutes)})
	}
	if c := e.caps[best.ResourceID]; len(c.res.Blocks) > 0 && !MatchesAny(c.res.Blocks, op.Attributes) {
		ws = append(ws, Warning{Code: CodeNoMatchingBlock, OperationID: op.ID,
			Detail: fmt.Sprintf("job %s does not match any block on %s; placed in unblocked capacity", op.ID, c.res.Code)})
	}
	return best, ws, nil
}
