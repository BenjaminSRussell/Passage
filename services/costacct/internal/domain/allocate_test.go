package domain

import (
	"errors"
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestAllocate_ReciprocalAnalytic(t *testing.T) {
	// A sends everything to B. B splits 50/50 between A and C. C is a sink.
	// x_A = 100 + 0.5 x_B and x_B = x_A give x_A = x_B = 200 and x_C = 100.
	cycle := Cycle{
		Centers: []string{"A", "B", "C"},
		Costs:   []decimal.Decimal{dec("100"), dec("0"), dec("0")},
		Basis: [][]decimal.Decimal{
			{dec("0"), dec("1"), dec("0")},
			{dec("1"), dec("0"), dec("1")},
			{dec("0"), dec("0"), dec("0")},
		},
	}
	res, err := Allocate(cycle, DefaultSolveOptions())
	if err != nil {
		t.Fatal(err)
	}
	wantFlows := map[string]string{"A>B": "200.00", "B>A": "100.00", "B>C": "100.00"}
	for _, f := range res.Flows {
		key := f.From + ">" + f.To
		if want, ok := wantFlows[key]; !ok || !f.Amount.Equal(dec(want)) {
			t.Errorf("flow %s = %s, want %s", key, f.Amount, wantFlows[key])
		}
	}
	if len(res.Flows) != len(wantFlows) {
		t.Errorf("got %d flows, want %d", len(res.Flows), len(wantFlows))
	}
	wantPost := map[string]string{"A": "-100.00", "B": "0.00", "C": "100.00"}
	for _, p := range res.Postings {
		if !p.Amount.Equal(dec(wantPost[p.CenterID])) {
			t.Errorf("posting %s = %s, want %s", p.CenterID, p.Amount, wantPost[p.CenterID])
		}
	}
	// The senders A and B bring 100 in; the final receiver C takes 100 out.
	if !res.TotalIn.Equal(dec("100")) || !res.TotalOut.Equal(dec("100")) || !res.Converged || !res.Swept.IsZero() {
		t.Errorf("in=%s out=%s converged=%v swept=%s", res.TotalIn, res.TotalOut, res.Converged, res.Swept)
	}
}

// AC-38.5: three service centers serve each other and a production center.
// Solving x1 = 400 + x2/4 + x3/4, x2 = 200 + x1/4 + x3/4, x3 = x1/4 + x2/4 by
// hand gives x = (560, 400, 240); P receives half of each, 600 in total.
func TestAllocate_AC385ThreeServiceCenters(t *testing.T) {
	q, h := dec("1"), dec("2") // basis 1:1:2 gives shares 25% / 25% / 50%
	cycle := Cycle{
		Centers: []string{"S1", "S2", "S3", "P"},
		Costs:   []decimal.Decimal{dec("400"), dec("200"), dec("0"), dec("0")},
		Basis: [][]decimal.Decimal{
			{dec("0"), q, q, h},
			{q, dec("0"), q, h},
			{q, q, dec("0"), h},
			{dec("0"), dec("0"), dec("0"), dec("0")},
		},
	}
	for name, opt := range map[string]SolveOptions{"exact": DefaultSolveOptions(), "iterative": {MaxIter: DefaultMaxIter, Tolerance: dec("0.01")}} {
		t.Run(name, func(t *testing.T) {
			res, err := Allocate(cycle, opt)
			if err != nil {
				t.Fatal(err)
			}
			if name == "exact" {
				want := map[string]string{"S1>S2": "140", "S1>S3": "140", "S1>P": "280", "S2>S1": "100", "S2>S3": "100", "S2>P": "200",
					"S3>S1": "60", "S3>S2": "60", "S3>P": "120"}
				for _, f := range res.Flows {
					if w := want[f.From+">"+f.To]; !f.Amount.Equal(dec(w)) {
						t.Errorf("flow %s>%s = %s, want %s", f.From, f.To, f.Amount, w)
					}
				}
			}
			post := map[string]decimal.Decimal{}
			for _, p := range res.Postings {
				post[p.CenterID] = p.Amount
			}
			if !post["S1"].Equal(dec("-400")) || !post["S2"].Equal(dec("-200")) || !post["S3"].IsZero() || !post["P"].Equal(dec("600")) {
				t.Errorf("postings = %v", post)
			}
			if !res.TotalIn.Equal(dec("600")) || !res.TotalOut.Equal(dec("600")) {
				t.Errorf("in=%s out=%s, want 600", res.TotalIn, res.TotalOut)
			}
		})
	}
}

// BR-38.6: when the iterative path stops at its iteration limit, the run does
// not fail; the residual is swept to the largest receiver so senders end at zero.
func TestAllocate_IterationLimitSweepsResidual(t *testing.T) {
	cycle := Cycle{
		Centers: []string{"A", "B", "P1", "P2"},
		Costs:   []decimal.Decimal{dec("1000"), dec("500"), dec("0"), dec("0")},
		Basis: [][]decimal.Decimal{
			{dec("0"), dec("9"), dec("1"), dec("0")},
			{dec("9"), dec("0"), dec("0"), dec("1")},
			{dec("0"), dec("0"), dec("0"), dec("0")},
			{dec("0"), dec("0"), dec("0"), dec("0")},
		},
	}
	res, err := Allocate(cycle, SolveOptions{MaxIter: 2, Tolerance: dec("0.01")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Converged || res.Swept.IsZero() {
		t.Fatalf("converged=%v swept=%s, want a stopped solve with a sweep", res.Converged, res.Swept)
	}
	for _, p := range res.Postings {
		if p.CenterID == "A" && !p.Amount.Equal(dec("-1000")) || p.CenterID == "B" && !p.Amount.Equal(dec("-500")) {
			t.Errorf("sender %s keeps cost: posting %s", p.CenterID, p.Amount)
		}
	}
	if !res.TotalOut.Equal(dec("1500")) {
		t.Errorf("out = %s, want 1500", res.TotalOut)
	}
}

func TestAllocate_ClosedCycleIsSingular(t *testing.T) {
	cycle := Cycle{
		Centers: []string{"A", "B"},
		Costs:   []decimal.Decimal{dec("100"), dec("0")},
		Basis:   [][]decimal.Decimal{{dec("0"), dec("1")}, {dec("1"), dec("0")}},
	}
	for _, opt := range []SolveOptions{DefaultSolveOptions(), {MaxIter: DefaultMaxIter}} {
		if _, err := Allocate(cycle, opt); !errors.Is(err, ErrSingular) {
			t.Errorf("closed cycle: err = %v, want ErrSingular", err)
		}
	}
}

func TestAllocate_ZeroBasisKeepsCost(t *testing.T) {
	cycle := Cycle{
		Centers: []string{"A", "B"},
		Costs:   []decimal.Decimal{dec("50"), dec("0")},
		Basis:   [][]decimal.Decimal{{dec("0"), dec("0")}, {dec("0"), dec("0")}},
	}
	res, err := Allocate(cycle, DefaultSolveOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Flows) != 0 || len(res.Unallocated) != 1 || res.Unallocated[0] != "A" {
		t.Errorf("flows=%v unallocated=%v", res.Flows, res.Unallocated)
	}
}

func TestSolveReciprocal_ClosedLoopFails(t *testing.T) {
	costs := []decimal.Decimal{dec("100"), dec("0")}
	shares := [][]decimal.Decimal{{dec("0"), dec("1")}, {dec("1"), dec("0")}}
	if _, err := SolveReciprocal(costs, shares, DefaultSolveOptions()); !errors.Is(err, ErrSingular) {
		t.Errorf("exact closed loop: err = %v, want ErrSingular", err)
	}
	opt := DefaultSolveOptions()
	opt.ExactMaxN = 0
	if _, err := SolveReciprocal(costs, shares, opt); !errors.Is(err, ErrNotConverged) {
		t.Errorf("iterative closed loop: err = %v, want ErrNotConverged", err)
	}
}

func TestSolveReciprocal_IterativeMatchesExact(t *testing.T) {
	costs := []decimal.Decimal{dec("300"), dec("200"), dec("100"), dec("0")}
	shares := [][]decimal.Decimal{
		{dec("0"), dec("0.5"), dec("0.5"), dec("0")},
		{dec("0.5"), dec("0"), dec("0.5"), dec("0")},
		{dec("0.3"), dec("0.2"), dec("0"), dec("0.5")},
		{dec("0"), dec("0"), dec("0"), dec("0")},
	}
	exact, err := SolveReciprocal(costs, shares, DefaultSolveOptions())
	if err != nil {
		t.Fatal(err)
	}
	opt := DefaultSolveOptions()
	opt.ExactMaxN = 0
	iter, err := SolveReciprocal(costs, shares, opt)
	if err != nil {
		t.Fatal(err)
	}
	if iter.Iterations > DefaultMaxIter || iter.Residual.GreaterThan(dec("0.01")) {
		t.Errorf("iterations=%d residual=%s", iter.Iterations, iter.Residual)
	}
	// A fixed-point residual of 0.01 bounds the error by residual / (1 - rho), so
	// allow a few cents of drift on totals near 1,000 while still catching real divergence.
	for i := range costs {
		if d := exact.Totals[i].Sub(iter.Totals[i]).Abs(); d.GreaterThan(dec("0.1")) {
			t.Errorf("center %d: exact %s iterative %s", i, exact.Totals[i], iter.Totals[i])
		}
	}
}

func TestAllocate_RejectsMalformedCycles(t *testing.T) {
	cases := map[string]Cycle{
		"basis rows": {Centers: []string{"A", "B"}, Costs: []decimal.Decimal{dec("1"), dec("1")},
			Basis: [][]decimal.Decimal{{dec("0"), dec("1")}}},
		"duplicate center": {Centers: []string{"A", "A"}, Costs: []decimal.Decimal{dec("1"), dec("1")},
			Basis: [][]decimal.Decimal{{dec("0"), dec("1")}, {dec("1"), dec("0")}}},
		"negative basis": {Centers: []string{"A", "B"}, Costs: []decimal.Decimal{dec("1"), dec("1")},
			Basis: [][]decimal.Decimal{{dec("0"), dec("-1")}, {dec("0"), dec("0")}}},
	}
	for name, c := range cases {
		if _, err := Allocate(c, DefaultSolveOptions()); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// genCycle draws a cycle whose last center is an absorbing sink. Every other
// center leaks a positive share to the sink, so I − Sᵀ is nonsingular.
func genCycle(t *rapid.T) Cycle {
	n := rapid.IntRange(2, 6).Draw(t, "n")
	c := Cycle{Centers: make([]string, n), Costs: make([]decimal.Decimal, n), Basis: make([][]decimal.Decimal, n)}
	for j := 0; j < n; j++ {
		c.Centers[j] = fmt.Sprintf("C%02d", j)
		c.Costs[j] = decimal.New(rapid.Int64Range(-100000, 100000).Draw(t, "cost"), -2)
		c.Basis[j] = make([]decimal.Decimal, n)
		if j == n-1 {
			continue
		}
		for i := 0; i < n; i++ {
			c.Basis[j][i] = decimal.NewFromInt(rapid.Int64Range(0, 5).Draw(t, "basis"))
		}
		c.Basis[j][n-1] = decimal.NewFromInt(rapid.Int64Range(1, 5).Draw(t, "leak"))
	}
	return c
}

// Property (7.38.9 allocation conservation): for random leaky cycles, on both
// solver paths, every sender ends at exactly zero, the sink takes exactly the
// senders' cost, and flows are whole cents.
func TestAllocate_ConservationProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		c := genCycle(t)
		opt := DefaultSolveOptions()
		if rapid.Bool().Draw(t, "iterative") {
			opt.ExactMaxN = 0
		}
		res, err := Allocate(c, opt)
		if err != nil {
			t.Fatalf("allocate: %v", err)
		}
		in := decimal.Zero
		for i, p := range res.Postings {
			if i < len(c.Centers)-1 {
				in = in.Add(c.Costs[i])
				if !c.Costs[i].Add(p.Amount).IsZero() {
					t.Fatalf("sender %s holds %s after the step", p.CenterID, c.Costs[i].Add(p.Amount))
				}
			}
		}
		if sink := res.Postings[len(res.Postings)-1].Amount; !sink.Equal(in) {
			t.Fatalf("sink received %s, senders brought %s", sink, in)
		}
		for _, f := range res.Flows {
			if !f.Amount.Equal(f.Amount.Round(2)) {
				t.Fatalf("flow %s -> %s has sub-cent amount %s", f.From, f.To, f.Amount)
			}
		}
		if !res.TotalIn.Equal(res.TotalOut) {
			t.Fatalf("in %s != out %s", res.TotalIn, res.TotalOut)
		}
	})
}
