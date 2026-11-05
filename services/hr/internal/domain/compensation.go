package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

const (
	CodeCompOutsideGuideline = "HCM-CMP-001"
	CodeCompOverBudget       = "HCM-CMP-002"
	CodeCompMatrix           = "HCM-CMP-003"
	// CodeCompSoD is a cycle approval by someone who may not approve it: the
	// cycle's creator, a submitter of one of its worksheets, or a worker whose
	// own pay change is on the cycle.
	CodeCompSoD = "HCM-CMP-SOD"
)

// CycleApproval is what the segregation-of-duties check needs about a cycle:
// who created it, who submitted its worksheets, and whether the approver is a
// worker, or the proposing manager, on one of the submitted rows.
type CycleApproval struct {
	Approver   string
	Creator    string
	Submitters []string
	OnCycle    bool
}

// CheckCycleApprover enforces segregation of duties on a compensation cycle
// approval: the approver must be a named user who did not create the cycle,
// did not submit any of its worksheets, and is neither a worker nor the
// proposing manager on any submitted row.
func CheckCycleApprover(a CycleApproval) error {
	switch {
	case a.Approver == "":
		return &Error{Code: CodeCompSoD, Detail: "a compensation cycle is approved by a named user"}
	case a.Approver == a.Creator:
		return &Error{Code: CodeCompSoD, Detail: "the user who created the cycle cannot approve it"}
	case a.OnCycle:
		return &Error{Code: CodeCompSoD, Detail: "a worker or proposing manager on the cycle cannot approve it"}
	}
	for _, s := range a.Submitters {
		if s == a.Approver {
			return &Error{Code: CodeCompSoD, Detail: "a user who submitted a worksheet on the cycle cannot approve it"}
		}
	}
	return nil
}

var (
	hundred       = decimal.NewFromInt(100)
	maxCompaRatio = decimal.NewFromInt(100000)
)

type GuidelineRow struct {
	Rating   int             `json:"rating"`
	CompaMin decimal.Decimal `json:"compaMin"`
	CompaMax decimal.Decimal `json:"compaMax"`
	PctMin   decimal.Decimal `json:"pctMin"`
	PctMax   decimal.Decimal `json:"pctMax"`
}

func ValidateMatrix(rows []GuidelineRow) error {
	if len(rows) == 0 {
		return &Error{Code: CodeCompMatrix, Detail: "the guideline matrix needs at least one row"}
	}
	for i, r := range rows {
		switch {
		case r.Rating < 1 || r.Rating > 5:
			return &Error{Code: CodeCompMatrix, Detail: fmt.Sprintf("row %d: rating must be 1 to 5", i)}
		case r.CompaMin.IsNegative() || !r.CompaMax.GreaterThan(r.CompaMin):
			return &Error{Code: CodeCompMatrix, Detail: fmt.Sprintf("row %d: compaMax must exceed compaMin, which cannot be negative", i)}
		case r.PctMax.LessThan(r.PctMin) || r.PctMin.LessThan(decimal.NewFromInt(-100)) || r.PctMax.GreaterThan(hundred):
			return &Error{Code: CodeCompMatrix, Detail: fmt.Sprintf("row %d: pctMin to pctMax must be an ordered range within -100 to 100", i)}
		}
		for j := 0; j < i; j++ {
			o := rows[j]
			if o.Rating == r.Rating && r.CompaMin.LessThan(o.CompaMax) && o.CompaMin.LessThan(r.CompaMax) {
				return &Error{Code: CodeCompMatrix, Detail: fmt.Sprintf("rows %d and %d overlap for rating %d", j, i, r.Rating)}
			}
		}
	}
	return nil
}

func CompaRatio(annualPay, rangeMin, rangeMax decimal.Decimal) (decimal.Decimal, bool) {
	mid := rangeMin.Add(rangeMax).Div(decimal.NewFromInt(2))
	if !mid.IsPositive() {
		return decimal.Zero, false
	}
	ratio := annualPay.Div(mid).Round(2)
	if ratio.GreaterThan(maxCompaRatio) {
		return decimal.Zero, false
	}
	return ratio, true
}

func GuidelineFor(rows []GuidelineRow, rating int, compa decimal.Decimal) (GuidelineRow, bool) {
	for _, r := range rows {
		if r.Rating == rating && !compa.LessThan(r.CompaMin) && compa.LessThan(r.CompaMax) {
			return r, true
		}
	}
	return GuidelineRow{}, false
}

func OutsideGuideline(pct decimal.Decimal, cell GuidelineRow, found bool) bool {
	return !found || pct.LessThan(cell.PctMin) || pct.GreaterThan(cell.PctMax)
}

func ProposedAmount(pay, pct decimal.Decimal, places int32) decimal.Decimal {
	return pay.Mul(pct).Div(hundred).Round(places)
}

func CheckDepartmentBudget(proposed, budget decimal.Decimal, allowOver bool, department string) error {
	if allowOver || !proposed.GreaterThan(budget) {
		return nil
	}
	return &Error{Code: CodeCompOverBudget, Detail: fmt.Sprintf("proposals %s exceed budget %s for department %s", proposed.StringFixed(2), budget.StringFixed(2), department)}
}
