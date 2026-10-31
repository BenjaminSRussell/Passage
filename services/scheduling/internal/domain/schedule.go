package domain

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Conflict codes (message catalog SCHED-0001, -0002, -0004, -0008).
const (
	CodeOverlap         = "SCHED-0001"
	CodeOutsideCapacity = "SCHED-0002"
	CodeNotCandidate    = "SCHED-0004"
	CodeFirmed          = "SCHED-0008"
)

// Resource is a work center or machine that can hold work. Infinite resources
// ignore booked time but still only run inside their open capacity.
//
// Blocks restrict parts of the open capacity to jobs whose attributes match
// (FR-23.17); their windows are UTC and should cover the planning horizon.
// Matrix prices changeovers between setup groups (FR-23.15); a matrix without
// entries leaves changeovers off on the resource. CurrentGroup is the setup
// group the resource is in before its first booked job of the horizon, empty
// when unknown (the first job then needs no changeover).
type Resource struct {
	ID           string
	Code         string
	Finite       bool
	Location     *time.Location
	Shifts       []Shift
	Blocks       []CapacityBlock
	Matrix       SetupMatrix
	CurrentGroup string
	// Overrides are downtimes and extra shifts layered over the calendar
	// (FR-23.1).
	Overrides []CapacityOverride
}

// Operation is one schedulable operation of an order. Candidates are resource
// ids in priority order (named resource first). DueAt zero means no due date.
//
// SetupGroup drives changeovers against the resource's setup matrix, and
// SetupMinutes is the route's static setup, used only when the matrix has no
// entry for the pair (SCHED-0012). Attributes are matched against capacity
// block conditions (BR-23.15). Network is the code of the resource network
// that governs the step from the order's previous operation into this one
// (FR-23.13); empty means the step is unconstrained.
type Operation struct {
	ID           string
	OrderID      string
	OperNumber   int
	Minutes      int
	DueAt        time.Time
	ReadyAt      time.Time
	Priority     int
	Candidates   []string
	SetupGroup   string
	SetupMinutes int
	Attributes   map[string]string
	Network      string
}

// Busy is booked time on a resource held by one job. Group and Setup are the
// job's setup group and route setup minutes; Changeover is the changeover the
// job carries at its start, set on its first segment only. Together they let
// the scheduler check that a job inserted in front still leaves the booked
// job enough changeover.
type Busy struct {
	ResourceID string
	Start      time.Time
	End        time.Time
	JobID      string
	Group      string
	Setup      int
	Changeover int
}

// Segment is one contiguous piece of a job on its resource.
type Segment struct {
	Start time.Time
	End   time.Time
}

// Placement is where one operation landed. When ChangeoverMinutes is set, the
// first that many minutes of the job are the changeover from the previous
// setup group (FR-23.15); the process minutes follow.
//
// ChangeoverCost and CleaningSetID come from the setup matrix entry that
// priced the changeover: its cost in the matrix currency and the cleaning
// standard operation set it requires (FR-23.15, recipe-dependent cleaning).
// ChangeoverCost is nil when no entry priced one (no change of setup group,
// or SCHED-0012 with the route setup used); CleaningSetID is empty when the
// entry names no set.
type Placement struct {
	OperationID       string
	OrderID           string
	OperNumber        int
	ResourceID        string
	Segments          []Segment
	Start             time.Time
	End               time.Time
	LateMinutes       int
	ChangeoverMinutes int
	ChangeoverCost    *money.Amount
	CleaningSetID     string
}

// withSlot copies the changeover a slot was priced with onto the placement.
func (p Placement) withSlot(s slot) Placement {
	p.ChangeoverMinutes = s.changeover
	p.ChangeoverCost, p.CleaningSetID = s.cost, s.cleaningSetID
	return p
}

// sameChangeover reports whether two placements carry the same changeover:
// minutes, cost and cleaning set.
func sameChangeover(a, b Placement) bool {
	if a.ChangeoverMinutes != b.ChangeoverMinutes || a.CleaningSetID != b.CleaningSetID || (a.ChangeoverCost == nil) != (b.ChangeoverCost == nil) {
		return false
	}
	return a.ChangeoverCost == nil || (a.ChangeoverCost.Currency == b.ChangeoverCost.Currency && a.ChangeoverCost.Value.Equal(b.ChangeoverCost.Value))
}

