package domain

import (
	"fmt"
	"slices"
	"sort"
	"time"
)

// CodeCampaignWindow is SCHED-0149: no stretch of the planning horizon holds
// the whole campaign on its resource without another job in the way.
const CodeCampaignWindow = "SCHED-0149"

// CodeCampaignPredecessor is SCHED-0153: an earlier operation of a campaign
// order is not on the schedule, so nothing says when the campaign job may
// start without running ahead of it.
const CodeCampaignPredecessor = "SCHED-0153"

// maxPlaceTries bounds how often a campaign start moves behind a foreign
// booking before the placement gives up.
const maxPlaceTries = 200

// CampaignPlaceInput is what PlaceCampaign needs. Resources holds every
// resource (the campaign's and the secondaries of its operations) with the
// matrix, blocks and overrides in force; the campaign resource's CurrentGroup
// is the setup the resource is in when the campaign begins. Booked is every
// finite booking except the campaign's own. Operations are the campaign's
// orders in sequence, each with its campaign setup group, run minutes and route
// setup. Cleanouts are the plan's, each after one of those orders.
//
// Chains hold every operation of each campaign order as stored (queue times,
// overlap, network), keyed by order id; Current the placements of those on
// the schedule apart from the campaign's own; Firmed the ones a firmed job
// holds; Networks the resource networks they name. With them the campaign job
// keeps the order's precedence (FR-23.3, BR-23.5, FR-23.13): it starts no
// earlier than its scheduled predecessors allow, and the order's later
// operations move forward behind it.
type CampaignPlaceInput struct {
	ResourceID  string
	Resources   []Resource
	Booked      []Busy
	Start       time.Time
	HorizonDays int
	Operations  []Operation
	Cleanouts   []PlannedCleanout
	Chains      map[string][]Operation
	Current     map[string]Placement
	Firmed      map[string]bool
	Networks    []ResourceNetwork
}

// PlacedCleanout is a planned cleanout with the closed window it holds on the
// resource. The window lies in open time in one piece (BR-23.13).
type PlacedCleanout struct {
	PlannedCleanout
	Start time.Time
	End   time.Time
}

// CampaignPlacement is a campaign laid on its resource: one placement per
// order in sequence order (changeovers inside them, the first is the leading
// setup), the cleanout windows, and the span they cover.
//
// Pushed are the later operations of the campaign orders that had to move
// forward behind their campaign job, on the resources they were on.
type CampaignPlacement struct {
	Placements []Placement
	Cleanouts  []PlacedCleanout
	Warnings   []Warning
	Pushed     []Placement
	Start      time.Time
	End        time.Time
}

// PlaceCampaign lays a sequenced campaign on its resource contiguously
// (FR-23.14): the orders in sequence, each straight after the one before it
// apart from closed time, with the changeover of the setup matrix in front of
// each and the plan's cleanouts, never split, after the orders they follow.
// When another booking would lie between two of the campaign's jobs, the start
// moves behind that booking and the campaign is laid again; the campaign never
// interleaves with other work. The first conflict that cannot be worked
// around (no open capacity left in the horizon, a secondary resource that is
// never free) comes back as the error.
//
// The order's own route holds as well: each campaign job starts no earlier
// than the release of its scheduled predecessors plus the queue between them
// (an earlier operation that is not scheduled refuses the placement with
// SCHED-0153), and the order's later operations are pushed forward on their
// resources behind it, as a move pushes them (a firmed one that would have to
// move refuses with SCHED-0138; a network step it breaks with SCHED-0009 or
// SCHED-0010).
func PlaceCampaign(in CampaignPlaceInput) (CampaignPlacement, *Conflict) {
	if len(in.Operations) == 0 {
		return CampaignPlacement{}, &Conflict{Code: CodeCampaignWindow, Detail: "the campaign has no orders to place"}
	}
	eff := 0
	for _, r := range in.Resources {
		if r.ID == in.ResourceID {
			eff = r.EfficiencyPct
		}
	}
	in.Operations = slices.Clone(in.Operations)
	later := map[string]bool{}
	for i, op := range in.Operations {
		floor, conf := predecessorFloor(in.Chains[op.OrderID], op, in.Current, eff)
		if conf != nil {
			return CampaignPlacement{}, conf
		}
		if floor.After(op.ReadyAt) {
			in.Operations[i].ReadyAt = floor
		}
		for _, o := range in.Chains[op.OrderID] {
			if o.OperNumber > op.OperNumber && !in.Firmed[o.ID] {
				later[o.ID] = true
			}
		}
	}
	// The order's later operations that may move are re-laid behind the
	// campaign, so they do not hold it back as other work would.
	in.Booked = slices.DeleteFunc(slices.Clone(in.Booked), func(b Busy) bool { return later[b.JobID] })
	start := ceilMinute(in.Start)
	for try := 0; try < maxPlaceTries; try++ {
		out, e, blocker, conf := placeCampaignFrom(in, start)
		if conf != nil {
			return CampaignPlacement{}, conf
		}
		if blocker == nil {
			if conf := pushLater(in, e, &out); conf != nil {
				return CampaignPlacement{}, conf
			}
			return out, nil
		}
		start = ceilMinute(blocker.End)
	}
	return CampaignPlacement{}, &Conflict{Code: CodeCampaignWindow,
		Detail: "no stretch of the planning horizon holds the whole campaign without other jobs in between"}
}

