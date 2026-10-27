package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// CodeMissingSetupEntry is SCHED-0012: no matrix entry exists, so the route's
// static setup time is used instead of a changeover.
const CodeMissingSetupEntry = "SCHED-0012"

// MaxSetupGroup is the longest setup group name the matrix accepts.
const MaxSetupGroup = 32

// SetupEntry is one changeover from a setup group to another (FR-23.15). Cost
// is in the matrix currency. CleaningSetID names the cleaning standard
// operation set the pair requires (recipe-dependent cleaning, for example an
// allergen wet clean); empty when none is needed.
type SetupEntry struct {
	FromGroup     string          `json:"fromGroup"`
	ToGroup       string          `json:"toGroup"`
	Minutes       int             `json:"minutes"`
	Cost          decimal.Decimal `json:"cost"`
	CleaningSetID string          `json:"cleaningSetId,omitempty"`
}

// SetupMatrix holds the changeover entries of one resource. Two orders in the
// same setup group never need a changeover.
type SetupMatrix struct {
	entries map[[2]string]SetupEntry
}

// NewSetupMatrix validates the entries and indexes them by group pair.
func NewSetupMatrix(entries []SetupEntry) (SetupMatrix, error) {
	m := SetupMatrix{entries: make(map[[2]string]SetupEntry, len(entries))}
	for _, e := range entries {
		switch {
		case strings.TrimSpace(e.FromGroup) == "" || strings.TrimSpace(e.ToGroup) == "":
			return SetupMatrix{}, errors.New("setup groups must be named")
		case len(e.FromGroup) > MaxSetupGroup || len(e.ToGroup) > MaxSetupGroup:
			return SetupMatrix{}, fmt.Errorf("entry %s->%s: setup groups are at most %d characters", e.FromGroup, e.ToGroup, MaxSetupGroup)
		case e.FromGroup == e.ToGroup:
			return SetupMatrix{}, fmt.Errorf("entry %s->%s: a group needs no changeover entry to itself", e.FromGroup, e.ToGroup)
		case e.Minutes < 0 || e.Cost.IsNegative():
			return SetupMatrix{}, fmt.Errorf("entry %s->%s: minutes and cost must not be negative", e.FromGroup, e.ToGroup)
		}
		k := [2]string{e.FromGroup, e.ToGroup}
		if _, dup := m.entries[k]; dup {
			return SetupMatrix{}, fmt.Errorf("entry %s->%s is listed twice", e.FromGroup, e.ToGroup)
		}
		m.entries[k] = e
	}
	return m, nil
}

// Len is the number of changeover entries. The scheduler sequences
// changeovers only on a resource whose matrix has entries.
func (m SetupMatrix) Len() int { return len(m.entries) }

// Lookup returns the changeover from one group to another. The same group is
// free, and so is the first order on a resource whose state is unknown (from
// is empty). ok is false when no entry exists; the caller then falls back to
// the route setup time and reports SCHED-0012.
func (m SetupMatrix) Lookup(from, to string) (SetupEntry, bool) {
	if from == to || from == "" {
		return SetupEntry{FromGroup: from, ToGroup: to}, true
	}
	e, ok := m.entries[[2]string{from, to}]
	return e, ok
}

// CleanoutRule defines when a setup group must be cleaned out (FR-23.15,
// BR-23.13). A zero AfterBatches or AfterMinutes means that limit does not apply.
type CleanoutRule struct {
	SetupGroup      string
	DurationMinutes int
	CleaningSetID   string
	AfterBatches    int
	AfterMinutes    int
	AtCampaignEnd   bool
}

// Cleanout reasons returned by CleanoutDue.
const (
	CleanoutNone        = ""
	CleanoutBatches     = "batches"
	CleanoutRunTime     = "run-time"
	CleanoutCampaignEnd = "campaign-end"
)

// CleanoutDue reports whether a cleanout is due after the current batch: the
// batch limit or the run-time limit, whichever comes first, or the campaign
// end when the rule demands it (BR-23.13).
func CleanoutDue(r CleanoutRule, batchesSince, minutesSince int, campaignEnd bool) string {
	switch {
	case campaignEnd && r.AtCampaignEnd:
		return CleanoutCampaignEnd
	case r.AfterBatches > 0 && batchesSince >= r.AfterBatches:
		return CleanoutBatches
	case r.AfterMinutes > 0 && minutesSince >= r.AfterMinutes:
		return CleanoutRunTime
	}
	return CleanoutNone
}

// SetupOrder is an order waiting for a resource or campaign slot. Minutes is
// its run time; RouteSetupMinutes is the route's static setup, used as the
// changeover when the matrix has no entry for the pair (FR-23.15).
type SetupOrder struct {
	OrderID           string
	SetupGroup        string
	Due               time.Time
	Minutes           int
	RouteSetupMinutes int
}

