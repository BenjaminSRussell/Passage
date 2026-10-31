package domain

import (
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

// OCMMode is the order change management profile mode (FR-21.16): automatic,
// manual (the planner confirms) or none (superseded versions are ignored).
type OCMMode string

const (
	OCMAutomatic OCMMode = "automatic"
	OCMManual    OCMMode = "manual"
	OCMNone      OCMMode = "none"
)

// Valid reports whether m is automatic, manual or none.
func (m OCMMode) Valid() bool { return m == OCMAutomatic || m == OCMManual || m == OCMNone }

// Stable error codes of order change management (FR-21.16).
const (
	// CodeChangeNoDelta: the order already matches the new BOM and route.
	CodeChangeNoDelta = "PRODUCTION_ORDER_CHANGE_NO_DELTA"
	// CodeChangeResolved: the change was already applied or dismissed.
	CodeChangeResolved = "PRODUCTION_ORDER_CHANGE_RESOLVED"
	// CodeChangeNotEligible: the order's status no longer allows a change.
	CodeChangeNotEligible = "PRODUCTION_ORDER_CHANGE_NOT_ELIGIBLE"
	// CodeChangeSuperseded: a later ECO on the order replaces the same BOM or
	// route, so applying this older change would undo it.
	CodeChangeSuperseded = "PRODUCTION_ORDER_CHANGE_SUPERSEDED"
)

// OCMProfile is the change profile of an item group × site (either may be
// AllKey): the mode and the change-cost surcharge, a fixed amount plus a
// percentage of a positive cost delta (BR-21.12).
type OCMProfile struct {
	ItemGroup      string          `json:"itemGroup"`
	SiteID         string          `json:"siteId"`
	Mode           OCMMode         `json:"mode"`
	SurchargeFixed decimal.Decimal `json:"surchargeFixed"`
	SurchargePct   decimal.Decimal `json:"surchargePct"`
}

// ResolveOCMProfile picks the most specific profile like posting profiles do
// (item group × site, item group × All, All × site, All × All). With none
// stored the default applies: manual, no surcharge (PRD 4.21.13).
func ResolveOCMProfile(profiles []OCMProfile, itemGroup, siteID string) OCMProfile {
	if itemGroup == "" {
		itemGroup = AllKey
	}
	for _, want := range [][2]string{{itemGroup, siteID}, {itemGroup, AllKey}, {AllKey, siteID}, {AllKey, AllKey}} {
		for _, p := range profiles {
			if p.ItemGroup == want[0] && p.SiteID == want[1] {
				return p
			}
		}
	}
	return OCMProfile{ItemGroup: AllKey, SiteID: AllKey, Mode: OCMManual}
}

// CheckChangeEligible applies FR-21.16 to an order's status: a change applies
// until the order is Started (the BOM and route of a Started order are locked,
// see CheckOrderEditable; `ocm.allowStarted` is not built).
func CheckChangeEligible(s Status) error {
	if err := CheckOrderEditable(s); err != nil {
		return &Error{Code: CodeChangeNotEligible, Detail: fmt.Sprintf("a %s order takes no BOM or route change", s)}
	}
	return nil
}

// ChangeDelta counts the differences between an order and a new BOM and route.
type ChangeDelta struct {
	LinesAdded        int `json:"linesAdded"`
	LinesRemoved      int `json:"linesRemoved"`
	LinesChanged      int `json:"linesChanged"`
	OperationsAdded   int `json:"operationsAdded"`
	OperationsRemoved int `json:"operationsRemoved"`
	OperationsChanged int `json:"operationsChanged"`
}

// Total is the number of differences.
func (d ChangeDelta) Total() int {
	return d.LinesAdded + d.LinesRemoved + d.LinesChanged + d.OperationsAdded + d.OperationsRemoved + d.OperationsChanged
}

// DiffSnapshot turns a new exploded BOM and route into the order-specific
// edits (FR-21.18) that make the order match them, so applying a change reuses
// the edit rules: a line that journals or consumption reference is never
// removed or re-pointed to another item. A nil next slice leaves lines (or
// operations) alone, for an ECO that touches only the BOM (or only the route).
func DiffSnapshot(lines []OrderLine, ops []Operation, nextLines []OrderLine, nextOps []Operation, replaceLines, replaceOps bool) (OrderEdits, ChangeDelta) {
	var edits OrderEdits
	var delta ChangeDelta
	if replaceLines {
		have := map[string]OrderLine{}
		for _, l := range lines {
			have[l.LineNo] = l
		}
		want := map[string]bool{}
		for _, n := range nextLines {
			want[n.LineNo] = true
			cur, ok := have[n.LineNo]
			switch {
			case !ok:
				item, per, series, scrap, up, cost, fl := n.ItemID, n.QtyPer, n.SeriesSize, n.ScrapPct, n.RoundUp, n.UnitCost, n.Flushing
				edits.Lines = append(edits.Lines, LineEdit{Op: EditAdd, LineNo: n.LineNo, ItemID: &item, QtyPer: &per, SeriesSize: &series,
					ScrapPct: &scrap, RoundUp: &up, UnitCost: &cost, Flushing: &fl})
				delta.LinesAdded++
			case lineDiffers(cur, n):
				item, per, series, scrap, up, cost, fl := n.ItemID, n.QtyPer, n.SeriesSize, n.ScrapPct, n.RoundUp, n.UnitCost, n.Flushing
				edits.Lines = append(edits.Lines, LineEdit{Op: EditChange, LineNo: n.LineNo, ItemID: &item, QtyPer: &per, SeriesSize: &series,
					ScrapPct: &scrap, RoundUp: &up, UnitCost: &cost, Flushing: &fl})
				delta.LinesChanged++
			}
		}
		var gone []string
		for _, l := range lines {
			if !want[l.LineNo] {
				gone = append(gone, l.LineNo)
			}
		}
		for _, no := range gone {
			edits.Lines = append(edits.Lines, LineEdit{Op: EditRemove, LineNo: no})
			delta.LinesRemoved++
		}
	}
	if replaceOps {
		have := map[int]Operation{}
		for _, o := range ops {
			have[o.OperNumber] = o
		}
		want := map[int]bool{}
		for _, n := range nextOps {
			want[n.OperNumber] = true
			cur, ok := have[n.OperNumber]
			if ok && !operationDiffers(cur, n) {
				continue
			}
			op := EditChange
			if !ok {
				op = EditAdd
				delta.OperationsAdded++
			} else {
				delta.OperationsChanged++
			}
			res, setup, run, scrap, labor, oh, sub, price := n.ResourceID, n.SetupH, n.RunHPer, n.ScrapPct, n.LaborRate, n.OverheadRate, n.Subcontract, n.SubcontractPrice
			edits.Operations = append(edits.Operations, OperationEdit{Op: op, OperNumber: n.OperNumber, ResourceID: &res, SetupH: &setup,
				RunHPer: &run, ScrapPct: &scrap, LaborRate: &labor, OverheadRate: &oh, Subcontract: &sub, SubcontractPrice: &price})
		}
		var gone []int
		for _, o := range ops {
			if !want[o.OperNumber] {
				gone = append(gone, o.OperNumber)
			}
		}
		sort.Ints(gone)
		for _, n := range gone {
			edits.Operations = append(edits.Operations, OperationEdit{Op: EditRemove, OperNumber: n})
			delta.OperationsRemoved++
		}
	}
	return edits, delta
}

func lineDiffers(a, b OrderLine) bool {
	flush := func(f Flushing) Flushing {
		if f == "" {
			return FlushManual
		}
		return f
	}
	return a.ItemID != b.ItemID || !a.QtyPer.Equal(b.QtyPer) || !a.SeriesSize.Equal(b.SeriesSize) || !a.ScrapPct.Equal(b.ScrapPct) ||
		a.RoundUp != b.RoundUp || !a.UnitCost.Equal(b.UnitCost) || flush(a.Flushing) != flush(b.Flushing)
}

func operationDiffers(a, b Operation) bool {
	return a.ResourceID != b.ResourceID || !a.SetupH.Equal(b.SetupH) || !a.RunHPer.Equal(b.RunHPer) || !a.ScrapPct.Equal(b.ScrapPct) ||
		!a.LaborRate.Equal(b.LaborRate) || !a.OverheadRate.Equal(b.OverheadRate) || a.Subcontract != b.Subcontract ||
		!a.SubcontractPrice.Equal(b.SubcontractPrice)
}

// ChangeCost is the cost of applying a change (BR-21.12): the delta of the
// planned cost plus the profile's surcharge. A negative delta still carries
// the fixed surcharge; the percentage applies to a positive delta only.
type ChangeCost struct {
	OldPlanned decimal.Decimal `json:"oldPlanned"`
	NewPlanned decimal.Decimal `json:"newPlanned"`
	Delta      decimal.Decimal `json:"delta"`
	Surcharge  decimal.Decimal `json:"surcharge"`
	Total      decimal.Decimal `json:"total"`
}

// PlannedTotal sums the amounts of planned cost rows.
func PlannedTotal(rows []CostRow) decimal.Decimal {
	sum := decimal.Zero
	for _, r := range rows {
		sum = sum.Add(r.Amount)
	}
	return sum
}

// ComputeChangeCost prices a change from the planned cost before and after it.
// Every amount is rounded to places (the currency's minor unit) and the total
// is the sum of the rounded delta and surcharge, so the parts add up as shown.
func ComputeChangeCost(before, after []CostRow, p OCMProfile, places int32) ChangeCost {
	old, next := PlannedTotal(before).Round(places), PlannedTotal(after).Round(places)
	delta := next.Sub(old)
	surcharge := p.SurchargeFixed
	if delta.IsPositive() {
		surcharge = surcharge.Add(delta.Mul(p.SurchargePct).Div(hundred))
	}
	surcharge = surcharge.Round(places)
	return ChangeCost{OldPlanned: old, NewPlanned: next, Delta: delta, Surcharge: surcharge, Total: delta.Add(surcharge)}
}

// AutoApplies reports whether a pending change applies by itself at the order's
// next status transition (FR-21.16): its profile is automatic, the order was
// not edited by hand (FR-21.18) and the command does not cancel the order.
func AutoApplies(mode OCMMode, orderEdited bool, c Command) bool {
	return mode == OCMAutomatic && !orderEdited && c != CmdCancel
}
