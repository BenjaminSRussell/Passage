package domain

import (
	"fmt"
	"math/big"
	"math/rand/v2"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// ratSolve is an independent reference for the reciprocal step: it builds the
// shares as exact fractions basis / row total and solves (I − Sᵀ) x = c by
// Gauss–Jordan elimination over math/big rationals, with no rounding at all.
// It also returns, per center, the exact cost received from senders.
func ratSolve(t fataler, costs []decimal.Decimal, basis [][]decimal.Decimal) (x, received []*big.Rat) {
	t.Helper()
	n := len(costs)
	rat := func(d decimal.Decimal) *big.Rat {
		r, ok := new(big.Rat).SetString(d.String())
		if !ok {
			t.Fatalf("bad decimal %s", d)
		}
		return r
	}
	shares := make([][]*big.Rat, n)
	for j := range basis {
		shares[j] = make([]*big.Rat, n)
		total := new(big.Rat)
		for i, b := range basis[j] {
			if i != j {
				total.Add(total, rat(b))
			}
		}
		for i, b := range basis[j] {
			shares[j][i] = new(big.Rat)
			if i != j && total.Sign() > 0 {
				shares[j][i].Quo(rat(b), total)
			}
		}
	}
	a := make([][]*big.Rat, n)
	for i := range a {
		a[i] = make([]*big.Rat, n+1)
		for j := 0; j < n; j++ {
			a[i][j] = new(big.Rat).Neg(shares[j][i])
			if i == j {
				a[i][j].Add(a[i][j], big.NewRat(1, 1))
			}
		}
		a[i][n] = rat(costs[i])
	}
	for col := 0; col < n; col++ {
		piv := col
		for piv < n && a[piv][col].Sign() == 0 {
			piv++
		}
		if piv == n {
			t.Fatalf("reference system is singular at column %d", col)
		}
		a[col], a[piv] = a[piv], a[col]
		inv := new(big.Rat).Inv(a[col][col])
		for k := col; k <= n; k++ {
			a[col][k].Mul(a[col][k], inv)
		}
		for r := 0; r < n; r++ {
			if r == col || a[r][col].Sign() == 0 {
				continue
			}
			f := new(big.Rat).Set(a[r][col])
			for k := col; k <= n; k++ {
				a[r][k].Sub(a[r][k], new(big.Rat).Mul(f, a[col][k]))
			}
		}
	}
	x = make([]*big.Rat, n)
	for i := range x {
		x[i] = a[i][n]
	}
	received = make([]*big.Rat, n)
	for i := 0; i < n; i++ {
		received[i] = new(big.Rat)
		for j := 0; j < n; j++ {
			received[i].Add(received[i], new(big.Rat).Mul(x[j], shares[j][i]))
		}
	}
	return x, received
}

// absDiff is |d − r| as a float, for tolerance checks against the reference.
func absDiff(d decimal.Decimal, r *big.Rat) float64 {
	dr, _ := new(big.Rat).SetString(d.String())
	f, _ := new(big.Rat).Sub(dr, r).Float64()
	if f < 0 {
		return -f
	}
	return f
}

// shareOf turns a basis matrix into the solver's share matrix (row j / Σ row j).
func shareOf(basis [][]decimal.Decimal) [][]decimal.Decimal {
	out := make([][]decimal.Decimal, len(basis))
	for j, row := range basis {
		out[j] = make([]decimal.Decimal, len(row))
		total := decimal.Zero
		for i, b := range row {
			if i != j {
				total = total.Add(b)
			}
		}
		for i, b := range row {
			out[j][i] = decimal.Zero
			if i != j && total.IsPositive() {
				out[j][i] = b.Div(total).Round(solvePlaces)
			}
		}
	}
	return out
}

func ints(vs ...int64) []decimal.Decimal {
	out := make([]decimal.Decimal, len(vs))
	for i, v := range vs {
		out[i] = decimal.NewFromInt(v)
	}
	return out
}

// allocReciprocal10 is the alloc-reciprocal-10 fixture (PRD 7.38.9): seven
// service centers that all serve each other and three production centers.
func allocReciprocal10() Cycle {
	return Cycle{
		Centers: []string{"S1", "S2", "S3", "S4", "S5", "S6", "S7", "P1", "P2", "P3"},
		Costs: []decimal.Decimal{dec("12000.00"), dec("8500.50"), dec("4300.25"), dec("15000.00"), dec("2750.75"), dec("9999.99"), dec("6100.10"),
			dec("50000.00"), dec("0"), dec("1234.56")},
		Basis: [][]decimal.Decimal{
			ints(0, 2, 1, 0, 0, 1, 0, 3, 2, 1),
			ints(1, 0, 2, 1, 0, 0, 1, 2, 2, 1),
			ints(0, 1, 0, 2, 1, 0, 0, 1, 3, 2),
			ints(2, 0, 1, 0, 2, 1, 0, 2, 1, 1),
			ints(1, 1, 0, 1, 0, 2, 1, 1, 1, 2),
			ints(0, 2, 0, 1, 1, 0, 2, 2, 2, 0),
			ints(1, 0, 1, 0, 1, 1, 0, 1, 2, 3),
			ints(0, 0, 0, 0, 0, 0, 0, 0, 0, 0),
			ints(0, 0, 0, 0, 0, 0, 0, 0, 0, 0),
			ints(0, 0, 0, 0, 0, 0, 0, 0, 0, 0),
		},
	}
}

// Wave 38 proof, AC "allocation cycles converge and reconcile", on the
// alloc-reciprocal-10 fixture: both solver paths agree with the exact rational
// reference, the iterative path converges within the PRD bound, and on both
// paths the production centers take exactly the 58,651.59 the seven service
// centers bring in (12,000.00 + 8,500.50 + 4,300.25 + 15,000.00 + 2,750.75 +
// 9,999.99 + 6,100.10, summed by hand), each within a cent per sender of its
// exact share.
func TestAllocate_AllocReciprocal10Fixture(t *testing.T) {
	c := allocReciprocal10()
	exact, received := ratSolve(t, c.Costs, c.Basis)
	iterOpt := DefaultSolveOptions()
	iterOpt.ExactMaxN = 0
	for name, opt := range map[string]SolveOptions{"exact": DefaultSolveOptions(), "iterative": iterOpt} {
		t.Run(name, func(t *testing.T) {
			sol, err := SolveReciprocal(c.Costs, shareOf(c.Basis), opt)
			if err != nil {
				t.Fatalf("solve: %v", err)
			}
			// The exact path divides at decimal's 16-place precision, so it agrees
			// with the rational reference far below a cent but not to the last digit.
			tol := 1e-6
			if name == "iterative" {
				if sol.Iterations > DefaultMaxIter || sol.Residual.GreaterThan(dec("0.01")) {
					t.Fatalf("iterations %d residual %s, want <= 100 and <= 0.01", sol.Iterations, sol.Residual)
				}
				// Every service center leaks at least 1/3 of its total to production,
				// so the service error is ‖e_s‖₁ <= ‖r_s‖₁ / (1 − 2/3) <= 3 · n · residual
				// and a production center's error is at most its residual plus that.
				rf, _ := sol.Residual.Float64()
				tol = (3*float64(len(c.Centers))+1)*rf + 1e-6
			}
			for i, id := range c.Centers {
				if diff := absDiff(sol.Totals[i], exact[i]); diff > tol {
					t.Errorf("%s total %s differs from the exact %s by %g (tolerance %g)", id, sol.Totals[i], exact[i].FloatString(6), diff, tol)
				}
			}

			res, err := Allocate(c, opt)
			if err != nil {
				t.Fatalf("allocate: %v", err)
			}
			if !res.Converged {
				t.Fatalf("converged = false after %d iterations", res.Iterations)
			}
			if !res.TotalIn.Equal(dec("58651.59")) || !res.TotalOut.Equal(dec("58651.59")) {
				t.Fatalf("in %s out %s, want 58651.59 both", res.TotalIn, res.TotalOut)
			}
			for i, p := range res.Postings {
				if i < 7 && !p.Amount.Equal(c.Costs[i].Neg()) {
					t.Errorf("service center %s posting %s, want %s", p.CenterID, p.Amount, c.Costs[i].Neg())
				}
				if i >= 7 && absDiff(p.Amount, received[i]) > 0.07 {
					t.Errorf("production %s received %s, exact %s", p.CenterID, p.Amount, received[i].FloatString(4))
				}
			}
		})
	}
}

// genDominant draws a strictly diagonally dominant reciprocal cycle: 2 to 12
// service centers followed by 1 to 3 production centers. Each service center
// sends at least a third of its basis to production, so the column sums of the
// service block of Sᵀ are at most 2/3 and Gauss–Seidel contracts.
func genDominant(t *rapid.T) (Cycle, int) {
	ns := rapid.IntRange(2, 12).Draw(t, "services")
	np := rapid.IntRange(1, 3).Draw(t, "production")
	n := ns + np
	c := Cycle{Centers: make([]string, n), Costs: make([]decimal.Decimal, n), Basis: make([][]decimal.Decimal, n)}
	for j := 0; j < n; j++ {
		c.Centers[j] = fmt.Sprintf("C%02d", j)
		c.Costs[j] = decimal.New(rapid.Int64Range(0, 10_000_000).Draw(t, "cost"), -2)
		c.Basis[j] = make([]decimal.Decimal, n)
		for i := range c.Basis[j] {
			c.Basis[j][i] = decimal.Zero
		}
		if j >= ns {
			continue
		}
		var toServices int64
		for i := 0; i < ns; i++ {
			if i != j {
				w := rapid.Int64Range(0, 9).Draw(t, "w")
				toServices += w
				c.Basis[j][i] = decimal.NewFromInt(w)
			}
		}
		for i := ns; i < n; i++ {
			c.Basis[j][i] = decimal.NewFromInt(rapid.Int64Range(0, 9).Draw(t, "p"))
		}
		// Top up the first production center so production holds >= 1/3 of the row.
		c.Basis[j][ns] = c.Basis[j][ns].Add(decimal.NewFromInt(toServices/2 + 1))
	}
	return c, ns
}

// Property (PRD 7.38.9 convergence): on diagonally dominant cycles the
// Gauss–Seidel path reaches a residual <= 0.01 within 100 iterations, lands
// within the residual's error bound of the exact rational solution, the exact
// path matches the reference to 1e-6, and the step conserves cost to the cent.
func TestSolveReciprocal_ConvergesOnDiagonallyDominantCycles(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		c, ns := genDominant(t)
		exact, _ := ratSolve(t, c.Costs, c.Basis)
		shares := shareOf(c.Basis)

		ex, err := SolveReciprocal(c.Costs, shares, DefaultSolveOptions())
		if err != nil {
			t.Fatalf("exact: %v", err)
		}
		opt := DefaultSolveOptions()
		opt.ExactMaxN = 0
		it, err := SolveReciprocal(c.Costs, shares, opt)
		if err != nil {
			t.Fatalf("iterative did not converge: %v (iterations %d residual %s)", err, it.Iterations, it.Residual)
		}
		if it.Iterations > DefaultMaxIter || it.Residual.GreaterThan(dec("0.01")) {
			t.Fatalf("iterations %d residual %s", it.Iterations, it.Residual)
		}
		rf, _ := it.Residual.Float64()
		bound := (3*float64(len(c.Centers))+1)*rf + 1e-6
		for i := range c.Centers {
			if diff := absDiff(ex.Totals[i], exact[i]); diff > 1e-6 {
				t.Fatalf("exact path center %d off the reference by %g", i, diff)
			}
			if diff := absDiff(it.Totals[i], exact[i]); diff > bound {
				t.Fatalf("iterative center %d off the reference by %g (bound %g)", i, diff, bound)
			}
		}

		in := decimal.Zero
		for j := 0; j < ns; j++ {
			in = in.Add(c.Costs[j])
		}
		res, err := Allocate(c, opt)
		if err != nil || !res.Converged {
			t.Fatalf("allocate: %v converged=%v", err, res.Converged)
		}
		if !res.TotalIn.Equal(in) || !res.TotalOut.Equal(in) {
			t.Fatalf("in %s out %s, want %s", res.TotalIn, res.TotalOut, in)
		}
	})
}

