package domain

import (
	"sort"

	"github.com/shopspring/decimal"
)

// Comparison row statuses.
const (
	RowCompared      = "Compared"
	RowBaselineOnly  = "BaselineOnly"
	RowCandidateOnly = "CandidateOnly"
)

// ComparisonRow sets one item's unit standard in a baseline run against a
// candidate run (FR-39.10). A candidate with overridden prices, rates or routes
// is the what-if simulation. ByComponent is candidate − baseline per component.
type ComparisonRow struct {
	ItemID      string          `json:"itemId"`
	Status      string          `json:"status"`
	Baseline    decimal.Decimal `json:"baseline"`
	Candidate   decimal.Decimal `json:"candidate"`
	Delta       decimal.Decimal `json:"delta"`
	DeltaPct    decimal.Decimal `json:"deltaPct"`
	ByComponent Split           `json:"byComponent"`
}

// Comparison is the item-by-item comparison of two runs.
type Comparison struct {
	Rows       []ComparisonRow `json:"rows"`
	TotalDelta decimal.Decimal `json:"totalDelta"`
}

// Compare sets two roll runs item by item (FR-39.10). Rows are sorted by item
// id. Items that errored on either side keep status Error and carry no delta,
// so one bad item never distorts the total.
func Compare(baseline, candidate RollRun) Comparison {
	base := indexResults(baseline)
	cand := indexResults(candidate)
	ids := make([]string, 0, len(base)+len(cand))
	seen := map[string]bool{}
	for id := range base {
		ids = append(ids, id)
		seen[id] = true
	}
	for id := range cand {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	out := Comparison{Rows: make([]ComparisonRow, 0, len(ids)), TotalDelta: decimal.Zero}
	for _, id := range ids {
		b, hasB := base[id]
		c, hasC := cand[id]
		row := ComparisonRow{ItemID: id, Baseline: decimal.Zero, Candidate: decimal.Zero, Delta: decimal.Zero, DeltaPct: decimal.Zero, ByComponent: Split{}}
		switch {
		case !hasB:
			row.Status = RowCandidateOnly
			row.Candidate = c.Unit
		case !hasC:
			row.Status = RowBaselineOnly
			row.Baseline = b.Unit
		case b.Status != StatusPending || c.Status != StatusPending:
			row.Status = StatusError
		default:
			row.Status = RowCompared
			row.Baseline, row.Candidate = b.Unit, c.Unit
			row.Delta = c.Unit.Sub(b.Unit).Round(UnitScale)
			if !b.Unit.IsZero() {
				row.DeltaPct = row.Delta.Mul(decimal.NewFromInt(100)).Div(b.Unit).Round(MoneyScale)
			}
			bs, cs := b.Combined(), c.Combined()
			for _, comp := range unionComponents(bs, cs) {
				row.ByComponent.Add(comp, cs[comp].Sub(bs[comp]).Round(UnitScale))
			}
			out.TotalDelta = out.TotalDelta.Add(row.Delta)
		}
		out.Rows = append(out.Rows, row)
	}
	return out
}

func indexResults(run RollRun) map[string]Result {
	m := make(map[string]Result, len(run.Results))
	for _, r := range run.Results {
		m[r.ItemID] = r
	}
	return m
}

func unionComponents(a, b Split) []string {
	seen := map[string]bool{}
	for c := range a {
		seen[c] = true
	}
	for c := range b {
		seen[c] = true
	}
	keys := make([]string, 0, len(seen))
	for c := range seen {
		keys = append(keys, c)
	}
	sort.Strings(keys)
	return keys
}

// StandardOnHand is the on-hand quantity of one item at one site with its
// active standard unit cost (FR-39.11).
type StandardOnHand struct {
	SiteID  string          `json:"siteId"`
	ItemID  string          `json:"itemId"`
	Qty     decimal.Decimal `json:"qty"`
	StdUnit decimal.Decimal `json:"stdUnit"`
}

// SiteBalance is the GL inventory balance of one site.
type SiteBalance struct {
	SiteID string          `json:"siteId"`
	Amount decimal.Decimal `json:"amount"`
}

// Reconciliation statuses.
const (
	ReconReconciled = "Reconciled"
	ReconDifference = "Difference"
)

// SiteReconciliation compares Σ on-hand × standard with the GL inventory
// balance of one site. Difference is standard − GL; any non-zero difference is
// reported (the nightly alert rule).
type SiteReconciliation struct {
	SiteID        string          `json:"siteId"`
	Lines         int             `json:"lines"`
	StandardValue decimal.Decimal `json:"standardValue"`
	GLBalance     decimal.Decimal `json:"glBalance"`
	Difference    decimal.Decimal `json:"difference"`
	Status        string          `json:"status"`
}

// ReconcileStandard reconciles inventory at standard to the GL per site
// (FR-39.11, AC-39.9). Each on-hand line is valued to scale decimals
// (PostingScale) before the sum, as the inventory postings are. Sites present
// only in the GL or only in on-hand are reported too.
func ReconcileStandard(onHand []StandardOnHand, gl []SiteBalance, scale int32) []SiteReconciliation {
	type acc struct {
		lines int
		std   decimal.Decimal
		gl    decimal.Decimal
	}
	sites := map[string]*acc{}
	get := func(site string) *acc {
		a, ok := sites[site]
		if !ok {
			a = &acc{std: decimal.Zero, gl: decimal.Zero}
			sites[site] = a
		}
		return a
	}
	for _, l := range onHand {
		a := get(l.SiteID)
		a.lines++
		a.std = a.std.Add(l.Qty.Mul(l.StdUnit).Round(scale))
	}
	for _, b := range gl {
		a := get(b.SiteID)
		a.gl = a.gl.Add(b.Amount)
	}
	keys := make([]string, 0, len(sites))
	for k := range sites {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]SiteReconciliation, 0, len(keys))
	for _, k := range keys {
		a := sites[k]
		diff := a.std.Sub(a.gl).Round(scale)
		status := ReconReconciled
		if !diff.IsZero() {
			status = ReconDifference
		}
		out = append(out, SiteReconciliation{SiteID: k, Lines: a.lines, StandardValue: a.std, GLBalance: a.gl, Difference: diff, Status: status})
	}
	return out
}
