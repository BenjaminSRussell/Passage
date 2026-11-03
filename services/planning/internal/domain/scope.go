package domain

import (
	"fmt"
	"slices"
	"strings"
)

// Limits of the planning horizon in days (PRD 4.22.13 "Planning horizon days").
const (
	MinHorizonDays = 30
	MaxHorizonDays = 1095
)

// ItemAttrs are the item master attributes a plan scope filters on (FR-22.13).
type ItemAttrs struct {
	Group   string
	Planner string
}

// Scope restricts which items a plan version reports (FR-22.13): by item id,
// item group, planner and low-level-code range. A criterion that is empty (or a
// nil bound) does not restrict; all given criteria must hold. The whole snapshot
// is still exploded and netted, so dependent demand from a parent outside the
// scope reaches a component inside it.
type Scope struct {
	ItemIDs, ItemGroups, Planners []string
	LLCMin, LLCMax                *int
}

// Empty reports whether the scope restricts nothing.
func (s Scope) Empty() bool {
	return len(s.ItemIDs) == 0 && len(s.ItemGroups) == 0 && len(s.Planners) == 0 && s.LLCMin == nil && s.LLCMax == nil
}

// Matches reports whether the item with the given attributes and low-level
// code is inside the scope.
func (s Scope) Matches(id string, a ItemAttrs, llc int) bool {
	switch {
	case len(s.ItemIDs) > 0 && !slices.ContainsFunc(s.ItemIDs, func(x string) bool { return strings.EqualFold(x, id) }):
		return false
	case len(s.ItemGroups) > 0 && !slices.Contains(s.ItemGroups, a.Group):
		return false
	case len(s.Planners) > 0 && !slices.Contains(s.Planners, a.Planner):
		return false
	case s.LLCMin != nil && llc < *s.LLCMin:
		return false
	case s.LLCMax != nil && llc > *s.LLCMax:
		return false
	}
	return true
}

// ClipHorizon returns items without the demand dated after lastDay (today is
// day 0, so a horizon of n days keeps demand up to day n): requirements past
// the planning horizon are ignored. A lastDay of 0 or less keeps everything. Supply is kept, and the input is not
// modified.
func ClipHorizon(items []Item, lastDay int) []Item {
	if lastDay <= 0 {
		return items
	}
	out := make([]Item, len(items))
	copy(out, items)
	for i := range out {
		kept := make([]Requirement, 0, len(out[i].Demand))
		for _, d := range out[i].Demand {
			if d.Date <= lastDay {
				kept = append(kept, d)
			}
		}
		out[i].Demand = kept
	}
	return out
}

// RunWithinHorizon plans the snapshot within the planning horizon (PRD 4.22.13,
// "requirements beyond are ignored"): demand dated beyond horizonDays from today
// is dropped before the run (ClipHorizon), and supply dated beyond it raises no
// Cancel or Decrease, because the demand it may serve there is not seen. It
// returns the result and the clipped snapshot. A horizon of 0 or less is
// RunWithCalendar over the whole snapshot.
func RunWithinHorizon(today int, cal *Calendar, items []Item, horizonDays int) (Result, []Item, error) {
	if horizonDays <= 0 {
		res, err := RunWithCalendar(today, cal, items)
		return res, items, err
	}
	clipped := ClipHorizon(items, today+horizonDays)
	res, err := RunWithCalendar(today, cal, clipped)
	if err != nil {
		return Result{}, nil, err
	}
	kept := make([]Message, 0, len(res.Messages))
	for _, m := range res.Messages {
		if (m.Action == ActionCancel || m.Action == ActionDecrease) && m.SuggestedDate > today+horizonDays {
			continue
		}
		kept = append(kept, m)
	}
	if len(kept) != len(res.Messages) {
		res.Messages = kept
		res.Hash = hashResult(res)
	}
	return res, clipped, nil
}

// LowLevelCodes returns the low-level code of every item of the snapshot
// (longest BOM path from a root); an unknown component is ErrInvalidItem and a
// cycle ErrBOMCycle.
func LowLevelCodes(items []Item) (map[string]int, error) {
	index := make(map[string]*Item, len(items))
	for i := range items {
		if _, dup := index[items[i].ID]; dup {
			return nil, fmt.Errorf("%w: duplicate id %q", ErrInvalidItem, items[i].ID)
		}
		index[items[i].ID] = &items[i]
	}
	return lowLevelCodes(items, index)
}

// ApplyScope returns the result restricted to the items inside the scope: their
// planned orders, pegs, messages and net requirements, with the hash recomputed
// over what remains, and the number of items inside. Items missing from attrs
// have no group and no planner.
func ApplyScope(res Result, items []Item, attrs map[string]ItemAttrs, s Scope) (Result, int, error) {
	if s.Empty() {
		return res, len(items), nil
	}
	// A warehouse scope is judged like its item: the low-level code is that of the
	// item in the BOM alone (the transfer link to the site item is not a BOM level).
	siteItems := make([]Item, 0, len(items))
	for _, it := range items {
		if _, wh := SplitWarehouseKey(it.ID); wh == "" {
			siteItems = append(siteItems, it)
		}
	}
	llc, err := LowLevelCodes(siteItems)
	if err != nil {
		return Result{}, 0, err
	}
	in := map[string]bool{}
	for _, it := range items {
		base, _ := SplitWarehouseKey(it.ID)
		if s.Matches(base, attrs[base], llc[base]) {
			in[it.ID] = true
		}
	}
	out := Result{}
	for _, o := range res.Orders {
		if in[o.ItemID] {
			out.Orders = append(out.Orders, o)
		}
	}
	for _, p := range res.Pegging {
		if in[p.ItemID] {
			out.Pegging = append(out.Pegging, p)
		}
	}
	for _, m := range res.Messages {
		if in[m.ItemID] {
			out.Messages = append(out.Messages, m)
		}
	}
	for _, n := range res.Net {
		if in[n.ItemID] {
			out.Net = append(out.Net, n)
		}
	}
	out.Hash = hashResult(out)
	return out, len(in), nil
}