// placeCampaignFrom lays the campaign from start. A non-nil blocker is the
// foreign booking that would sit between two of the campaign's jobs.
func placeCampaignFrom(in CampaignPlaceInput, start time.Time) (CampaignPlacement, *engine, *Busy, *Conflict) {
	e := &engine{caps: capacities(in.Resources, start, in.HorizonDays), busy: groupBusy(append([]Busy(nil), in.Booked...)),
		booked: map[string]int{}, nets: map[string]ResourceNetwork{}, delay: DefaultNetworkDelayDays * 24 * time.Hour, days: DefaultNetworkDelayDays}
	c, ok := e.caps[in.ResourceID]
	if !ok {
		return CampaignPlacement{}, nil, nil, &Conflict{Code: CodeNotCandidate, Detail: "the campaign resource is unknown"}
	}
	if !c.res.Finite {
		return CampaignPlacement{}, nil, nil, &Conflict{Code: CodeNotCandidate, Detail: "a campaign runs on a finite-capacity resource"}
	}
	after := map[string][]PlannedCleanout{}
	for _, cl := range in.Cleanouts {
		after[cl.AfterOrderID] = append(after[cl.AfterOrderID], cl)
	}
	foreign := foreignBusy(in.Booked, in.ResourceID)
	out := CampaignPlacement{Placements: make([]Placement, 0, len(in.Operations)), Cleanouts: []PlacedCleanout{}}
	cursor := start
	for _, op := range in.Operations {
		op.Candidates = []string{in.ResourceID}
		if op.ReadyAt.After(cursor) {
			// An order that is not ready yet holds the campaign back: it
			// could not be contiguous with the order before it otherwise.
			cursor = op.ReadyAt
		}
		pl, ws, stop := e.placeOp(op, cursor, cursor, nil, false, nil)
		if stop != nil {
			return CampaignPlacement{}, nil, nil, &Conflict{Code: CodeOutsideCapacity, Detail: fmt.Sprintf("order %s cannot be placed: %s", op.OrderID, stop.reason), JobID: op.ID}
		}
		if b := between(foreign, cursor, pl.Start); b != nil {
			return CampaignPlacement{}, nil, b, nil
		}
		e.book(op, pl)
		out.Placements = append(out.Placements, pl)
		out.Warnings = append(out.Warnings, ws...)
		cursor = pl.End
		for _, cl := range after[op.OrderID] {
			w, b, conf := placeCleanout(e, c, foreign, in.ResourceID, cl, cursor)
			if conf != nil || b != nil {
				return CampaignPlacement{}, nil, b, conf
			}
			out.Cleanouts = append(out.Cleanouts, w)
			cursor = w.End
		}
	}
	out.Start, out.End = out.Placements[0].Start, cursor
	return out, e, nil, nil
}

