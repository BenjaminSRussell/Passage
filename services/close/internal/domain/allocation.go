package domain

import (
	"github.com/shopspring/decimal"
)

const workingPrecision = 8

type Share struct {
	Key    string
	Weight decimal.Decimal
}

type Part struct {
	Key    string
	Amount decimal.Decimal
}

func Allocate(source decimal.Decimal, shares []Share, scale int32) ([]Part, error) {
	if len(shares) == 0 {
		return nil, fail(CodeValidation, "at least one destination is required")
	}
	total := decimal.Zero
	largest := 0
	for i, s := range shares {
		if s.Weight.IsNegative() {
			return nil, fail(CodeValidation, "destination "+s.Key+" has a negative basis")
		}
		total = total.Add(s.Weight)
		if s.Weight.GreaterThan(shares[largest].Weight) {
			largest = i
		}
	}
	if total.IsZero() {
		return nil, fail(CodeZeroBasis, "every destination has a zero basis")
	}
	parts := make([]Part, len(shares))
	assigned := decimal.Zero
	for i, s := range shares {
		amount := source.Mul(s.Weight).DivRound(total, scale)
		parts[i] = Part{Key: s.Key, Amount: amount}
		assigned = assigned.Add(amount)
	}
	parts[largest].Amount = parts[largest].Amount.Add(source.Sub(assigned))
	return parts, nil
}

func Reciprocal(direct []decimal.Decimal, matrix [][]decimal.Decimal, scale int32, maxIterations int) ([]decimal.Decimal, error) {
	n := len(direct)
	if n == 0 || len(matrix) != n {
		return nil, fail(CodeValidation, "matrix must be square and match the direct costs")
	}
	for _, row := range matrix {
		if len(row) != n {
			return nil, fail(CodeValidation, "matrix must be square and match the direct costs")
		}
	}
	tolerance := decimal.New(1, -(scale + 2))
	totals := append([]decimal.Decimal(nil), direct...)
	for iteration := 0; iteration < maxIterations; iteration++ {
		largestChange := decimal.Zero
		for i := 0; i < n; i++ {
			next := direct[i]
			for j := 0; j < n; j++ {
				if j != i {
					next = next.Add(totals[j].Mul(matrix[j][i]))
				}
			}
			next = next.Round(scale + workingPrecision)
			if change := next.Sub(totals[i]).Abs(); change.GreaterThan(largestChange) {
				largestChange = change
			}
			totals[i] = next
		}
		if largestChange.LessThan(tolerance) {
			rounded := make([]decimal.Decimal, n)
			for i, t := range totals {
				rounded[i] = t.Round(scale)
			}
			return rounded, nil
		}
	}
	return nil, fail(CodeNoConvergence, "reciprocal allocation did not converge")
}
