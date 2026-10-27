package domain

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

// Hierarchy types (FR-38.18). Only a Collective hierarchy lets a node collect
// costs that cannot be assigned to a lower object; a Reporting hierarchy only
// groups cost objects.
const (
	HierarchyReporting  = "Reporting"
	HierarchyCollective = "Collective"
)

// Distribution bases of a collecting node (FR-38.18). ActualOutput and
// StandardCost split in proportion to a quantity or value per child (actual
// output, or output valued at standard cost); FixedPercent gives each child a
// percentage of what the node holds.
const (
	BasisActualOutput = "ActualOutput"
	BasisStandardCost = "StandardCost"
	BasisFixedPercent = "FixedPercent"
)

// Errors returned by SplitHierarchy.
var (
	ErrPercentOverflow   = errors.New("fixed percentages total more than 100")
	ErrInvalidBasisValue = errors.New("a fixed percentage is outside 0-100")
)

// ValidHierarchyType reports whether t names a hierarchy type.
func ValidHierarchyType(t string) bool { return t == HierarchyReporting || t == HierarchyCollective }

// ValidDistributionBasis reports whether b names a supported basis.
func ValidDistributionBasis(b string) bool {
	switch b {
	case BasisActualOutput, BasisStandardCost, BasisFixedPercent:
		return true
	}
	return false
}

// HierarchyChild is a child node with its basis value: a quantity or value for
// ActualOutput and StandardCost, or a percentage in [0, 100] for FixedPercent.
type HierarchyChild struct {
	NodeID string          `json:"nodeId"`
	Basis  decimal.Decimal `json:"basis"`
}

// HierarchyShare is the amount one child receives.
type HierarchyShare struct {
	NodeID string          `json:"nodeId"`
	Basis  decimal.Decimal `json:"basis"`
	Amount decimal.Decimal `json:"amount"`
}

// HierarchySplit is the outcome of one distribution, shares in input order.
// Distributed + Residual = the amount split; the residual stays on the node.
type HierarchySplit struct {
	Shares      []HierarchyShare `json:"shares"`
	Distributed decimal.Decimal  `json:"distributed"`
	Residual    decimal.Decimal  `json:"residual"`
}

// SplitHierarchy distributes amount over children by basis at scale decimal
// places (the currency's minor unit).
//
// ActualOutput and StandardCost follow the top-down rule (BR-38.13): shares are
// proportional, rounded half away from zero, and the rounding residue goes to
// the largest basis, so the whole amount is distributed. When every basis is
// zero nothing can be distributed and the whole amount is the residual.
//
// FixedPercent gives each child amount × pct / 100, truncated to the minor
// unit. When the percentages total exactly 100 the truncation residue goes to
// the largest percentage (the first on a tie), so nothing remains. When they
// total less than 100 the unassigned part is the residual.
func SplitHierarchy(amount decimal.Decimal, basis string, children []HierarchyChild, scale int32) (HierarchySplit, error) {
	if scale < 0 || scale > 4 {
		return HierarchySplit{}, fmt.Errorf("scale %d is outside 0-4", scale)
	}
	if !amount.IsPositive() {
		return HierarchySplit{}, errors.New("the amount to distribute must be positive")
	}
	if !amount.Equal(amount.Round(scale)) {
		return HierarchySplit{}, fmt.Errorf("amount %s has more than %d decimal places", amount, scale)
	}
	if len(children) == 0 {
		return HierarchySplit{}, errors.New("a distribution needs at least one child node")
	}
	seen := make(map[string]bool, len(children))
	for _, c := range children {
		if c.NodeID == "" || seen[c.NodeID] {
			return HierarchySplit{}, errors.New("child nodes must be distinct and non-empty")
		}
		seen[c.NodeID] = true
		if c.Basis.IsNegative() {
			return HierarchySplit{}, fmt.Errorf("child %s has a negative basis", c.NodeID)
		}
	}
	shares := make([]HierarchyShare, len(children))
	for i, c := range children {
		shares[i] = HierarchyShare{NodeID: c.NodeID, Basis: c.Basis, Amount: decimal.Zero}
	}
	switch basis {
	case BasisFixedPercent:
		hundred := decimal.NewFromInt(100)
		total := decimal.Zero
		largest := 0
		for i, c := range children {
			if c.Basis.GreaterThan(hundred) {
				return HierarchySplit{}, ErrInvalidBasisValue
			}
			total = total.Add(c.Basis)
			if c.Basis.GreaterThan(children[largest].Basis) {
				largest = i
			}
		}
		if total.GreaterThan(hundred) {
			return HierarchySplit{}, ErrPercentOverflow
		}
		given := decimal.Zero
		for i, c := range children {
			shares[i].Amount = amount.Mul(c.Basis).Div(hundred).Truncate(scale)
			given = given.Add(shares[i].Amount)
		}
		if total.Equal(hundred) {
			shares[largest].Amount = shares[largest].Amount.Add(amount.Sub(given))
		}
	case BasisActualOutput, BasisStandardCost:
		segs := make([]Segment, len(children))
		for i, c := range children {
			segs[i] = Segment{ID: c.NodeID, Basis: c.Basis}
		}
		res, err := TopDown(amount, segs, scale)
		switch {
		case errors.Is(err, ErrNoTopDownBasis):
			// Every basis is zero: the whole amount stays on the node.
		case err != nil:
			return HierarchySplit{}, err
		default:
			// TopDown leaves zero-basis segments out of its shares; they keep 0.
			byID := make(map[string]decimal.Decimal, len(res.Shares))
			for _, sh := range res.Shares {
				byID[sh.SegmentID] = sh.Amount
			}
			for i := range shares {
				if a, ok := byID[shares[i].NodeID]; ok {
					shares[i].Amount = a
				}
			}
		}
	default:
		return HierarchySplit{}, fmt.Errorf("unknown distribution basis %q", basis)
	}
	distributed := decimal.Zero
	for _, s := range shares {
		distributed = distributed.Add(s.Amount)
	}
	residual := amount.Sub(distributed)
	if residual.IsNegative() {
		return HierarchySplit{}, ErrConservation
	}
	return HierarchySplit{Shares: shares, Distributed: distributed, Residual: residual}, nil
}