// A 150-center cycle forced onto the iterative path converges inside the PRD
// iteration bound and conserves cost to the cent. The cycle is seeded so the
// run is deterministic; the expected total is summed from the drawn costs.
func TestAllocate_IterativeLargeCycleConvergesAndConserves(t *testing.T) {
	if testing.Short() {
		t.Skip("large cycle")
	}
	const ns, np = 150, 2
	n := ns + np
	r := rand.New(rand.NewPCG(38, 7))
	c := Cycle{Centers: make([]string, n), Costs: make([]decimal.Decimal, n), Basis: make([][]decimal.Decimal, n)}
	in := decimal.Zero
	for j := 0; j < n; j++ {
		c.Centers[j] = fmt.Sprintf("CC%03d", j)
		c.Costs[j] = decimal.New(r.Int64N(5_000_000), -2)
		c.Basis[j] = make([]decimal.Decimal, n)
		for i := range c.Basis[j] {
			c.Basis[j][i] = decimal.Zero
		}
		if j >= ns {
			continue
		}
		in = in.Add(c.Costs[j])
		var toServices int64
		for k := 0; k < 8; k++ {
			if i := r.IntN(ns); i != j {
				w := 1 + r.Int64N(9)
				toServices += w
				c.Basis[j][i] = c.Basis[j][i].Add(decimal.NewFromInt(w))
			}
		}
		c.Basis[j][ns+r.IntN(np)] = decimal.NewFromInt(toServices/2 + 1)
	}
	opt := DefaultSolveOptions()
	opt.ExactMaxN = 0
	res, err := Allocate(c, opt)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Converged || res.Iterations > DefaultMaxIter || res.Residual.GreaterThan(dec("0.01")) {
		t.Fatalf("converged=%v iterations=%d residual=%s", res.Converged, res.Iterations, res.Residual)
	}
	if !res.TotalIn.Equal(in) || !res.TotalOut.Equal(in) {
		t.Fatalf("in %s out %s, want %s", res.TotalIn, res.TotalOut, in)
	}
	for i := 0; i < ns; i++ {
		if !res.Postings[i].Amount.Equal(c.Costs[i].Neg()) {
			t.Fatalf("sender %s keeps %s", c.Centers[i], c.Costs[i].Add(res.Postings[i].Amount))
		}
	}
}
