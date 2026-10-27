// Package domain holds the pure cost accounting rules: reciprocal allocation,
// primary element mapping and cost object validation. It performs no I/O.
package domain

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

// Solver defaults from the PRD parameter table (alloc.exactSolveMaxN, alloc.maxIterations).
const (
	DefaultExactMaxN = 2000
	DefaultMaxIter   = 100
	solvePlaces      = 24
	iterTolerance    = "0.01"
)

var (
	// ErrNotConverged is CO-0611: the iterative solve did not reach the residual bound.
	ErrNotConverged = errors.New("allocation did not converge")
	// ErrSingular means the cycle is closed: no center leaks cost out of the loop.
	ErrSingular = errors.New("allocation system is singular")
)

// SolveOptions selects the solver. ExactMaxN = 0 forces the iterative path.
type SolveOptions struct {
	ExactMaxN int
	MaxIter   int
	Tolerance decimal.Decimal
}

// DefaultSolveOptions returns the PRD defaults.
func DefaultSolveOptions() SolveOptions {
	return SolveOptions{ExactMaxN: DefaultExactMaxN, MaxIter: DefaultMaxIter, Tolerance: decimal.RequireFromString(iterTolerance)}
}

// Solution is the total cost each center carries after the cycle (its own
// cost plus everything received), with the solver trace.
type Solution struct {
	Totals     []decimal.Decimal
	Iterations int
	Residual   decimal.Decimal
	Method     string
}

// SolveReciprocal solves x = c + Sᵀx, where shares[j][i] is the fraction of
// sender j's total that flows to receiver i and each shares[j][j] is zero.
// Small systems are solved exactly by LU with partial pivoting in decimal
// arithmetic; larger ones by Gauss–Seidel iteration (FR-38.7).
func SolveReciprocal(costs []decimal.Decimal, shares [][]decimal.Decimal, opt SolveOptions) (Solution, error) {
	n := len(costs)
	if len(shares) != n {
		return Solution{}, fmt.Errorf("shares has %d rows for %d centers", len(shares), n)
	}
	for j, row := range shares {
		if len(row) != n {
			return Solution{}, fmt.Errorf("shares row %d has %d columns for %d centers", j, len(row), n)
		}
	}
	if n <= opt.ExactMaxN {
		return solveExact(costs, shares)
	}
	return solveIterative(costs, shares, opt)
}

func round(d decimal.Decimal) decimal.Decimal { return d.Round(solvePlaces) }

// solveExact performs Gaussian elimination with partial pivoting on the
// augmented system (I − Sᵀ | c).
func solveExact(costs []decimal.Decimal, shares [][]decimal.Decimal) (Solution, error) {
	n := len(costs)
	a := make([][]decimal.Decimal, n)
	for i := range a {
		a[i] = make([]decimal.Decimal, n+1)
		for j := 0; j < n; j++ {
			v := decimal.Zero
			if i == j {
				v = decimal.NewFromInt(1)
			}
			a[i][j] = v.Sub(shares[j][i])
		}
		a[i][n] = costs[i]
	}
	for col := 0; col < n; col++ {
		piv := col
		for r := col + 1; r < n; r++ {
			if a[r][col].Abs().GreaterThan(a[piv][col].Abs()) {
				piv = r
			}
		}
		if a[piv][col].IsZero() {
			return Solution{}, ErrSingular
		}
		a[col], a[piv] = a[piv], a[col]
		for r := col + 1; r < n; r++ {
			f := round(a[r][col].Div(a[col][col]))
			for k := col; k <= n; k++ {
				a[r][k] = round(a[r][k].Sub(f.Mul(a[col][k])))
			}
		}
	}
	x := make([]decimal.Decimal, n)
	for i := n - 1; i >= 0; i-- {
		s := a[i][n]
		for k := i + 1; k < n; k++ {
			s = s.Sub(a[i][k].Mul(x[k]))
		}
		x[i] = round(s.Div(a[i][i]))
	}
	return Solution{Totals: x, Method: "exact-lu"}, nil
}

// solveIterative runs Gauss–Seidel sweeps until the largest fixed-point
// residual on any center is within tolerance (BR-38.6).
func solveIterative(costs []decimal.Decimal, shares [][]decimal.Decimal, opt SolveOptions) (Solution, error) {
	n := len(costs)
	x := make([]decimal.Decimal, n)
	copy(x, costs)
	tol := opt.Tolerance
	if tol.IsZero() {
		tol = decimal.RequireFromString(iterTolerance)
	}
	var residual decimal.Decimal
	for it := 1; it <= opt.MaxIter; it++ {
		for i := 0; i < n; i++ {
			s := costs[i]
			for j := 0; j < n; j++ {
				s = s.Add(shares[j][i].Mul(x[j]))
			}
			x[i] = round(s)
		}
		residual = decimal.Zero
		for i := 0; i < n; i++ {
			s := costs[i]
			for j := 0; j < n; j++ {
				s = s.Add(shares[j][i].Mul(x[j]))
			}
			if d := round(s.Sub(x[i])).Abs(); d.GreaterThan(residual) {
				residual = d
			}
		}
		if residual.LessThanOrEqual(tol) {
			return Solution{Totals: x, Iterations: it, Residual: residual, Method: "gauss-seidel"}, nil
		}
	}
	return Solution{Totals: x, Iterations: opt.MaxIter, Residual: residual, Method: "gauss-seidel"}, ErrNotConverged
}
