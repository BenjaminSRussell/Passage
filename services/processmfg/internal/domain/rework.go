package domain

import "github.com/shopspring/decimal"

// Variance types a rework order posts its cost as (FR-24.18, w21 FR-21.20).
const (
	VarianceRework      = "rework"
	VarianceCapitalized = "capitalized"
)

// ReworkParams are the rework parameters of FR-24.18 (AX "Rework" parameters).
type ReworkParams struct {
	KeepBatchNumber     bool
	RequireQualityOrder bool
	OutputDisposition   string
	Capitalize          bool
}

// DefaultReworkParams returns the FR-24.18 defaults: keep the batch number,
// require a quality order, hold the output in quarantine until quality release
// and post the cost as a rework variance.
func DefaultReworkParams() ReworkParams {
	return ReworkParams{KeepBatchNumber: true, RequireQualityOrder: true, OutputDisposition: DispositionQuarantine}
}

// ReworkPlan is what a rework batch order records: the source batch (or the
// reworked part of it) as its first ingredient line, the output batch (the
// source batch number when it is kept, empty when a new number is drawn at
// report as finished), the output disposition, whether a quality order is
// needed, the variance type of the rework cost, and the genealogy parents.
type ReworkPlan struct {
	SourceBatchID        string
	InputQty             decimal.Decimal
	OutputBatchID        string
	OutputQty            decimal.Decimal
	OutputDisposition    string
	QualityOrderRequired bool
	VarianceType         string
	Parents              []string
}

// PlanRework checks a rework order against FR-24.18 and returns its plan. The
// reworked quantity must be positive and at most the batch quantity (the
// whole batch or part of it); the output quantity must be positive. The output
// disposition must be available, on-hold or quarantine, and it cannot be
// available while a quality order is required, because release comes from
// quality. The source disposition is not restricted: rework is how blocked
// batches are recovered. All refusals are COM-0001.
func PlanRework(source Batch, inputQty, outputQty decimal.Decimal, p ReworkParams) (ReworkPlan, error) {
	if !source.Qty.IsPositive() {
		return ReworkPlan{}, errorf(CodeInvalid, "batch %s has no quantity to rework", source.ID)
	}
	if !inputQty.IsPositive() || inputQty.GreaterThan(source.Qty) {
		return ReworkPlan{}, errorf(CodeInvalid, "rework quantity must be positive and at most the batch quantity %s", source.Qty)
	}
	if !outputQty.IsPositive() {
		return ReworkPlan{}, errorf(CodeInvalid, "rework output quantity must be positive")
	}
	switch p.OutputDisposition {
	case DispositionAvailable, DispositionOnHold, DispositionQuarantine:
	default:
		return ReworkPlan{}, errorf(CodeInvalid, "rework output disposition must be available, on-hold or quarantine, not %q", p.OutputDisposition)
	}
	if p.RequireQualityOrder && p.OutputDisposition == DispositionAvailable {
		return ReworkPlan{}, errorf(CodeInvalid, "rework output cannot be available while a quality order is required")
	}
	plan := ReworkPlan{
		SourceBatchID:        source.ID,
		InputQty:             inputQty,
		OutputQty:            outputQty,
		OutputDisposition:    p.OutputDisposition,
		QualityOrderRequired: p.RequireQualityOrder,
		VarianceType:         VarianceRework,
		Parents:              []string{source.ID},
	}
	if p.KeepBatchNumber {
		plan.OutputBatchID = source.ID
	}
	if p.Capitalize {
		plan.VarianceType = VarianceCapitalized
	}
	return plan, nil
}