// Conflict explains why a placement is infeasible.
type Conflict struct {
	Code   string
	Detail string
	JobID  string
}

// Problem is the input to Schedule. Booked holds existing finite bookings that
// stay where they are (firmed jobs and jobs outside the selection). Fixed holds
// the placements of operations that keep their slot (firmed, or not selected)
// so their successors in the same order still respect precedence. Infinite
// ignores booked time on every resource (an infinite-capacity run): jobs still
// run only inside open capacity, but may overlap each other, and no
// changeovers are sequenced. Networks are the resource networks operations
// name by code; NetworkDelayDays bounds how far a predecessor may be delayed
// to meet a network's maximum wait (BR-23.11, default 7).
type Problem struct {
	Resources        []Resource
	Operations       []Operation
	Booked           []Busy
	Fixed            []Placement
	Networks         []ResourceNetwork
	Start            time.Time
	HorizonDays      int
	NetworkDelayDays int
	Infinite         bool
	// Backward schedules each order with a due date as late as it can run
	// and still end by that date (FR-23.2); BR-23.2 describes the fallback.
	Backward bool
	// Pegs link a parent order to the orders that consume its output; a
	// dependent starts no earlier than its parents end (FR-23.8). ParentEnds
	// holds the last end of each parent order that is not part of this run;
	// UnplacedParents names the parent orders outside the run that hold an
	// unplaced operation, whose dependents cannot be placed either.
	Pegs            []Peg
	ParentEnds      map[string]time.Time
	UnplacedParents map[string]bool
}

// Unplaced is an operation the heuristic could not place within the horizon.
type Unplaced struct {
	OperationID string
	Reason      string
}

// Warning is a finding about a placed operation that does not stop the run:
// SCHED-0010 (network wait over the maximum behind a frozen predecessor),
// SCHED-0012 (route setup used for a missing matrix entry) or SCHED-0014 (the
// job matches no block on its resource).
type Warning struct {
	Code        string
	OperationID string
	Detail      string
}

// Result is the heuristic output.
type Result struct {
	Placements []Placement
	Unplaced   []Unplaced
	Warnings   []Warning
}

type capacity struct {
	res  Resource
	iv   []Interval
	open int
}

func capacities(rs []Resource, from time.Time, days int) map[string]capacity {
	out := make(map[string]capacity, len(rs))
	for _, r := range rs {
		iv := r.Open(from, days)
		open := 0
		for _, i := range iv {
			open += i.Minutes()
		}
		out[r.ID] = capacity{res: r, iv: iv, open: open}
	}
	return out
}

func groupBusy(bs []Busy) map[string][]Busy {
	out := map[string][]Busy{}
	for _, b := range bs {
		out[b.ResourceID] = append(out[b.ResourceID], b)
	}
	for rid := range out {
		sortBusy(out[rid])
	}
	return out
}

func sortBusy(bs []Busy) {
	sort.Slice(bs, func(i, j int) bool { return bs[i].Start.Before(bs[j].Start) })
}

func insertBusy(bs []Busy, add ...Busy) []Busy {
	bs = append(bs, add...)
	sortBusy(bs)
	return bs
}

func removeJob(bs []Busy, jobID string) []Busy {
	return slices.DeleteFunc(bs, func(b Busy) bool { return b.JobID == jobID })
}

// busyOf books the segments of a placement. The changeover sits on the first
// segment only, so a check against it looks at the job's start.
func busyOf(op Operation, pl Placement) []Busy {
	out := make([]Busy, 0, len(pl.Segments))
	for i, s := range pl.Segments {
		b := Busy{ResourceID: pl.ResourceID, Start: s.Start, End: s.End, JobID: pl.OperationID, Group: op.SetupGroup, Setup: op.SetupMinutes}
		if i == 0 {
			b.Changeover = pl.ChangeoverMinutes
		}
		out = append(out, b)
	}
	return out
}

func ceilMinute(t time.Time) time.Time {
	if t.Equal(t.Truncate(time.Minute)) {
		return t
	}
	return t.Truncate(time.Minute).Add(time.Minute)
}

