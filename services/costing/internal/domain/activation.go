package domain

import (
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

// Activation and copy refusal codes (FR-20.5, BR-20.7). CST-ACT-001 and 002 are
// the PRD's; 003, 005, 006 and 007 and the CST-COPY codes are this service's.
// CST-ACT-004 (activation during an in-flight close) arrives with the close job.
const (
	CodeActivationBlocked = "CST-ACT-001"
	CodeZeroCost          = "CST-ACT-002"
	CodeNoPending         = "CST-ACT-003"
	CodeNotStandard       = "CST-ACT-005"
	CodeFutureEffective   = "CST-ACT-006"
	CodeBookValueMissing  = "CST-ACT-007"
	CodeBadPercent        = "CST-COPY-001"
)

// MaxUnitCost bounds a unit cost to numeric(20,6): at most 14 integer digits.
var MaxUnitCost = decimal.New(1, 14)

// VersionState is what activation needs to know about a costing version.
type VersionState struct {
	Type          string // STANDARD or PLANNED
	EffectiveFrom string // YYYY-MM-DD
	Blocked       bool
}

// CheckActivatable refuses a version that cannot be activated today (UTC date
// as YYYY-MM-DD): a blocked one (CST-ACT-001), a PLANNED one (CST-ACT-005:
// planned costs feed the wave 39 roll-up and never become the inventory
// standard) and one whose effective date is still ahead (CST-ACT-006: the
// scheduled activation job activates those on their date).
func CheckActivatable(v VersionState, today string) error {
	switch {
	case v.Blocked:
		return &Error{Code: CodeActivationBlocked, Detail: "costing version is blocked for activation"}
	case v.Type != "STANDARD":
		return &Error{Code: CodeNotStandard, Detail: "only a STANDARD costing version can be activated"}
	case v.EffectiveFrom > today:
		return &Error{Code: CodeFutureEffective, Detail: fmt.Sprintf("costing version is effective from %s; it cannot be activated before that date", v.EffectiveFrom)}
	}
	return nil
}

// PendingCost is one pending product x site cost with the active cost it
// replaces and the physical on-hand quantity the revaluation applies to
// (BR-20.7). When the product x site has no active cost yet, BookValue is the
// current inventory value of that on-hand, which the new standard replaces.
type PendingCost struct {
	ProductID    string
	SiteID       string
	NewCost      decimal.Decimal
	PreviousCost decimal.Decimal
	HasPrevious  bool
	OnHandQty    decimal.Decimal
	BookValue    *decimal.Decimal
}

// ActivationLine is one planned activation: the cost change and the revaluation
// of on-hand it causes, rounded half-even to the currency minor unit (BR-20.4).
// PreviousCost is meaningful only when HasPrevious; otherwise BookValue is the
// value replaced.
type ActivationLine struct {
	ProductID    string
	SiteID       string
	PreviousCost decimal.Decimal
	HasPrevious  bool
	BookValue    decimal.Decimal
	NewCost      decimal.Decimal
	OnHandQty    decimal.Decimal
	Revaluation  decimal.Decimal
}

// SiteTotal is the revaluation of one site (one revaluation voucher per site, FR-20.5).
type SiteTotal struct {
	SiteID string
	Amount decimal.Decimal
}

// ActivationPlan is the preview a caller sees before activation is committed.
type ActivationPlan struct {
	Lines []ActivationLine
	Sites []SiteTotal
	Total decimal.Decimal
}

// PlanActivation computes the revaluation of activating pending costs:
// (new − old) × on-hand per product x site, or new × on-hand − book value when
// there is no old standard. It refuses an empty set (CST-ACT-003), zero costs
// unless allowZero (CST-ACT-002) and stock without a book value where no old
// standard exists (CST-ACT-007). Lines come back sorted by site, then product,
// so the same input always yields the same plan; site totals are sums of the
// rounded lines, so each site voucher balances to the cent.
func PlanActivation(allowZero bool, scale int32, pending []PendingCost) (ActivationPlan, error) {
	if len(pending) == 0 {
		return ActivationPlan{}, &Error{Code: CodeNoPending, Detail: "costing version has no pending item costs"}
	}
	zeros, unvalued := 0, 0
	for _, p := range pending {
		if !p.NewCost.IsPositive() {
			zeros++
		}
		if !p.HasPrevious && !p.OnHandQty.IsZero() && p.BookValue == nil {
			unvalued++
		}
	}
	if zeros > 0 && !allowZero {
		return ActivationPlan{}, &Error{Code: CodeZeroCost, Detail: fmt.Sprintf("%d pending costs are zero and zero cost is not allowed", zeros)}
	}
	if unvalued > 0 {
		return ActivationPlan{}, &Error{Code: CodeBookValueMissing, Detail: fmt.Sprintf(
			"%d product and site pairs have stock but no active standard cost; give their current book value", unvalued)}
	}
	lines := make([]ActivationLine, 0, len(pending))
	for _, p := range pending {
		l := ActivationLine{ProductID: p.ProductID, SiteID: p.SiteID, HasPrevious: p.HasPrevious, NewCost: p.NewCost, OnHandQty: p.OnHandQty}
		if p.HasPrevious {
			l.PreviousCost = p.PreviousCost
			l.Revaluation = p.NewCost.Sub(p.PreviousCost).Mul(p.OnHandQty).RoundBank(scale)
		} else {
			if p.BookValue != nil {
				l.BookValue = *p.BookValue
			}
			l.Revaluation = p.NewCost.Mul(p.OnHandQty).Sub(l.BookValue).RoundBank(scale)
		}
		lines = append(lines, l)
	}
	sort.Slice(lines, func(i, j int) bool {
		if lines[i].SiteID != lines[j].SiteID {
			return lines[i].SiteID < lines[j].SiteID
		}
		return lines[i].ProductID < lines[j].ProductID
	})
	sites, total := SiteTotals(lines)
	return ActivationPlan{Lines: lines, Sites: sites, Total: total}, nil
}

// SiteTotals sums revaluation per site (sorted by site id) and overall.
func SiteTotals(lines []ActivationLine) ([]SiteTotal, decimal.Decimal) {
	bySite := map[string]decimal.Decimal{}
	total := decimal.Zero
	for _, l := range lines {
		bySite[l.SiteID] = bySite[l.SiteID].Add(l.Revaluation)
		total = total.Add(l.Revaluation)
	}
	out := make([]SiteTotal, 0, len(bySite))
	for s, a := range bySite {
		out = append(out, SiteTotal{SiteID: s, Amount: a})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SiteID < out[j].SiteID })
	return out, total
}

