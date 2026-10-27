package domain

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// AdjustmentType is a non-depreciation change to one book's balances posted
// through POST /assets/{id}/transactions (FR-37.9, FR-37.14).
type AdjustmentType string

const (
	AdjustmentAcquisition  AdjustmentType = "AcquisitionAdj"
	AdjustmentDepreciation AdjustmentType = "DepreciationAdj"
	AdjustmentRevaluation  AdjustmentType = "Revaluation"
	AdjustmentImpairment   AdjustmentType = "Impairment"
)

// CodeImpairmentLimited is the AST-0512 warning: a reversal was capped.
const CodeImpairmentLimited = "AST-0512"

// BookBalance is the projected state of one book that an adjustment acts on.
// Impairment is the cumulative impairment loss still recorded on the book and
// is part of Accumulated. Scheduled is the accumulated depreciation the book's
// schedule would have charged through its last depreciated period had it never
// been impaired; it bounds an impairment reversal (FR-37.9).
type BookBalance struct {
	Cost        decimal.Decimal
	Accumulated decimal.Decimal
	Impairment  decimal.Decimal
	Scheduled   decimal.Decimal
	Currency    string
}

// Adjustment is the effect of one adjustment on a book. Applied is the amount
// actually posted; it is smaller than requested only when a reversal is capped
// (Limited, AST-0512).
type Adjustment struct {
	CostDelta        decimal.Decimal
	AccumulatedDelta decimal.Decimal
	ImpairmentDelta  decimal.Decimal
	Applied          decimal.Decimal
	Limited          bool
}

// RequireOpenBook rejects postings to books that are sold or scrapped (BR-37.2).
func RequireOpenBook(status string) error {
	if status != string(StatusOpen) {
		return fail(CodeAssetState, "book is "+status+"; only an Open book takes postings")
	}
	return nil
}

// ReversalCap is the most impairment that can be reversed: the reversal may not
// lift the carrying amount above what it would have been without impairment
// (IAS 36.117), and never by more than the impairment still recorded.
func ReversalCap(b BookBalance) decimal.Decimal {
	withoutImpairment := b.Accumulated.Sub(b.Impairment)
	if b.Scheduled.GreaterThan(withoutImpairment) {
		withoutImpairment = b.Scheduled
	}
	limit := b.Accumulated.Sub(withoutImpairment)
	if limit.GreaterThan(b.Impairment) {
		limit = b.Impairment
	}
	if limit.IsNegative() {
		return decimal.Zero
	}
	return limit
}

// Adjust computes the balance effect of one adjustment. Every kind keeps
// 0 ≤ accumulated ≤ cost, so NBV never goes negative (PRD 7.37.4 invariant).
//   - AcquisitionAdj changes cost (an addition, or a credit note when negative).
//   - DepreciationAdj changes accumulated depreciation.
//   - Revaluation changes cost (IAS 16 revaluation model; the OCI or P&L
//     side is the GL posting).
//   - Impairment is a write-down when positive and a reversal when negative;
//     a reversal is capped by ReversalCap and reported as Limited.
func Adjust(kind AdjustmentType, b BookBalance, amount decimal.Decimal) (Adjustment, error) {
	if len(b.Currency) != 3 {
		return Adjustment{}, fail(CodeInvalidAdjustment, "currency must be an ISO 4217 code")
	}
	if amount.IsZero() {
		return Adjustment{}, fail(CodeInvalidAdjustment, "adjustment amount must be non-zero")
	}
	if !amount.Equal(amount.Round(money.MinorUnits(b.Currency))) {
		return Adjustment{}, fail(CodeInvalidAdjustment, "amount has more decimals than the currency minor unit")
	}
	switch kind {
	case AdjustmentAcquisition, AdjustmentRevaluation:
		if b.Cost.Add(amount).LessThan(b.Accumulated) {
			return Adjustment{}, fail(CodeInvalidAdjustment, "the adjustment would make cost lower than accumulated depreciation (negative NBV)")
		}
		return Adjustment{CostDelta: amount, Applied: amount}, nil
	case AdjustmentDepreciation:
		next := b.Accumulated.Add(amount)
		if next.IsNegative() || next.GreaterThan(b.Cost) {
			return Adjustment{}, fail(CodeInvalidAdjustment, "accumulated depreciation must stay between 0 and cost")
		}
		if next.LessThan(b.Impairment) {
			return Adjustment{}, fail(CodeInvalidAdjustment, "accumulated depreciation cannot drop below the recorded impairment; reverse the impairment instead")
		}
		return Adjustment{AccumulatedDelta: amount, Applied: amount}, nil
	case AdjustmentImpairment:
		if amount.IsPositive() {
			if b.Cost.Sub(b.Accumulated).LessThan(amount) {
				return Adjustment{}, fail(CodeInvalidAdjustment, "impairment would make NBV negative")
			}
			return Adjustment{AccumulatedDelta: amount, ImpairmentDelta: amount, Applied: amount}, nil
		}
		limit := ReversalCap(b)
		if !limit.IsPositive() {
			return Adjustment{}, fail(CodeInvalidAdjustment, "no impairment can be reversed on this book")
		}
		applied, limited := amount, false
		if amount.Neg().GreaterThan(limit) {
			applied, limited = limit.Neg(), true
		}
		return Adjustment{AccumulatedDelta: applied, ImpairmentDelta: applied, Applied: applied, Limited: limited}, nil
	}
	return Adjustment{}, fail(CodeInvalidAdjustment, "unknown adjustment type "+string(kind))
}