func appendSegment(segs []Segment, s Segment) []Segment {
	if n := len(segs); n > 0 && segs[n-1].End.Equal(s.Start) {
		segs[n-1].End = s.End
		return segs
	}
	return append(segs, s)
}

// fill lays `minutes` of work from t across open capacity. Closed time splits
// the job into segments (BR-23.3). Booked time on the resource is never crossed:
// a segment that would overlap booked work is a conflict, so the caller retries
// from the end of that booking. busy must be sorted by start and must not
// overlap itself (finite bookings, guaranteed by the exclusion constraint). With
// exact set, t itself must be open capacity.
func fill(iv []Interval, busy []Busy, t time.Time, minutes int, exact bool) ([]Segment, *Conflict) {
	t = ceilMinute(t)
	i := sort.Search(len(iv), func(k int) bool { return iv[k].End.After(t) })
	bi := sort.Search(len(busy), func(k int) bool { return busy[k].End.After(t) })
	var segs []Segment
	cursor, remaining := t, minutes
	for remaining > 0 {
		if i >= len(iv) {
			return nil, &Conflict{Code: CodeOutsideCapacity, Detail: "no open capacity left in the planning horizon"}
		}
		o := iv[i]
		if cursor.Before(o.Start) {
			if exact && len(segs) == 0 {
				return nil, &Conflict{Code: CodeOutsideCapacity, Detail: "start is outside open capacity"}
			}
			cursor = o.Start
		}
		take := o.End.Sub(cursor)
		if need := time.Duration(remaining) * time.Minute; take > need {
			take = need
		}
		for bi < len(busy) && !busy[bi].End.After(cursor) {
			bi++
		}
		// Only booked work inside the span this segment occupies conflicts; a
		// booking later in the same shift leaves the gap before it usable.
		if bi < len(busy) && busy[bi].Start.Before(cursor.Add(take)) {
			return nil, &Conflict{Code: CodeOverlap, Detail: "overlaps a booked job on the resource", JobID: busy[bi].JobID}
		}
		segs = appendSegment(segs, Segment{Start: cursor, End: cursor.Add(take)})
		cursor = cursor.Add(take)
		remaining -= int(take / time.Minute)
		if !cursor.Before(o.End) {
			i++
		}
	}
	return segs, nil
}

// startCandidates are the only earliest feasible starts at or after ready:
// ready itself and the ends of booked segments.
func startCandidates(busy []Busy, ready time.Time) []time.Time {
	cands := []time.Time{ready}
	for _, b := range busy {
		if b.End.After(ready) {
			cands = append(cands, b.End)
		}
	}
	return cands
}

func newPlacement(op Operation, resourceID string, segs []Segment) Placement {
	pl := Placement{OperationID: op.ID, OrderID: op.OrderID, OperNumber: op.OperNumber, ResourceID: resourceID,
		Segments: segs, Start: segs[0].Start, End: segs[len(segs)-1].End}
	if !op.DueAt.IsZero() && pl.End.After(op.DueAt) {
		pl.LateMinutes = minutesBetween(op.DueAt, pl.End)
	}
	return pl
}

// dueCompare orders due dates ascending with "no due date" last.
func dueCompare(a, b time.Time) int {
	switch {
	case a.IsZero() && b.IsZero():
		return 0
	case a.IsZero():
		return 1
	case b.IsZero():
		return -1
	case a.Before(b):
		return -1
	case a.After(b):
		return 1
	}
	return 0
}

// orderPlan sorts operations by order (delivery date, priority, order id) and
// then operation number, so each order's operations are contiguous and ascending.
func orderPlan(ops []Operation) []Operation {
	type key struct {
		due  time.Time
		prio int
	}
	keys := map[string]key{}
	for _, op := range ops {
		k, seen := keys[op.OrderID]
		if !seen || dueCompare(op.DueAt, k.due) < 0 {
			k.due = op.DueAt
		}
		if !seen || op.Priority < k.prio {
			k.prio = op.Priority
		}
		keys[op.OrderID] = k
	}
	out := slices.Clone(ops)
	slices.SortFunc(out, func(a, b Operation) int {
		ka, kb := keys[a.OrderID], keys[b.OrderID]
		if c := dueCompare(ka.due, kb.due); c != 0 {
			return c
		}
		if ka.prio != kb.prio {
			return ka.prio - kb.prio
		}
		if c := strings.Compare(a.OrderID, b.OrderID); c != 0 {
			return c
		}
		return a.OperNumber - b.OperNumber
	})
	return out
}