// foreignBusy is the primary bookings of other jobs on the resource, sorted by
// start: the work a campaign must not be interleaved with.
func foreignBusy(booked []Busy, rid string) []Busy {
	var out []Busy
	for _, b := range booked {
		if b.ResourceID == rid {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// between returns a booking that lies wholly or partly inside [from, to), the
// stretch the campaign has to cross to reach its next job.
func between(bs []Busy, from, to time.Time) *Busy {
	for i := range bs {
		if bs[i].End.After(from) && bs[i].Start.Before(to) {
			return &bs[i]
		}
	}
	return nil
}

// placeCleanout finds the cleanout's window: the earliest stretch at or after t
// in open time that holds all its minutes in one piece (BR-23.13 never splits a
// cleanout), booked with the campaign so the next order starts behind it.
func placeCleanout(e *engine, c capacity, foreign []Busy, rid string, cl PlannedCleanout, t time.Time) (PlacedCleanout, *Busy, *Conflict) {
	out := PlacedCleanout{PlannedCleanout: cl}
	if cl.Minutes <= 0 {
		out.Start, out.End = t, t
		return out, nil, nil
	}
	from := t
	for {
		segs, conf := fill(c.iv, e.busyOn(rid), from, cl.Minutes, false)
		if conf != nil {
			for i := range foreign {
				if conf.Code == CodeOverlap && foreign[i].JobID == conf.JobID {
					return out, &foreign[i], nil
				}
			}
			return out, nil, &Conflict{Code: conf.Code, JobID: conf.JobID,
				Detail: fmt.Sprintf("the %d min cleanout after order %s does not fit in one piece: %s", cl.Minutes, cl.AfterOrderID, conf.Detail)}
		}
		if len(segs) > 1 {
			next, ok := nextOpen(c.iv, segs[0].End)
			if !ok {
				return out, nil, &Conflict{Code: CodeOutsideCapacity, Detail: fmt.Sprintf("no open interval left in the planning horizon holds the %d min cleanout after order %s", cl.Minutes, cl.AfterOrderID)}
			}
			from = next
			continue
		}
		out.Start, out.End = segs[0].Start, segs[0].End
		break
	}
	if b := between(foreign, t, out.End); b != nil {
		return out, b, nil
	}
	e.busy[rid] = insertBusy(e.busy[rid], Busy{ResourceID: rid, Start: out.Start, End: out.End, JobID: "cleanout:" + cl.AfterOrderID})
	return out, nil, nil
}

// predecessorFloor is the earliest start the order's earlier operations allow
// the campaign operation op, as for a moved job: the release of each of them,
// plus the queue behind the one right before it, and with an overlap no
// earlier than that one's end plus the queue less op's own process time
// (FR-23.3, BR-23.5). An earlier operation that is not scheduled refuses the
// placement (SCHED-0153): the campaign job could run ahead of it.
func predecessorFloor(chain []Operation, op Operation, current map[string]Placement, efficiencyPct int) (time.Time, *Conflict) {
	var floor time.Time
	var before *Operation
	for i, p := range chain {
		if p.OperNumber >= op.OperNumber {
			continue
		}
		pl, ok := current[p.ID]
		if !ok {
			return time.Time{}, &Conflict{Code: CodeCampaignPredecessor, JobID: p.ID, Detail: fmt.Sprintf(
				"operation %d of order %s comes before the campaign operation %d and is not scheduled; schedule it before you place the campaign",
				p.OperNumber, op.OrderID, op.OperNumber)}
		}
		floor = latest(floor, pl.Release())
		if before == nil || p.OperNumber > before.OperNumber {
			before = &chain[i]
		}
	}
	if before != nil {
		pl := current[before.ID]
		gap := queueGap(*before, op)
		floor = latest(floor, pl.Release().Add(gap))
		if pl.OverlapBP > 0 {
			floor = latest(floor, pl.End.Add(gap-minutes(BookedMinutes(op.Minutes, efficiencyPct))))
		}
	}
	return floor, nil
}

// pushLater moves the later operations of each campaign order forward behind
// its campaign job on the resources they hold, then checks the network steps
// the placement changed and the changeovers of every job behind a moved one.
func pushLater(in CampaignPlaceInput, e *engine, out *CampaignPlacement) *Conflict {
	touched := map[string]time.Time{}
	pusher := chainPusher{caps: e.caps, busy: e.busy, current: in.Current, firmed: in.Firmed, touch: func(rid string, t time.Time) {
		if cur, ok := touched[rid]; !ok || t.Before(cur) {
			touched[rid] = t
		}
	}}
	for i, pl := range out.Placements {
		op := in.Operations[i]
		chain := in.Chains[op.OrderID]
		var later []Operation
		for _, o := range chain {
			if o.OperNumber > op.OperNumber {
				later = append(later, o)
			}
		}
		pushed, conf := pusher.push(byOperNumber(later), pl.Release(), pl.End, &op,
			fmt.Sprintf("order %s: a later operation held by a firmed job would have to move behind the campaign; unfirm it first", op.OrderID))
		if conf != nil {
			return conf
		}
		if conf := checkNetworks(in.Networks, chain, in.Current, e.caps, append([]Placement{pl}, pushed...)); conf != nil {
			conf.Detail = fmt.Sprintf("order %s: %s", op.OrderID, conf.Detail)
			return conf
		}
		out.Pushed = append(out.Pushed, pushed...)
	}
	rids := make([]string, 0, len(touched))
	for rid := range touched {
		rids = append(rids, rid)
	}
	slices.Sort(rids)
	for _, rid := range rids {
		if conf := checkSequence(e.caps[rid], e.busy[rid], touched[rid]); conf != nil {
			return conf
		}
	}
	return nil
}
