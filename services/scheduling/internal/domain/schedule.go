package domain

import (
	"slices"
	"sort"
	"strings"
	"time"
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
type Resource struct {
	ID       string
	Code     string
	Finite   bool
	Location *time.Location
	Shifts   []Shift
}

// Operation is one schedulable operation of an order. Candidates are resource
// ids in priority order (named resource first). DueAt zero means no due date.
type Operation struct {
	ID         string
	OrderID    string
	OperNumber int
	Minutes    int
	DueAt      time.Time
	ReadyAt    time.Time
	Priority   int
	Candidates []string
}

// Busy is booked time on a resource held by one job.
type Busy struct {
	ResourceID string
	Start      time.Time
	End        time.Time
	JobID      string
}

// Segment is one contiguous piece of a job on its resource.
type Segment struct {
	Start time.Time
	End   time.Time
}

// Placement is where one operation landed.
type Placement struct {
	OperationID string
	OrderID     string
	OperNumber  int
	ResourceID  string
	Segments    []Segment
	Start       time.Time
	End         time.Time
	LateMinutes int
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
// run only inside open capacity, but may overlap each other.
type Problem struct {
	Resources   []Resource
	Operations  []Operation
	Booked      []Busy
	Fixed       []Placement
	Start       time.Time
	HorizonDays int
	Infinite    bool
}

// Unplaced is an operation the heuristic could not place within the horizon.
type Unplaced struct {
	OperationID string
	Reason      string
}

// Result is the heuristic output.
type Result struct {
	Placements []Placement
	Unplaced   []Unplaced
}

type capacity struct {
	res  Resource
	iv   []Interval
	open int
}

func capacities(rs []Resource, from time.Time, days int) map[string]capacity {
	out := make(map[string]capacity, len(rs))
	for _, r := range rs {
		iv := OpenIntervals(r.Location, r.Shifts, from, days)
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

func busyOf(resourceID, jobID string, segs []Segment) []Busy {
	out := make([]Busy, 0, len(segs))
	for _, s := range segs {
		out = append(out, Busy{ResourceID: resourceID, Start: s.Start, End: s.End, JobID: jobID})
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

// place finds the earliest start at or after ready where the work fits. Only
// ready and the ends of booked segments can be earliest feasible starts.
func place(iv []Interval, busy []Busy, ready time.Time, minutes int) ([]Segment, *Conflict) {
	cands := []time.Time{ready}
	for _, b := range busy {
		if b.End.After(ready) {
			cands = append(cands, b.End)
		}
	}
	var last *Conflict
	for _, c := range cands {
		segs, conf := fill(iv, busy, c, minutes, false)
		if conf == nil {
			return segs, nil
		}
		last = conf
	}
	return nil, last
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
func Schedule(p Problem) Result {
	caps := capacities(p.Resources, p.Start, p.HorizonDays)
	busy := groupBusy(p.Booked)
	booked := map[string]int{}
	for _, b := range p.Booked {
		booked[b.ResourceID] += minutesBetween(b.Start, b.End)
	}
	predEnd := map[string]time.Time{}
	blocked := map[string]bool{}
	fixed := map[string][]Placement{}
	for _, f := range p.Fixed {
		fixed[f.OrderID] = append(fixed[f.OrderID], f)
	}
	var res Result
	for _, op := range orderPlan(p.Operations) {
		switch {
		case blocked[op.OrderID]:
			res.Unplaced = append(res.Unplaced, Unplaced{op.ID, "predecessor operation is unplaced"})
			continue
		case op.Minutes < 1:
			res.Unplaced = append(res.Unplaced, Unplaced{op.ID, "duration must be at least one minute"})
			blocked[op.OrderID] = true
			continue
		}
		ready := p.Start
		if op.ReadyAt.After(ready) {
			ready = op.ReadyAt
		}
		if e, ok := predEnd[op.OrderID]; ok && e.After(ready) {
			ready = e
		}
		for _, f := range fixed[op.OrderID] {
			if f.OperNumber < op.OperNumber && f.End.After(ready) {
				ready = f.End
			}
		}
		var best Placement
		var bestRID string
		found := false
		for _, rid := range op.Candidates {
			c, ok := caps[rid]
			if !ok {
				continue
			}
			var bs []Busy
			if c.res.Finite && !p.Infinite {
				bs = busy[rid]
			}
			segs, conf := place(c.iv, bs, ready, op.Minutes)
			if conf != nil {
				continue
			}
			cand := newPlacement(op, rid, segs)
			if !found || betterCandidate(cand, rid, best, bestRID, caps, booked) {
				best, bestRID, found = cand, rid, true
			}
		}
		if !found {
			res.Unplaced = append(res.Unplaced, Unplaced{op.ID, "no candidate resource has open capacity in the horizon"})
			blocked[op.OrderID] = true
			continue
		}
		if caps[bestRID].res.Finite && !p.Infinite {
			busy[bestRID] = insertBusy(busy[bestRID], busyOf(bestRID, op.ID, best.Segments)...)
		}
		booked[bestRID] += op.Minutes
		predEnd[op.OrderID] = best.End
		res.Placements = append(res.Placements, best)
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
	// operation must not be firmed, and a firmed successor cannot be pushed.
	Firmed map[string]bool
}

// MovePlan is the proposed change set. Conflict is set when the move is
// infeasible; Changes lists every operation whose placement changes, the moved
// operation first.
type MovePlan struct {
	Changes  []Placement
	Conflict *Conflict
}

// PlanMove plans a move (FR-23.8). The moved operation starts no earlier than
// its predecessor's end. Successors keep their resource and move only forward.
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
	old := in.Current[mv.ID]
	busy[old.ResourceID] = removeJob(busy[old.ResourceID], mv.ID)
	var bs []Busy
	if c.res.Finite {
		bs = busy[in.ResourceID]
	}
	segs, conf := fill(c.iv, bs, at, mv.Minutes, true)
	if conf != nil {
		return MovePlan{Conflict: conf}
	}
	if c.res.Finite {
		busy[in.ResourceID] = insertBusy(busy[in.ResourceID], busyOf(in.ResourceID, mv.ID, segs)...)
	}
	changes := []Placement{newPlacement(*mv, in.ResourceID, segs)}
	pred := changes[0].End
	for _, op := range in.Order {
		if op.OperNumber <= mv.OperNumber {
			continue
		}
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
				return MovePlan{Conflict: &Conflict{Code: CodeFirmed, Detail: "move would push a firmed successor; unfirm it first", JobID: op.ID}}
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
		next, conf := place(cc.iv, sbs, ready, op.Minutes)
		if conf != nil {
			return MovePlan{Conflict: conf}
		}
		if cc.res.Finite {
			busy[cur.ResourceID] = insertBusy(busy[cur.ResourceID], busyOf(cur.ResourceID, op.ID, next)...)
		}
		if !next[0].Start.Equal(cur.Start) {
			changes = append(changes, newPlacement(op, cur.ResourceID, next))
		}
		pred = next[len(next)-1].End
	}
	return MovePlan{Changes: changes}
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
