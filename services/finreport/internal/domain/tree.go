package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Slice-local message codes for reporting trees (FR-15.4); they are not in the
// PRD catalog yet.
const (
	// CodeTreeInvalid is REPORT-0039: a reporting tree body is malformed (a tree
	// that does not exist answers REPORT-0037 like the other designer parts).
	CodeTreeInvalid = "REPORT-0039"
	// CodeTreeRun is REPORT-0040: the run cannot be limited to the tree or unit.
	CodeTreeRun = "REPORT-0040"
	// CodeTreeOutside is REPORT-0041, the warning stored with a roll-up run
	// whose ledger holds balances for values the tree does not list.
	CodeTreeOutside = "REPORT-0041"
)

// Limits of a reporting tree.
const (
	MaxTreeUnits  = 500
	MaxTreeDepth  = 10
	MaxUnitValues = 200
	treeSample    = 5
)

var unitCodePattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_.-]{0,29}$`)

// TreeUnit is one reporting unit of a tree. Values are the values of the tree's
// dimension attribute (for example profit center numbers) whose balances belong
// to the unit itself; a unit also covers the values of all units below it.
type TreeUnit struct {
	Code   string   `json:"code"`
	Name   string   `json:"name"`
	Parent string   `json:"parent,omitempty"`
	Values []string `json:"values,omitempty"`
}

// Tree is a reporting tree (FR-15.4, PRD §2.15.5 /trees): reporting units over
// the values of one dimension attribute, with roll-up. A statement run takes
// one unit; a parent unit's run is the roll-up of everything below it.
type Tree struct {
	Code      string     `json:"code"`
	Name      string     `json:"name"`
	Attribute string     `json:"attribute"`
	Units     []TreeUnit `json:"units"`
	UpdatedBy string     `json:"updatedBy,omitempty"`
}

// Validate checks a tree on its own: code and name, the dimension attribute,
// unit codes and parents (known, no cycles, depth), and that a dimension value
// belongs to at most one unit, which keeps roll-ups from counting it twice.
// A unit with no children must list at least one value.
func (t Tree) Validate() []Issue {
	out := partCodeIssues(t.Code, t.Name)
	bad := func(field, detail string) {
		out = append(out, Issue{Field: field, Code: CodeTreeInvalid, Detail: detail})
	}
	if !ValidCode(t.Attribute) {
		bad("attribute", "must name the dimension attribute the units group, such as profit_center")
	}
	switch {
	case len(t.Units) == 0:
		bad("units", "must list at least one unit")
		return out
	case len(t.Units) > MaxTreeUnits:
		bad("units", fmt.Sprintf("may list at most %d units", MaxTreeUnits))
		return out
	}
	byCode := map[string]int{}
	hasChild := map[string]bool{}
	for i, u := range t.Units {
		f := fmt.Sprintf("units[%d]", i)
		if !unitCodePattern.MatchString(u.Code) {
			bad(f+".code", "must be 1 to 30 characters: upper-case letters, digits, dots, hyphens or underscores")
		} else if _, dup := byCode[u.Code]; dup {
			bad(f+".code", "duplicate unit code "+u.Code)
		}
		byCode[u.Code] = i
		if n := utf8.RuneCountInString(u.Name); n < 1 || n > 120 {
			bad(f+".name", "must be 1 to 120 characters")
		}
		if u.Parent != "" {
			hasChild[u.Parent] = true
		}
	}
	owner := map[string]string{}
	for i, u := range t.Units {
		f := fmt.Sprintf("units[%d]", i)
		if u.Parent != "" {
			if _, ok := byCode[u.Parent]; !ok {
				bad(f+".parent", "unit "+u.Parent+" does not exist")
			} else if u.Parent == u.Code {
				bad(f+".parent", "a unit cannot be its own parent")
			}
		}
		if len(u.Values) > MaxUnitValues {
			bad(f+".values", fmt.Sprintf("may list at most %d values", MaxUnitValues))
		}
		if len(u.Values) == 0 && !hasChild[u.Code] {
			bad(f+".values", "a unit without child units must list at least one value")
		}
		for j, v := range u.Values {
			if v == "" || utf8.RuneCountInString(v) > 60 || v != strings.TrimSpace(v) {
				bad(fmt.Sprintf("%s.values[%d]", f, j), "must be 1 to 60 characters without leading or trailing blanks")
				continue
			}
			if prev, dup := owner[v]; dup {
				if prev == u.Code {
					bad(fmt.Sprintf("%s.values[%d]", f, j), "value "+v+" is listed twice")
				} else {
					bad(fmt.Sprintf("%s.values[%d]", f, j), "value "+v+" already belongs to unit "+prev)
				}
				continue
			}
			owner[v] = u.Code
		}
	}
	if len(out) > 0 {
		return out
	}
	// Cycles and depth, once every parent is known.
	for i, u := range t.Units {
		depth, cur := 1, u
		for cur.Parent != "" {
			depth++
			if depth > MaxTreeDepth+len(t.Units) || cur.Parent == u.Code {
				bad(fmt.Sprintf("units[%d].parent", i), "units form a cycle")
				return out
			}
			cur = t.Units[byCode[cur.Parent]]
		}
		if depth > MaxTreeDepth {
			bad(fmt.Sprintf("units[%d].parent", i), fmt.Sprintf("is nested deeper than %d levels", MaxTreeDepth))
		}
	}
	return out
}

// Coverage returns the dimension values the unit and every unit below it list.
// An empty unit means the whole tree: every unit. An unknown unit is
// REPORT-0040.
func (t Tree) Coverage(unit string) (map[string]bool, error) {
	known := map[string]bool{}
	children := map[string][]string{}
	for _, u := range t.Units {
		known[u.Code] = true
		children[u.Parent] = append(children[u.Parent], u.Code)
	}
	if unit != "" && !known[unit] {
		return nil, errf(CodeTreeRun, "tree %s has no unit %s.", t.Code, unit)
	}
	byCode := map[string]TreeUnit{}
	for _, u := range t.Units {
		byCode[u.Code] = u
	}
	out := map[string]bool{}
	var walk func(code string)
	walk = func(code string) {
		for _, v := range byCode[code].Values {
			out[v] = true
		}
		for _, c := range children[code] {
			walk(c)
		}
	}
	if unit == "" {
		for _, root := range children[""] {
			walk(root)
		}
	} else {
		walk(unit)
	}
	return out, nil
}

// Subtree returns the tree limited to unit and the units below it, in the
// tree's order (the whole tree when unit is empty). The unit keeps its parent
// code. It is what a run on the unit used, and all a run's viewer is shown.
func (t Tree) Subtree(unit string) Tree {
	if unit == "" {
		return t
	}
	keep := map[string]bool{unit: true}
	for grew := true; grew; {
		grew = false
		for _, u := range t.Units {
			if !keep[u.Code] && u.Parent != "" && keep[u.Parent] {
				keep[u.Code] = true
				grew = true
			}
		}
	}
	out := t
	out.Units = nil
	for _, u := range t.Units {
		if keep[u.Code] {
			out.Units = append(out.Units, u)
		}
	}
	return out
}

// TreeScope is what limiting a run to a unit left out.
type TreeScope struct {
	// Outside lists (sorted) the dimension values with balances that the unit
	// does not cover and, for a whole-tree run, the tree does not list.
	Outside []string
	// Blank is true when some balances carry no value for the attribute.
	Blank bool
}

// LimitToUnit keeps the balances whose dimension attribute value the unit
// covers (the unit's roll-up; the whole tree when unit is empty). A balance
// without a value for the attribute is never in a unit.
func LimitToUnit(t Tree, unit string, balances []Balance) ([]Balance, TreeScope, error) {
	cover, err := t.Coverage(unit)
	if err != nil {
		return nil, TreeScope{}, err
	}
	var kept []Balance
	var scope TreeScope
	outside := map[string]bool{}
	for _, b := range balances {
		v := b.Dims[t.Attribute]
		switch {
		case v == "":
			scope.Blank = true
		case cover[v]:
			kept = append(kept, b)
		default:
			outside[v] = true
		}
	}
	for v := range outside {
		scope.Outside = append(scope.Outside, v)
	}
	sort.Strings(scope.Outside)
	return kept, scope, nil
}

// TreeRunCheck is the warning a whole-tree run carries when the ledger has
// balances for values the tree does not list, or without a value for the
// attribute: the statement then does not cover the whole ledger. Nil when the
// run is for one unit (leaving other units out is the point) or nothing is left
// out.
func TreeRunCheck(t Tree, unit string, scope TreeScope) *Check {
	if unit != "" || (len(scope.Outside) == 0 && !scope.Blank) {
		return nil
	}
	detail := fmt.Sprintf("%d values of %s have balances but no unit in tree %s", len(scope.Outside), t.Attribute, t.Code)
	if len(scope.Outside) > 0 {
		sample := scope.Outside
		if len(sample) > treeSample {
			sample = sample[:treeSample]
		}
		detail += ": " + strings.Join(sample, ", ")
	}
	if scope.Blank {
		detail += "; some balances carry no value for " + t.Attribute
	}
	return &Check{Code: CodeTreeOutside, Severity: SeverityWarning, Detail: detail + "; they are left out of the statement."}
}
