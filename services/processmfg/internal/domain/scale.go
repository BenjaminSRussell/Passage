package domain

import "github.com/shopspring/decimal"

// Precisions from PRD 4.24.4: quantities at UoM precision (6), scale factor 9,
// money-like amounts 4.
const (
	QtyPrecision    = 6
	FactorPrecision = 9
	AmountPrecision = 4
)

// LineType is the FR-24.1 line_type enum.
type LineType string

const (
	LineIngredient       LineType = "ingredient"
	LineActiveIngredient LineType = "active-ingredient"
	LinePhantom          LineType = "phantom"
	LineCoProduct        LineType = "co-product"
	LineByProduct        LineType = "by-product"
)

// ValidLineType reports whether t is one of the FR-24.1 line types.
func ValidLineType(t LineType) bool {
	switch t {
	case LineIngredient, LineActiveIngredient, LinePhantom, LineCoProduct, LineByProduct:
		return true
	}
	return false
}

// percentTolerance is the FR-24.1 tolerance for percent-controlled lines.
const percentTolerance = "0.01"

// oneHundred returns 100 (percent base). A function, because package-level
// values other than constants are not allowed (CONVENTIONS: no globals).
func oneHundred() decimal.Decimal { return decimal.NewFromInt(100) }

// FormulaLine is one line of a formula for the formula size. Scalable lines
// scale with the batch; percent-controlled lines take Percent of the batch size.
type FormulaLine struct {
	LineNumber        int
	ItemID            string
	Type              LineType
	Qty               decimal.Decimal
	Percent           decimal.Decimal
	Scalable          bool
	PercentControlled bool
}

// Formula is a formula valid for Size units of batch.
type Formula struct {
	Size  decimal.Decimal
	Lines []FormulaLine
}

// ScaledLine is a formula line at a batch size.
type ScaledLine struct {
	LineNumber int
	ItemID     string
	Type       LineType
	FormulaQty decimal.Decimal
	Qty        decimal.Decimal
}

// ScaleResult is the scaled formula with its factor (batch size / formula size).
type ScaleResult struct {
	ScaleFactor decimal.Decimal
	Lines       []ScaledLine
}

// MaxFormulaLines bounds one formula so a request cannot allocate without limit.
const MaxFormulaLines = 200

// IsOutput reports whether a line type is produced by the batch (co- and
// by-products) rather than consumed by it.
func IsOutput(t LineType) bool { return t == LineCoProduct || t == LineByProduct }

// ValidateFormula enforces FR-24.1: a positive size, 1 to MaxFormulaLines lines
// with known types, unique positive line numbers, non-negative quantities,
// percentages from 0 to 100, and the percent of percent-controlled scalable
// lines summing to 100.0000 within ±0.01 (PMF-0001).
func ValidateFormula(f Formula) error {
	if !f.Size.IsPositive() {
		return errorf(CodeInvalid, "formula size must be positive")
	}
	if len(f.Lines) == 0 || len(f.Lines) > MaxFormulaLines {
		return errorf(CodeInvalid, "a formula needs 1 to %d lines", MaxFormulaLines)
	}
	seen := make(map[int]bool, len(f.Lines))
	sum, found := decimal.Zero, false
	for _, l := range f.Lines {
		switch {
		case l.LineNumber < 1:
			return errorf(CodeInvalid, "line numbers must be positive")
		case seen[l.LineNumber]:
			return errorf(CodeInvalid, "line number %d is used twice", l.LineNumber)
		case !ValidLineType(l.Type):
			return errorf(CodeInvalid, "line %d has unknown type %q", l.LineNumber, l.Type)
		case l.Qty.IsNegative():
			return errorf(CodeInvalid, "line %d quantity must not be negative", l.LineNumber)
		case l.Percent.IsNegative() || l.Percent.GreaterThan(oneHundred()):
			return errorf(CodeInvalid, "line %d percent must be between 0 and 100", l.LineNumber)
		case l.PercentControlled && IsOutput(l.Type):
			return errorf(CodeInvalid, "line %d: co- and by-product lines cannot be percent-controlled", l.LineNumber)
		}
		seen[l.LineNumber] = true
		if l.PercentControlled && l.Scalable {
			sum = sum.Add(l.Percent)
			found = true
		}
	}
	if found && sum.Sub(oneHundred()).Abs().GreaterThan(decimal.RequireFromString(percentTolerance)) {
		return errorf(CodePercentSum, "percent-controlled lines sum to %s%%, not 100%%", sum.StringFixed(4))
	}
	return nil
}

// Scale implements BR-24.1. Scalable lines are formula qty × factor (percent
// lines: batch × percent / 100); non-scalable lines are unchanged. Each value
// is rounded to UoM precision. The rounding residual of the scalable input
// lines (ingredients, active ingredients, phantoms) goes to the largest of
// them, so that group sums exactly to its unrounded total (the batch size when
// every scalable input is percent-controlled). Co- and by-product lines are
// outputs: they scale with the batch but take no share of the input residual.
func Scale(f Formula, batchSize decimal.Decimal) (ScaleResult, error) {
	if err := ValidateFormula(f); err != nil {
		return ScaleResult{}, err
	}
	if !batchSize.IsPositive() {
		return ScaleResult{}, errorf(CodeInvalid, "batch size must be positive")
	}
	factor := batchSize.DivRound(f.Size, FactorPrecision)
	res := ScaleResult{ScaleFactor: factor, Lines: make([]ScaledLine, len(f.Lines))}
	target, rounded := decimal.Zero, decimal.Zero
	largest := -1
	for i, l := range f.Lines {
		res.Lines[i] = ScaledLine{LineNumber: l.LineNumber, ItemID: l.ItemID, Type: l.Type, FormulaQty: l.Qty, Qty: l.Qty}
		if !l.Scalable {
			continue
		}
		// The exact factor (not the 9-place rounded one) keeps Σ exact.
		exact := l.Qty.Mul(batchSize).DivRound(f.Size, 24)
		if l.PercentControlled {
			exact = batchSize.Mul(l.Percent).Div(oneHundred())
		}
		q := exact.Round(QtyPrecision)
		res.Lines[i].Qty = q
		if IsOutput(l.Type) {
			continue
		}
		target = target.Add(exact)
		rounded = rounded.Add(q)
		if largest < 0 || q.GreaterThan(res.Lines[largest].Qty) {
			largest = i
		}
	}
	if largest >= 0 {
		res.Lines[largest].Qty = res.Lines[largest].Qty.Add(target.Round(QtyPrecision).Sub(rounded))
	}
	return res, nil
}
