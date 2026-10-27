package domain

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// Predicate is one attribute test of a block condition (FR-23.17), for example
// color = white or grade >= 2. Ops are =, !=, >=, <=, > and <. Ordering
// operators compare as exact decimals; = and != compare text.
type Predicate struct {
	Attribute string `json:"attribute"`
	Op        string `json:"op"`
	Value     string `json:"value"`
}

// Validate checks the operator and attribute name.
func (p Predicate) Validate() error {
	if strings.TrimSpace(p.Attribute) == "" {
		return fmt.Errorf("predicate needs an attribute name")
	}
	switch p.Op {
	case "=", "!=":
		return nil
	case ">=", "<=", ">", "<":
		if _, err := decimal.NewFromString(p.Value); err != nil {
			return fmt.Errorf("predicate %s %s: value must be numeric", p.Attribute, p.Op)
		}
		return nil
	}
	return fmt.Errorf("predicate %s has unknown operator %q", p.Attribute, p.Op)
}

// Holds evaluates the predicate. A missing attribute never holds.
func (p Predicate) Holds(attrs map[string]string) bool {
	got, ok := attrs[p.Attribute]
	if !ok {
		return false
	}
	switch p.Op {
	case "=":
		return got == p.Value
	case "!=":
		return got != p.Value
	}
	g, errG := decimal.NewFromString(got)
	v, errV := decimal.NewFromString(p.Value)
	if errG != nil || errV != nil {
		return false
	}
	switch p.Op {
	case ">=":
		return g.GreaterThanOrEqual(v)
	case "<=":
		return g.LessThanOrEqual(v)
	case ">":
		return g.GreaterThan(v)
	case "<":
		return g.LessThan(v)
	}
	return false
}

// CapacityBlock reserves time windows on a resource for jobs whose attributes
// satisfy Condition (FR-23.17). Higher Priority wins where blocks overlap;
// equal priorities resolve by ID so the result does not depend on input order.
type CapacityBlock struct {
	ID         string
	ResourceID string
	Windows    []Interval
	Priority   int
	Condition  []Predicate
}

// Matches reports whether every predicate holds for the job's attributes. An
// empty condition matches every job.
func (b CapacityBlock) Matches(attrs map[string]string) bool {
	for _, p := range b.Condition {
		if !p.Holds(attrs) {
			return false
		}
	}
	return true
}

// Placeable returns the open capacity a job with attrs may use on one resource
// (BR-23.15). The job gets unblocked open time plus the windows of blocks it
// matches. Blocks are resolved by priority: a higher-priority block takes its
// time away from lower-priority blocks even when it does not match the job.
// The result is sorted and merged, so it is ready for placement.
func Placeable(open []Interval, blocks []CapacityBlock, attrs map[string]string) []Interval {
	ordered := slices.Clone(blocks)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority != ordered[j].Priority {
			return ordered[i].Priority > ordered[j].Priority
		}
		return ordered[i].ID < ordered[j].ID
	})
	var claimed, allowed []Interval
	for _, b := range ordered {
		match := b.Matches(attrs)
		for _, w := range b.Windows {
			for _, o := range open {
				piece, ok := ivIntersect(w, o)
				if !ok {
					continue
				}
				if match {
					allowed = append(allowed, ivSubtractAll(piece, claimed)...)
				}
				claimed = append(claimed, piece)
			}
		}
	}
	for _, o := range open {
		allowed = append(allowed, ivSubtractAll(o, claimed)...)
	}
	return ivMerge(allowed)
}

// MatchesAny reports whether the job matches at least one block. A job that
// matches none may use unblocked capacity only and is reported with SCHED-0014.
func MatchesAny(blocks []CapacityBlock, attrs map[string]string) bool {
	for _, b := range blocks {
		if b.Matches(attrs) {
			return true
		}
	}
	return false
}

// CodeNoMatchingBlock is SCHED-0014: the job matches no block on the resource.
const CodeNoMatchingBlock = "SCHED-0014"

// ivIntersect returns the overlap of two intervals, if any.
func ivIntersect(a, b Interval) (Interval, bool) {
	s, e := a.Start, a.End
	if b.Start.After(s) {
		s = b.Start
	}
	if b.End.Before(e) {
		e = b.End
	}
	if !e.After(s) {
		return Interval{}, false
	}
	return Interval{Start: s, End: e}, true
}

// ivSubtractAll removes every cut from a and returns the remaining pieces.
func ivSubtractAll(a Interval, cuts []Interval) []Interval {
	pieces := []Interval{a}
	for _, c := range cuts {
		next := pieces[:0:0]
		for _, p := range pieces {
			if !c.End.After(p.Start) || !c.Start.Before(p.End) {
				next = append(next, p)
				continue
			}
			if c.Start.After(p.Start) {
				next = append(next, Interval{Start: p.Start, End: c.Start})
			}
			if c.End.Before(p.End) {
				next = append(next, Interval{Start: c.End, End: p.End})
			}
		}
		pieces = next
	}
	return pieces
}

// ivMerge sorts intervals and joins the ones that overlap or touch.
func ivMerge(in []Interval) []Interval {
	if len(in) == 0 {
		return nil
	}
	s := slices.Clone(in)
	sort.Slice(s, func(i, j int) bool { return s[i].Start.Before(s[j].Start) })
	out := []Interval{s[0]}
	for _, iv := range s[1:] {
		last := &out[len(out)-1]
		if !iv.Start.After(last.End) {
			if iv.End.After(last.End) {
				last.End = iv.End
			}
			continue
		}
		out = append(out, iv)
	}
	return out
}
