package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
)

// Period is one ledger period of a fiscal calendar. No is the period number
// within the fiscal year (adjustment periods carry their own number).
type Period struct {
	Year       int
	No         int
	Quarter    int
	Adjustment bool
}

// Calendar is the ordered list of periods a ledger has balances for.
type Calendar struct {
	periods []Period
}

// NewCalendar sorts the periods by (year, no) and rejects duplicates.
func NewCalendar(ps []Period) (Calendar, error) {
	sorted := append([]Period(nil), ps...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Year != sorted[j].Year {
			return sorted[i].Year < sorted[j].Year
		}
		return sorted[i].No < sorted[j].No
	})
	for i := 1; i < len(sorted); i++ {
		if sorted[i].Year == sorted[i-1].Year && sorted[i].No == sorted[i-1].No {
			return Calendar{}, errf(CodePeriodNotFound, "period %04d-%02d appears twice", sorted[i].Year, sorted[i].No)
		}
	}
	return Calendar{periods: sorted}, nil
}

// Periods returns the periods in calendar order.
func (c Calendar) Periods() []Period { return c.periods }

// Len is the number of periods.
func (c Calendar) Len() int { return len(c.periods) }

// Index finds the position of (year, no).
func (c Calendar) Index(year, no int) (int, bool) {
	for i, p := range c.periods {
		if p.Year == year && p.No == no {
			return i, true
		}
	}
	return 0, false
}

// Label formats a period as YYYY-PP, the form used in balance keys.
func (p Period) Label() string { return fmt.Sprintf("%04d-%02d", p.Year, p.No) }

var periodExprPattern = regexp.MustCompile(`^(P|P-[0-9]+|YTD|PY|PYTD|Q|FY|CALC)$`)

// ValidPeriodExpr reports whether expr is one of the column period expressions.
func ValidPeriodExpr(expr string) bool { return periodExprPattern.MatchString(expr) }

// Select evaluates a column period expression for the run period at (an index
// into cal) and returns the calendar indexes it covers, in calendar order.
// found is false when the expression refers to a period the calendar does not
// have (for example PY after a calendar change, BR-15.9). CALC selects nothing:
// its values come from other columns.
//
// Adjustment periods are skipped unless includeAdj is set (BR-15.1).
func Select(expr string, at int, cal Calendar, includeAdj bool) (sel []int, found bool, err error) {
	if at < 0 || at >= cal.Len() {
		return nil, false, errf(CodePeriodNotFound, "run period index %d is outside the calendar", at)
	}
	if !ValidPeriodExpr(expr) {
		return nil, false, errf(CodeExpression, "period expression %q is not one of P, P-n, YTD, PY, PYTD, Q, FY, CALC", expr)
	}
	ps := cal.periods
	cur := ps[at]
	counted := func(i int) bool { return includeAdj || !ps[i].Adjustment }
	filter := func(keep func(i int) bool) []int {
		var out []int
		for i := range ps {
			if counted(i) && keep(i) {
				out = append(out, i)
			}
		}
		return out
	}
	switch {
	case expr == "CALC":
		return nil, true, nil
	case expr == "P":
		return []int{at}, true, nil
	case len(expr) > 2 && expr[:2] == "P-":
		n, convErr := strconv.Atoi(expr[2:])
		if convErr != nil {
			return nil, false, errf(CodeExpression, "period expression %q has no offset", expr)
		}
		idx := at
		for step := 0; step < n; step++ {
			idx = previousCounted(ps, idx, counted)
			if idx < 0 {
				return nil, false, nil
			}
		}
		return []int{idx}, true, nil
	case expr == "YTD":
		return filter(func(i int) bool { return ps[i].Year == cur.Year && i <= at }), true, nil
	case expr == "FY":
		return filter(func(i int) bool { return ps[i].Year == cur.Year }), true, nil
	case expr == "Q":
		return filter(func(i int) bool { return ps[i].Year == cur.Year && ps[i].Quarter == cur.Quarter }), true, nil
	case expr == "PY":
		py, ok := cal.Index(cur.Year-1, cur.No)
		if !ok {
			return nil, false, nil
		}
		return []int{py}, true, nil
	default: // PYTD
		py, ok := cal.Index(cur.Year-1, cur.No)
		if !ok {
			return nil, false, nil
		}
		return filter(func(i int) bool { return ps[i].Year == cur.Year-1 && i <= py }), true, nil
	}
}

func previousCounted(ps []Period, from int, counted func(int) bool) int {
	for i := from - 1; i >= 0; i-- {
		if counted(i) {
			return i
		}
	}
	return -1
}
