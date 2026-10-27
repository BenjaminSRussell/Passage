package domain

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

// ErrNoTopDownBasis means no segment carries a positive reference basis, so the
// amount has no receiver and the run is rejected.
var ErrNoTopDownBasis = errors.New("top-down distribution needs at least one segment with a positive basis")

// Segment is one detailed receiver of a top-down distribution (FR-38.19) with
// its reference basis (revenue, quantity or any measure of the reference period).
type Segment struct {
	ID    string
	Basis decimal.Decimal
}

// Share is the amount one segment receives from the aggregated source.
type Share struct {
	SegmentID string          `json:"segmentId"`
	Basis     decimal.Decimal `json:"basis"`
	Amount    decimal.Decimal `json:"amount"`
}

// TopDownResult lists the shares in input order. Segments with a zero basis
// receive nothing and are reported in Excluded.
type TopDownResult struct {
	Shares   []Share         `json:"shares"`
	Excluded []string        `json:"excluded"`
	Total    decimal.Decimal `json:"total"`
}

// TopDown distributes amount to segments in proportion to their basis (FR-38.19).
// Each share is rounded half away from zero to scale decimal places (the
// currency's minor unit); the rounding residual goes to the segment with the
// largest basis, the first one on a tie, so Σ shares = amount to the cent
// (BR-38.13).
func TopDown(amount decimal.Decimal, segments []Segment, scale int32) (TopDownResult, error) {
	if scale < 0 || scale > 4 {
		return TopDownResult{}, fmt.Errorf("scale %d is outside 0-4", scale)
	}
	if !amount.Equal(amount.Round(scale)) {
		return TopDownResult{}, fmt.Errorf("amount %s has more than %d decimal places", amount, scale)
	}
	if len(segments) == 0 {
		return TopDownResult{}, errors.New("top-down distribution needs at least one segment")
	}
	seen := make(map[string]bool, len(segments))
	total := decimal.Zero
	largest := -1
	for i, s := range segments {
		if s.ID == "" || seen[s.ID] {
			return TopDownResult{}, errors.New("segment ids must be distinct and non-empty")
		}
		seen[s.ID] = true
		if s.Basis.Sign() < 0 {
			return TopDownResult{}, fmt.Errorf("segment %s has a negative basis", s.ID)
		}
		if s.Basis.Sign() == 0 {
			continue
		}
		total = total.Add(s.Basis)
		if largest < 0 || s.Basis.GreaterThan(segments[largest].Basis) {
			largest = i
		}
	}
	if largest < 0 {
		return TopDownResult{}, ErrNoTopDownBasis
	}

	amounts := make([]decimal.Decimal, len(segments))
	others := decimal.Zero
	for i, s := range segments {
		if s.Basis.Sign() == 0 || i == largest {
			continue
		}
		// Multiply before dividing and round once, half away from zero, at the minor unit.
		amounts[i] = amount.Mul(s.Basis).DivRound(total, scale)
		others = others.Add(amounts[i])
	}
	amounts[largest] = amount.Sub(others)

	res := TopDownResult{Shares: []Share{}, Excluded: []string{}, Total: decimal.Zero}
	for i, s := range segments {
		if s.Basis.Sign() == 0 {
			res.Excluded = append(res.Excluded, s.ID)
			continue
		}
		res.Shares = append(res.Shares, Share{SegmentID: s.ID, Basis: s.Basis, Amount: amounts[i]})
		res.Total = res.Total.Add(amounts[i])
	}
	if !res.Total.Equal(amount) {
		return TopDownResult{}, ErrConservation
	}
	return res, nil
}