// Schedule runs the priority-list heuristic (FR-23.5): operations in order plan
// are placed at the earliest feasible interval on the best candidate, where best
// means least finish time, then lower load, then lower resource code (BR-23.8).
// An operation whose predecessor is unplaced is itself unplaced.
//
// On top of capacity and precedence the heuristic applies the slice 3
// constraints: a job runs only in open time its attributes may use under the
// resource's capacity blocks (FR-23.17, BR-23.15); on a resource with a setup
// matrix the changeover from the previous job's setup group is laid in front of
// the process minutes (FR-23.15); and an operation that names a resource
// network runs only on a resource with an edge from its predecessor's
// resource, within the edge's transfer time and maximum wait, delaying the
// predecessor when needed (FR-23.13, BR-23.11).
func Schedule(p Problem) Result {
	days := p.NetworkDelayDays
	if days <= 0 {
		days = DefaultNetworkDelayDays
	}
	e := &engine{caps: capacities(p.Resources, p.Start, p.HorizonDays), busy: groupBusy(p.Booked), booked: map[string]int{},
		nets: map[string]ResourceNetwork{}, infinite: p.Infinite, delay: time.Duration(days) * 24 * time.Hour, days: days}
	for _, b := range p.Booked {
		e.booked[b.ResourceID] += minutesBetween(b.Start, b.End)
	}
	for _, n := range p.Networks {
		e.nets[n.Code] = n
	}
	fixed := map[string][]Placement{}
	for _, f := range p.Fixed {
		fixed[f.OrderID] = append(fixed[f.OrderID], f)
	}
	var res Result
	ops := orderPlan(p.Operations)
	var chains [][]Operation
	for i := 0; i < len(ops); {
		j := i + 1
		for j < len(ops) && ops[j].OrderID == ops[i].OrderID {
			j++
		}
		chains = append(chains, ops[i:j])
		i = j
	}
	chains = pegPlan(chains, p.Pegs)
	ends := map[string]time.Time{}
	unplacedOrders := map[string]bool{}
	for oid, un := range p.UnplacedParents {
		unplacedOrders[oid] = un
	}
	for oid, fs := range fixed {
		for _, f := range fs {
			if f.End.After(ends[oid]) {
				ends[oid] = f.End
			}
		}
	}
	for _, chain := range chains {
		oid := chain[0].OrderID
		if len(p.Pegs) > 0 {
			floor, blocked := pegFloor(oid, p.Pegs, ends, p.ParentEnds, unplacedOrders)
			if blocked != "" {
				for _, op := range chain {
					res.Unplaced = append(res.Unplaced, Unplaced{op.ID, pegBlockedReason(blocked)})
				}
				unplacedOrders[oid] = true
				continue
			}
			if floor.After(chain[0].ReadyAt) {
				chain[0].ReadyAt = floor
			}
		}
		nPl, nUn := len(res.Placements), len(res.Unplaced)
		if p.Backward {
			e.placeBackward(chain, fixed[oid], p.Start, &res)
		} else {
			e.placeChain(chain, fixed[oid], p.Start, time.Time{}, &res)
		}
		for _, pl := range res.Placements[nPl:] {
			if pl.End.After(ends[oid]) {
				ends[oid] = pl.End
			}
		}
		if len(res.Unplaced) > nUn {
			unplacedOrders[oid] = true
		}
	}
	return res
}

// betterCandidate implements BR-23.8: earlier finish, then lower load ratio
// (booked/open, compared without floats), then lower resource code.
func betterCandidate(a Placement, aRID string, b Placement, bRID string, caps map[string]capacity, booked map[string]int) bool {
	if !a.End.Equal(b.End) {
		return a.End.Before(b.End)
	}
	ca, cb := caps[aRID], caps[bRID]
	la, lb := booked[aRID]*cb.open, booked[bRID]*ca.open
	if la != lb {
		return la < lb
	}
	return ca.res.Code < cb.res.Code
}

