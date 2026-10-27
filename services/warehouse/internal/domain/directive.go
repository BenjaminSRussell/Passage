package domain

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/shopspring/decimal"
)

// Strategy orders the candidate locations of an action (FR-31.3).
type Strategy string

const (
	StrategyConsolidate          Strategy = "consolidate"
	StrategyFIFO                 Strategy = "fifo"
	StrategyLIFO                 Strategy = "lifo"
	StrategyFEFO                 Strategy = "fefo"
	StrategyEmptyLocation        Strategy = "empty_location"
	StrategyMatchPackingQuantity Strategy = "match_packing_quantity"
	StrategyRoundUpToLP          Strategy = "round_up_to_lp"
	StrategyOutboundStaging      Strategy = "outbound_staging"
)

// Query is the slice-1 subset of the metadata query model: every key must equal
// the line attribute of the same name. An empty query matches every line.
type Query map[string]string

// Match reports whether every query key equals the line's attribute.
func (q Query) Match(attrs map[string]string) bool {
	for k, v := range q {
		if attrs[k] != v {
			return false
		}
	}
	return true
}

// Band is one quantity range of a directive, from_qty <= qty < to_qty. A nil
// ToQty is unbounded. Bands within a directive are mutually exclusive.
type Band struct {
	Seq     int              `json:"seq"`
	FromQty decimal.Decimal  `json:"fromQty"`
	ToQty   *decimal.Decimal `json:"toQty,omitempty"`
	Actions []Action         `json:"actions"`
}

// Directive is an ordered rule that resolves a PUT or PICK location for a line (BR-31.1).
type Directive struct {
	ID        string    `json:"id"`
	Sequence  int       `json:"sequence"`
	WorkType  string    `json:"workType"`
	Direction Direction `json:"direction"`
	Warehouse string    `json:"warehouseId"`
	Query     Query     `json:"query,omitempty"`
	Bands     []Band    `json:"lines"`
}

