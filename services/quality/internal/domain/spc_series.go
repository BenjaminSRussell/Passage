package domain

import (
	"slices"
	"time"

	"github.com/shopspring/decimal"
)

// Observation is one recorded result of a test on a quality order, the input of an
// SPC chart (FR-33.11). Variable results carry Value with HasValue; attribute
// results carry Passed only.
type Observation struct {
	QualityOrderID string
	SampleNo       int
	Value          decimal.Decimal
	HasValue       bool
	Passed         bool
	RecordedAt     time.Time
}

// SPCResult is the chart for one item and test: X-bar/R for variable tests, a p
// chart for attribute tests. SkippedSubgroups counts orders whose sample size
// differed from the modal size and were left off an X-bar/R chart.
type SPCResult struct {
	Chart        string `json:"chart"`
	Observations int    `json:"observations"`
	// OrderIDs names the quality order behind each chart point, in point order.
	OrderIDs         []string `json:"orderIds"`
	SkippedSubgroups int      `json:"skippedSubgroups"`
	XBarR            *XBarR   `json:"xbarR,omitempty"`
	PChart           *PChart  `json:"pChart,omitempty"`
}

// SPCResult.Chart values: the variable chart pair, the attribute chart, or NONE
// when no results are recorded yet for the item and test.
const (
	ChartXbarR = "XBAR_R"
	ChartNone  = "NONE"
)

// ComputeSPC builds the chart for the observations of one item and test. A test with
// any numeric value is variable; otherwise it is attribute. Each quality order is
// one subgroup (variable) or one inspection (attribute), in recording order. No
// observations is not an error: the result is chart NONE so a screen can show an
// empty state.
func ComputeSPC(obs []Observation) (SPCResult, error) {
	if len(obs) == 0 {
		return SPCResult{Chart: ChartNone}, nil
	}
	variable := slices.ContainsFunc(obs, func(o Observation) bool { return o.HasValue })
	if variable {
		orders, subs, skipped := variableSubgroups(obs)
		x, err := ComputeXBarR(subs)
		if err != nil {
			return SPCResult{}, err
		}
		return SPCResult{Chart: ChartXbarR, Observations: len(obs), OrderIDs: orders, SkippedSubgroups: skipped, XBarR: &x}, nil
	}
	orders, samples := attributeSamples(obs)
	p, err := ComputeP(samples)
	if err != nil {
		return SPCResult{}, err
	}
	return SPCResult{Chart: ChartP, Observations: len(obs), OrderIDs: orders, PChart: &p}, nil
}

// orderGroup is the observations of one quality order.
type orderGroup struct {
	id   string
	at   time.Time
	obss []Observation
}

// groupByOrder groups observations by quality order, oldest order first.
func groupByOrder(obs []Observation) []orderGroup {
	idx := map[string]int{}
	var groups []orderGroup
	for _, o := range obs {
		i, ok := idx[o.QualityOrderID]
		if !ok {
			i = len(groups)
			idx[o.QualityOrderID] = i
			groups = append(groups, orderGroup{id: o.QualityOrderID, at: o.RecordedAt})
		}
		if o.RecordedAt.Before(groups[i].at) {
			groups[i].at = o.RecordedAt
		}
		groups[i].obss = append(groups[i].obss, o)
	}
	slices.SortStableFunc(groups, func(a, b orderGroup) int {
		if c := a.at.Compare(b.at); c != 0 {
			return c
		}
		if a.id < b.id {
			return -1
		}
		if a.id > b.id {
			return 1
		}
		return 0
	})
	return groups
}

// variableSubgroups returns the numeric subgroups of the modal size. The modal size
// is the most common subgroup size; ties go to the larger size. Other sizes are
// counted as skipped.
func variableSubgroups(obs []Observation) ([]string, [][]decimal.Decimal, int) {
	var groups [][]Observation
	var groupIDs []string
	for _, g := range groupByOrder(obs) {
		var vals []Observation
		for _, o := range g.obss {
			if o.HasValue {
				vals = append(vals, o)
			}
		}
		if len(vals) == 0 {
			continue
		}
		slices.SortFunc(vals, func(a, b Observation) int { return a.SampleNo - b.SampleNo })
		groups = append(groups, vals)
		groupIDs = append(groupIDs, g.id)
	}
	count := map[int]int{}
	for _, g := range groups {
		count[len(g)]++
	}
	modal := 0
	for size, c := range count {
		if c > count[modal] || (c == count[modal] && size > modal) {
			modal = size
		}
	}
	var out [][]decimal.Decimal
	var ids []string
	for gi, g := range groups {
		if len(g) != modal {
			continue
		}
		ids = append(ids, groupIDs[gi])
		row := make([]decimal.Decimal, len(g))
		for i, o := range g {
			row[i] = o.Value
		}
		out = append(out, row)
	}
	return ids, out, len(groups) - len(out)
}

// attributeSamples returns one p-chart sample per quality order: the distinct samples
// inspected, and how many of them had any failing result.
func attributeSamples(obs []Observation) ([]string, []PSample) {
	var out []PSample
	var ids []string
	for _, g := range groupByOrder(obs) {
		ids = append(ids, g.id)
		failed := map[int]bool{}
		for _, o := range g.obss {
			failed[o.SampleNo] = failed[o.SampleNo] || !o.Passed
		}
		s := PSample{Size: len(failed)}
		for _, f := range failed {
			if f {
				s.Defectives++
			}
		}
		out = append(out, s)
	}
	return ids, out
}