// MoveInput asks for one operation to be placed exactly on a resource at a
// start time, with its successors in the same order pushed forward as needed.
type MoveInput struct {
	Resources   []Resource
	Start       time.Time
	HorizonDays int
	Booked      []Busy
	Order       []Operation
	Current     map[string]Placement
	Moved       string
	ResourceID  string
	At          time.Time
	// Firmed lists operations whose placement is frozen (FR-23.4): the moved
	// operation must not be firmed, and a firmed successor or dependent
	// cannot be pushed.
	Firmed map[string]bool
	// Networks are the resource networks the operations name (FR-23.13).
	Networks []ResourceNetwork
	// Dependents are the operations of the orders pegged to the moved order,
	// directly or through other pegged orders (FR-23.8), and Pegs links them.
	// Current must hold their placements. Empty means the move does not
	// propagate beyond the moved order.
	Dependents []Operation
	Pegs       []Peg
}

// MovePlan is the proposed change set. Conflict is set when the move is
// infeasible; Changes lists every operation whose placement changes, the moved
// operation first, then its successors, then the dependents' operations.
// Dependents names the pegged orders that moved, in the order they were
// re-planned.
type MovePlan struct {
	Changes    []Placement
	Dependents []string
	Conflict   *Conflict
}

// PlanMove plans a move (FR-23.8). The moved operation starts no earlier than
// its predecessor's end. Successors keep their resource and move only forward.
// Moves keep the slice 3 constraints: the job must start in time its
// attributes may use under the resource's blocks; changeovers are repriced for
// every job placed, and every later job on a resource the move touches must
// still carry the changeover its new predecessor needs; and each network step
// the move changes must use an edge within its transfer time and maximum wait
// (SCHED-0009, SCHED-0010).
//
// With dependents, each pegged order whose first operation starts before one
// of its parents now ends is pushed forward to that end, with its own
// successors, on the same resources. Parents are re-planned before their
// dependents, so a dependent pegged to two orders waits for both. Dependents
// never move earlier, and any conflict refuses the whole move.
func PlanMove(in MoveInput) MovePlan {
	caps := capacities(in.Resources, in.Start, in.HorizonDays)
	busy := groupBusy(in.Booked)
	var mv *Operation
	for i := range in.Order {
		if in.Order[i].ID == in.Moved {
			mv = &in.Order[i]
		}
	}
	if mv == nil {
		return MovePlan{Conflict: &Conflict{Code: CodeNotCandidate, Detail: "operation is not part of its order"}}
	}
	if in.Firmed[mv.ID] {
		return MovePlan{Conflict: &Conflict{Code: CodeFirmed, Detail: "firmed job cannot be moved; unfirm it first", JobID: mv.ID}}
	}
	c, known := caps[in.ResourceID]
	if !known || !slices.Contains(mv.Candidates, in.ResourceID) {
		return MovePlan{Conflict: &Conflict{Code: CodeNotCandidate, Detail: "resource is not a candidate for the operation"}}
	}
	at := in.At
	for _, op := range in.Order {
		if op.OperNumber < mv.OperNumber {
			if pl, ok := in.Current[op.ID]; ok && pl.End.After(at) {
				at = pl.End
			}
		}
	}
	iv := c.usable(*mv)
	if t := ceilMinute(at); insideAny(c.iv, t) && !insideAny(iv, t) {
		return MovePlan{Conflict: &Conflict{Code: CodeOutsideCapacity, Detail: "start is inside a capacity block the job does not match (BR-23.15)"}}
	}
	touched := map[string]time.Time{}
	touch := func(rid string, t time.Time) {
		if cur, ok := touched[rid]; !ok || t.Before(cur) {
			touched[rid] = t
		}
	}
	old, wasPlaced := in.Current[mv.ID]
	if wasPlaced {
		busy[old.ResourceID] = removeJob(busy[old.ResourceID], mv.ID)
		touch(old.ResourceID, old.Start)
	}
	var bs []Busy
	if c.res.Finite {
		bs = busy[in.ResourceID]
	}
	s, conf := fillSlot(c, iv, *mv, bs, at, true, sequenced(c, *mv, false))
	if conf != nil {
		return MovePlan{Conflict: conf}
	}
	moved := newPlacement(*mv, in.ResourceID, s.segs).withSlot(s)
	if c.res.Finite {
		busy[in.ResourceID] = insertBusy(busy[in.ResourceID], busyOf(*mv, moved)...)
	}
	touch(in.ResourceID, moved.Start)

	// push re-places the placed operations of chain (sorted by operation
	// number) on their resources so the first starts no earlier than pred and
	// each later one no earlier than the one before it ends. Operations that
	// already start late enough and keep their changeover stay as they are.
	push := func(chain []Operation, pred time.Time, firmedDetail string) ([]Placement, *Conflict) {
		var out []Placement
		for _, op := range chain {
			cur, ok := in.Current[op.ID]
			if !ok {
				continue
			}
			ready := cur.Start
			if pred.After(ready) {
				ready = pred
			}
			if in.Firmed[op.ID] {
				if ready.After(cur.Start) {
					return nil, &Conflict{Code: CodeFirmed, Detail: firmedDetail, JobID: op.ID}
				}
				pred = cur.End
				continue
			}
			cc := caps[cur.ResourceID]
			busy[cur.ResourceID] = removeJob(busy[cur.ResourceID], op.ID)
			var sbs []Busy
			if cc.res.Finite {
				sbs = busy[cur.ResourceID]
			}
			ns, conf := placeOn(cc, op, sbs, ready, sequenced(cc, op, false))
			if conf != nil {
				return nil, conf
			}
			next := newPlacement(op, cur.ResourceID, ns.segs).withSlot(ns)
			if cc.res.Finite {
				busy[cur.ResourceID] = insertBusy(busy[cur.ResourceID], busyOf(op, next)...)
			}
			if !next.Start.Equal(cur.Start) || !next.End.Equal(cur.End) || !sameChangeover(next, cur) {
				out = append(out, next)
				touch(cur.ResourceID, cur.Start)
			}
			pred = next.End
		}
		return out, nil
	}

	successors := byOperNumber(slices.DeleteFunc(slices.Clone(in.Order), func(op Operation) bool { return op.OperNumber <= mv.OperNumber }))
	pushed, conf := push(successors, moved.End, "move would push a firmed successor; unfirm it first")
	if conf != nil {
		return MovePlan{Conflict: conf}
	}
	changes := append([]Placement{moved}, pushed...)
	if conf := checkNetworks(in.Networks, in.Order, in.Current, caps, changes); conf != nil {
		return MovePlan{Conflict: conf}
	}

	var dependents []string
	if len(in.Dependents) > 0 {
		ends := orderEnds(in.Current, changes)
		chains := map[string][]Operation{}
		for _, op := range in.Dependents {
			chains[op.OrderID] = append(chains[op.OrderID], op)
		}
		parents := map[string][]string{}
		for _, p := range in.Pegs {
			parents[p.DependentOrderID] = append(parents[p.DependentOrderID], p.ParentOrderID)
		}
		for _, oid := range PropagationOrder(mv.OrderID, in.Pegs) {
			chain := byOperNumber(chains[oid])
			if len(chain) == 0 {
				continue
			}
			var ready time.Time
			for _, p := range parents[oid] {
				if e, ok := ends[p]; ok && e.After(ready) {
					ready = e
				}
			}
			dep, conf := push(chain, ready, "a firmed job would have to move; unfirm it first")
			if conf != nil {
				conf.Detail = fmt.Sprintf("pegged order %s: %s", oid, conf.Detail)
				return MovePlan{Conflict: conf}
			}
			if len(dep) == 0 {
				continue
			}
			if conf := checkNetworks(in.Networks, chain, in.Current, caps, dep); conf != nil {
				conf.Detail = fmt.Sprintf("pegged order %s: %s", oid, conf.Detail)
				return MovePlan{Conflict: conf}
			}
			for _, pl := range dep {
				if pl.End.After(ends[oid]) {
					ends[oid] = pl.End
				}
			}
			changes = append(changes, dep...)
			dependents = append(dependents, oid)
		}
	}

	rids := make([]string, 0, len(touched))
	for rid := range touched {
		rids = append(rids, rid)
	}
	slices.Sort(rids)
	for _, rid := range rids {
		if rc, ok := caps[rid]; ok {
			if conf := checkSequence(rc, busy[rid], touched[rid]); conf != nil {
				return MovePlan{Conflict: conf}
			}
		}
	}
	return MovePlan{Changes: changes, Dependents: dependents}
}