// Line is the work line a directive is evaluated for. Qty is in base UoM; the
// caller converts units (wave 6). Weight and Volume are totals for the line.
type Line struct {
	ItemID     string            `json:"itemId"`
	WorkType   string            `json:"workType"`
	Direction  Direction         `json:"direction"`
	Warehouse  string            `json:"warehouseId"`
	Qty        decimal.Decimal   `json:"qty"`
	Weight     decimal.Decimal   `json:"weight"`
	Volume     decimal.Decimal   `json:"volume"`
	LPs        int               `json:"lps"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// Step is one entry of the simulation trace: which directive, line and action
// was considered and whether it hit, missed or was out of scope.
type Step struct {
	Directive string `json:"directive"`
	Band      int    `json:"band,omitempty"`
	Action    int    `json:"action,omitempty"`
	Outcome   string `json:"outcome"`
	Reason    string `json:"reason"`
}

// Result is the outcome of Evaluate. A blocked line has a nil Location and
// Code WMS-0101; the trace says why each directive missed.
type Result struct {
	Location  *Location `json:"-"`
	Directive string    `json:"directive,omitempty"`
	Blocked   bool      `json:"blocked"`
	Code      string    `json:"code,omitempty"`
	Trace     []Step    `json:"trace"`
}

// Evaluate resolves the location for line (BR-31.1). Directives are taken in
// sequence order. The first directive whose query matches and whose band covers
// the quantity is tried: its actions run in order, and the first action with a
// candidate wins with the top candidate. If no action yields one, the next
// directive is tried. If none does, the line is blocked.
func Evaluate(directives []Directive, line Line, locations []Location) Result {
	return EvaluateWithCompliance(directives, line, locations, Compliance{})
}

// EvaluateWithCompliance is Evaluate with hazmat reference data (FR-31.19). A PUT
// of a hazmat item only admits locations that pass BR-31.15.
func EvaluateWithCompliance(directives []Directive, line Line, locations []Location, comp Compliance) Result {
	ordered := slices.Clone(directives)
	slices.SortStableFunc(ordered, func(a, b Directive) int { return cmp.Compare(a.Sequence, b.Sequence) })
	env := evalEnv{comp: comp, secs: comp.sectionsOf(locations)}
	var trace []Step
	for _, d := range ordered {
		if d.WorkType != line.WorkType || d.Direction != line.Direction || d.Warehouse != line.Warehouse {
			trace = append(trace, Step{Directive: d.ID, Outcome: "skip", Reason: "scope: work type, direction or warehouse differs"})
			continue
		}
		if !d.Query.Match(line.Attributes) {
			trace = append(trace, Step{Directive: d.ID, Outcome: "miss", Reason: "query does not match the line"})
			continue
		}
		band, ok := d.bandFor(line.Qty)
		if !ok {
			trace = append(trace, Step{Directive: d.ID, Outcome: "miss", Reason: fmt.Sprintf("no line covers quantity %s", line.Qty)})
			continue
		}
		for _, act := range sortedActions(band.Actions) {
			cands, rejected := candidates(act, line, locations, env)
			if len(cands) == 0 {
				trace = append(trace, Step{Directive: d.ID, Band: band.Seq, Action: act.Seq, Outcome: "miss",
					Reason: fmt.Sprintf("action %d %s: %s", act.Seq, act.Strategy, summarize(rejected))})
				continue
			}
			chosen := cands[0]
			reason := fmt.Sprintf("action %d %s: %d candidates, chose %s", act.Seq, act.Strategy, len(cands), chosen.Name)
			if len(rejected) > 0 {
				reason += "; " + summarize(rejected)
			}
			trace = append(trace, Step{Directive: d.ID, Band: band.Seq, Action: act.Seq, Outcome: "hit", Reason: reason})
			return Result{Location: &chosen, Directive: d.ID, Trace: trace}
		}
	}
	return Result{Blocked: true, Code: CodeNoLocation, Trace: trace}
}

func (d Directive) bandFor(qty decimal.Decimal) (Band, bool) {
	bands := slices.Clone(d.Bands)
	slices.SortStableFunc(bands, func(a, b Band) int { return cmp.Compare(a.Seq, b.Seq) })
	for _, b := range bands {
		if qty.LessThan(b.FromQty) {
			continue
		}
		if b.ToQty != nil && !qty.LessThan(*b.ToQty) {
			continue
		}
		return b, true
	}
	return Band{}, false
}

func sortedActions(actions []Action) []Action {
	out := slices.Clone(actions)
	slices.SortStableFunc(out, func(a, b Action) int { return cmp.Compare(a.Seq, b.Seq) })
	return out
}

// candidates returns the locations an action admits, best first, and the count
// of refusals by reason (rendered by summarize, e.g. "rejected capacity: lps 1/1 (2)").
func candidates(act Action, line Line, locations []Location, env evalEnv) ([]Location, map[string]int) {
	var out []Location
	rejected := map[string]int{}
	for _, l := range locations {
		if act.Zone != "" && l.Zone != act.Zone {
			continue
		}
		reason, ok := admit(act, line, l, env)
		if !ok {
			rejected[reason]++
			continue
		}
		out = append(out, l)
	}
	sortCandidates(out, act.Strategy, line.ItemID)
	return out, rejected
}

// admit is the per-location test for an action: direction and status first,
// then for PUT the empty and mixing rules and capacity (BR-31.2), and for PICK
// that the location holds the whole line quantity. Splitting one pick line
// across several locations is a later slice, so a location holding less than
// the line is not a candidate. A PUT of a hazmat item must also pass the
// storage-compatibility check (BR-31.15).
func admit(act Action, line Line, l Location, env evalEnv) (string, bool) {
	if !l.Status.Allows(line.Direction) {
		return fmt.Sprintf("status %s blocks %s", l.Status, line.Direction), false
	}
	if line.Direction == DirectionPick {
		if !l.holds(line.ItemID) {
			return "no stock of item", false
		}
		if !l.covers(line.ItemID, line.Qty) {
			return "stock below line quantity", false
		}
		return "", true
	}
	if act.Strategy == StrategyEmptyLocation && !l.isEmpty() {
		return "not empty", false
	}
	if reason, ok := l.admitPut(line.ItemID, Usage{Weight: line.Weight, Volume: line.Volume, LPs: line.LPs}); !ok {
		return reason, false
	}
	return env.comp.admitHazmat(line, l, env.secs)
}

// evalEnv carries the per-evaluation hazmat view so it is built once, not per location.
type evalEnv struct {
	comp Compliance
	secs sections
}

func summarize(rejected map[string]int) string {
	if len(rejected) == 0 {
		return "no locations in zone"
	}
	keys := make([]string, 0, len(rejected))
	for k := range rejected {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s (%d)", k, rejected[k]))
	}
	return "rejected " + strings.Join(parts, ", ")
}