// TopDeltas returns up to n lines with the largest absolute revaluation, ties
// broken by site and product so the answer is stable.
func TopDeltas(lines []ActivationLine, n int) []ActivationLine {
	cp := append([]ActivationLine(nil), lines...)
	sort.SliceStable(cp, func(i, j int) bool {
		ai, aj := cp[i].Revaluation.Abs(), cp[j].Revaluation.Abs()
		if !ai.Equal(aj) {
			return ai.GreaterThan(aj)
		}
		if cp[i].SiteID != cp[j].SiteID {
			return cp[i].SiteID < cp[j].SiteID
		}
		return cp[i].ProductID < cp[j].ProductID
	})
	if len(cp) > n {
		cp = cp[:n]
	}
	return cp
}

// CopyCost applies a percent change to a cost (FR-20.5 copy with percent). The
// result is rounded half-even to six places, the storage scale of item costs,
// and must still fit numeric(20,6).
func CopyCost(cost decimal.Decimal, percent decimal.Decimal) (decimal.Decimal, error) {
	if percent.LessThanOrEqual(decimal.NewFromInt(-100)) {
		return decimal.Zero, &Error{Code: CodeBadPercent, Detail: "percent change must be greater than -100"}
	}
	factor := decimal.NewFromInt(1).Add(percent.Div(decimal.NewFromInt(100)))
	out := cost.Mul(factor).RoundBank(6)
	if out.GreaterThanOrEqual(MaxUnitCost) {
		return decimal.Zero, &Error{Code: CodeBadPercent, Detail: "percent change makes a unit cost larger than 14 integer digits"}
	}
	return out, nil
}