// Changeover is a generated changeover job between two consecutive orders with
// different setup groups. BeforeOrderID is empty for the leading setup from
// the resource's current state. Missing marks a pair without a matrix entry:
// Minutes is then the route setup time and Cost is zero (SCHED-0012).
type Changeover struct {
	BeforeOrderID string          `json:"beforeOrderId,omitempty"`
	AfterOrderID  string          `json:"afterOrderId"`
	FromGroup     string          `json:"fromGroup"`
	ToGroup       string          `json:"toGroup"`
	Minutes       int             `json:"minutes"`
	Cost          decimal.Decimal `json:"cost"`
	CleaningSetID string          `json:"cleaningSetId,omitempty"`
	Missing       bool            `json:"missing"`
}

// Sequence is the output of SequenceNearest and SequenceAsGiven.
type Sequence struct {
	Orders       []SetupOrder
	Changeovers  []Changeover
	TotalMinutes int
	TotalCost    decimal.Decimal
	// Missing lists "from->to" pairs without a matrix entry (SCHED-0012).
	Missing []string
}

// changeover prices the step from prev to o.
func changeover(m SetupMatrix, prev string, o SetupOrder) (SetupEntry, bool) {
	e, ok := m.Lookup(prev, o.SetupGroup)
	if !ok {
		e = SetupEntry{FromGroup: prev, ToGroup: o.SetupGroup, Minutes: o.RouteSetupMinutes}
	}
	return e, ok
}

func (s *Sequence) add(prevGroup, prevOrder string, o SetupOrder, e SetupEntry, found bool) {
	if o.SetupGroup != prevGroup && prevGroup != "" {
		if !found {
			s.Missing = append(s.Missing, prevGroup+"->"+o.SetupGroup)
		}
		s.Changeovers = append(s.Changeovers, Changeover{BeforeOrderID: prevOrder, AfterOrderID: o.OrderID,
			FromGroup: prevGroup, ToGroup: o.SetupGroup, Minutes: e.Minutes, Cost: e.Cost,
			CleaningSetID: e.CleaningSetID, Missing: !found})
		s.TotalMinutes += e.Minutes
		s.TotalCost = s.TotalCost.Add(e.Cost)
	}
	s.Orders = append(s.Orders, o)
}

// SequenceNearest orders orders to keep changeovers low (BR-23.12). Starting
// from the resource's current setup group, it repeatedly picks the unscheduled
// order with the smallest changeover from the previous group; a pair without
// a matrix entry costs the order's route setup time. Ties go to the earliest
// due date (no due date last), then to the order id.
func SequenceNearest(m SetupMatrix, current string, orders []SetupOrder) Sequence {
	rest := slices.Clone(orders)
	out := Sequence{Orders: make([]SetupOrder, 0, len(orders)), Changeovers: []Changeover{}, Missing: []string{}}
	prevGroup, prevOrder := current, ""
	for len(rest) > 0 {
		bestIdx, bestOK := -1, false
		var bestEntry SetupEntry
		for i, o := range rest {
			e, ok := changeover(m, prevGroup, o)
			if bestIdx < 0 || nearer(o, e, rest[bestIdx], bestEntry) {
				bestIdx, bestOK, bestEntry = i, ok, e
			}
		}
		chosen := rest[bestIdx]
		rest = slices.Delete(rest, bestIdx, bestIdx+1)
		out.add(prevGroup, prevOrder, chosen, bestEntry, bestOK)
		prevGroup, prevOrder = chosen.SetupGroup, chosen.OrderID
	}
	return out
}

// SequenceAsGiven keeps the planner's order (sequence rule "manual") and
// prices the changeovers between consecutive orders.
func SequenceAsGiven(m SetupMatrix, current string, orders []SetupOrder) Sequence {
	out := Sequence{Orders: make([]SetupOrder, 0, len(orders)), Changeovers: []Changeover{}, Missing: []string{}}
	prevGroup, prevOrder := current, ""
	for _, o := range orders {
		e, ok := changeover(m, prevGroup, o)
		out.add(prevGroup, prevOrder, o, e, ok)
		prevGroup, prevOrder = o.SetupGroup, o.OrderID
	}
	return out
}

// nearer reports whether order a (reached by changeover ea) beats order b
// (reached by eb) under BR-23.12's tie rules.
func nearer(a SetupOrder, ea SetupEntry, b SetupOrder, eb SetupEntry) bool {
	if ea.Minutes != eb.Minutes {
		return ea.Minutes < eb.Minutes
	}
	if c := dueCompare(a.Due, b.Due); c != 0 {
		return c < 0
	}
	return a.OrderID < b.OrderID
}