// byOperNumber sorts operations by operation number in place and returns them.
func byOperNumber(ops []Operation) []Operation {
	slices.SortFunc(ops, func(a, b Operation) int { return a.OperNumber - b.OperNumber })
	return ops
}

// orderEnds is the latest end of each order's placements once changes apply.
func orderEnds(current map[string]Placement, changes []Placement) map[string]time.Time {
	merged := make(map[string]Placement, len(current))
	for id, pl := range current {
		merged[id] = pl
	}
	for _, c := range changes {
		merged[c.OperationID] = c
	}
	out := map[string]time.Time{}
	for _, pl := range merged {
		if pl.End.After(out[pl.OrderID]) {
			out[pl.OrderID] = pl.End
		}
	}
	return out
}

// insideAny reports whether t falls inside one of the sorted intervals.
func insideAny(iv []Interval, t time.Time) bool {
	i := sort.Search(len(iv), func(k int) bool { return iv[k].End.After(t) })
	return i < len(iv) && !iv[i].Start.After(t)
}

// checkNetworks verifies every network step of one order that the move
// changes: the step must use an edge of the operation's network and start
// within the edge's transfer time and maximum wait (BR-23.11).
func checkNetworks(networks []ResourceNetwork, order []Operation, current map[string]Placement, caps map[string]capacity, changes []Placement) *Conflict {
	nets := map[string]ResourceNetwork{}
	for _, n := range networks {
		nets[n.Code] = n
	}
	merged := make(map[string]Placement, len(current)+len(changes))
	for id, pl := range current {
		merged[id] = pl
	}
	changed := map[string]bool{}
	for _, c := range changes {
		merged[c.OperationID] = c
		changed[c.OperationID] = true
	}
	ops := byOperNumber(slices.Clone(order))
	var prev *Operation
	for i := range ops {
		op := &ops[i]
		cur, placed := merged[op.ID]
		if !placed {
			continue
		}
		if p := prev; p != nil && op.Network != "" && (changed[op.ID] || changed[p.ID]) {
			pp := merged[p.ID]
			n, ok := nets[op.Network]
			if !ok {
				return &Conflict{Code: CodeNetworkInfeasible, JobID: op.ID, Detail: fmt.Sprintf("network %s is not defined or not active", op.Network)}
			}
			edge, ok := n.Edge(pp.ResourceID, cur.ResourceID)
			if !ok {
				return &Conflict{Code: CodeNetworkInfeasible, JobID: op.ID,
					Detail: fmt.Sprintf("network %s has no transition from %s to %s", n.Code, caps[pp.ResourceID].res.Code, caps[cur.ResourceID].res.Code)}
			}
			if !edge.WaitOK(pp.End, cur.Start) {
				limit := "no maximum"
				if edge.MaxWaitMinutes != nil {
					limit = fmt.Sprintf("maximum %d min", *edge.MaxWaitMinutes)
				}
				return &Conflict{Code: CodeNetworkWait, JobID: op.ID,
					Detail: fmt.Sprintf("wait %d min breaks network %s limits (transfer %d min, %s)", minutesBetween(pp.End, cur.Start), n.Code, edge.TransferMinutes, limit)}
			}
		}
		prev = op
	}
	return nil
}

// Makespan is the span from the earliest start to the latest end.
func Makespan(ps []Placement) int {
	if len(ps) == 0 {
		return 0
	}
	first, last := ps[0].Start, ps[0].End
	for _, p := range ps[1:] {
		if p.Start.Before(first) {
			first = p.Start
		}
		if p.End.After(last) {
			last = p.End
		}
	}
	return minutesBetween(first, last)
}

// TotalLateness sums late minutes over placements.
func TotalLateness(ps []Placement) int {
	total := 0
	for _, p := range ps {
		total += p.LateMinutes
	}
	return total
}
