package domain

import (
	"fmt"
	"math"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func flagged(vs []Violation, index, rule int) bool {
	for _, v := range vs {
		if v.Index == index && v.Rule == rule {
			return true
		}
	}
	return false
}

func ones(n int) []float64 {
	s := make([]float64, n)
	for i := range s {
		s[i] = 1
	}
	return s
}

func TestWesternElectricRules(t *testing.T) {
	cases := []struct {
		name  string
		x     []float64
		index int
		rule  int
	}{
		{"rule 1 beyond 3 sigma", []float64{0, 0, 0, 0, 0, 0, 0, 0, 4}, 8, 1},
		{"rule 2 two of three beyond 2 sigma", []float64{0, 0, 2.5, 0, 2.5}, 4, 2},
		{"rule 3 four of five beyond 1 sigma", []float64{1.5, 1.2, 0, 1.5, 1.2}, 4, 3},
		{"rule 4 eight on one side", []float64{0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5}, 7, 4},
		{"rule 4 negative side", []float64{-0.5, -0.5, -0.5, -0.5, -0.5, -0.5, -0.5, -0.5}, 7, 4},
	}
	for _, c := range cases {
		sig := make([]float64, len(c.x))
		for i := range sig {
			sig[i] = 1
		}
		vs := weRules(ChartXbar, c.x, 0, sig)
		if !flagged(vs, c.index, c.rule) {
			t.Errorf("%s: want rule %d at %d, got %v", c.name, c.rule, c.index, vs)
		}
	}
	// Seven points on one side do not trip rule 4.
	sig := make([]float64, 7)
	for i := range sig {
		sig[i] = 1
	}
	if vs := weRules(ChartXbar, ones(7), 0, sig); len(vs) != 0 {
		t.Errorf("seven one-sided points: %v", vs)
	}
}

func TestZeroSigmaIsQuiet(t *testing.T) {
	x := []float64{5, 5, 5}
	if vs := weRules(ChartXbar, x, 5, make([]float64, 3)); len(vs) != 0 {
		t.Errorf("zero sigma must not flag: %v", vs)
	}
}

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestComputeXBarRLimits(t *testing.T) {
	// Five subgroups of size 5: ranges all 2, means 10, 10, 10, 10, 12.
	g := func(mid string) []decimal.Decimal {
		m := d(mid)
		return []decimal.Decimal{m.Sub(d("1")), m.Sub(d("1")), m, m.Add(d("1")), m.Add(d("1"))}
	}
	chart, err := ComputeXBarR([][]decimal.Decimal{g("10"), g("10"), g("10"), g("10"), g("12")})
	if err != nil {
		t.Fatal(err)
	}
	// Rbar = 2, Xbarbar = 10.4, A2(5) = 0.577, D4(5) = 2.114, D3(5) = 0.
	if math.Abs(chart.XbarLimits.CL-10.4) > 1e-9 {
		t.Errorf("CL = %v", chart.XbarLimits.CL)
	}
	if math.Abs(chart.XbarLimits.UCL-(10.4+0.577*2)) > 1e-9 || math.Abs(chart.XbarLimits.LCL-(10.4-0.577*2)) > 1e-9 {
		t.Errorf("xbar limits %+v", chart.XbarLimits)
	}
	if math.Abs(chart.RLimits.UCL-2.114*2) > 1e-9 || chart.RLimits.LCL != 0 {
		t.Errorf("R limits %+v", chart.RLimits)
	}
	if math.Abs(chart.Sigma-2/2.326) > 1e-9 {
		t.Errorf("sigma = %v", chart.Sigma)
	}
	// The 12 mean is z = 1.86 sigma from the centre line: inside every rule.
	if len(chart.Violations) != 0 {
		t.Errorf("no rule should fire: %v", chart.Violations)
	}
}

func TestComputeXBarRRejects(t *testing.T) {
	one := []decimal.Decimal{d("1")}
	if _, err := ComputeXBarR(nil); err == nil {
		t.Error("no subgroups")
	}
	if _, err := ComputeXBarR([][]decimal.Decimal{one}); err == nil {
		t.Error("size 1 subgroup")
	}
	if _, err := ComputeXBarR([][]decimal.Decimal{{d("1"), d("2")}, {d("1")}}); err == nil {
		t.Error("mixed sizes")
	}
}

func TestComputeXBarRUsesLastWindow(t *testing.T) {
	// 30 subgroups of size 2 (range 1): the first five sit near 100; only the last 25,
	// centred on 10.5, set the limits.
	var groups [][]decimal.Decimal
	for i := 0; i < 30; i++ {
		v := 10
		if i < 5 {
			v = 100
		}
		groups = append(groups, []decimal.Decimal{decimal.NewFromInt(int64(v)), decimal.NewFromInt(int64(v + 1))})
	}
	chart, err := ComputeXBarR(groups)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(chart.XbarLimits.CL-10.5) > 1e-9 {
		t.Errorf("limits must come from the last 25 subgroups, CL = %v", chart.XbarLimits.CL)
	}
	if !flagged(chart.Violations, 0, 1) {
		t.Error("the early subgroups are beyond the limits")
	}
}

func TestComputePChart(t *testing.T) {
	samples := []PSample{{Defectives: 2, Size: 100}, {Defectives: 1, Size: 100}, {Defectives: 3, Size: 100}, {Defectives: 40, Size: 100}}
	chart, err := ComputeP(samples)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(chart.PBar-46.0/400.0) > 1e-12 {
		t.Errorf("pbar = %v", chart.PBar)
	}
	if !flagged(chart.Violations, 3, 1) {
		t.Errorf("the 40 %% sample is beyond 3 sigma: %v", chart.Violations)
	}
	sigma := math.Sqrt(chart.PBar * (1 - chart.PBar) / 100)
	if math.Abs(chart.Limits[0].LCL-(chart.PBar-3*sigma)) > 1e-12 {
		t.Errorf("LCL = %v", chart.Limits[0].LCL)
	}
	small, err := ComputeP([]PSample{{Defectives: 0, Size: 10}, {Defectives: 0, Size: 10}, {Defectives: 1, Size: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if small.Limits[0].LCL != 0 {
		t.Errorf("LCL must clamp at zero, got %v", small.Limits[0].LCL)
	}
	if _, err := ComputeP([]PSample{{Defectives: 5, Size: 4}}); err == nil {
		t.Error("defectives above size must fail")
	}
}

func TestSPCPropertiesHold(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(2, 10).Draw(t, "n")
		k := rapid.IntRange(1, 40).Draw(t, "k")
		groups := rapid.SliceOfN(rapid.Float64Range(-50, 50), n*k, n*k).Draw(t, "values")
		var sub [][]decimal.Decimal
		for i := 0; i < k; i++ {
			var g []decimal.Decimal
			for j := 0; j < n; j++ {
				g = append(g, decimal.NewFromFloat(groups[i*n+j]))
			}
			sub = append(sub, g)
		}
		chart, err := ComputeXBarR(sub)
		if err != nil {
			t.Fatal(err)
		}
		if chart.XbarLimits.UCL < chart.XbarLimits.CL || chart.XbarLimits.LCL > chart.XbarLimits.CL {
			t.Fatalf("limits out of order: %+v", chart.XbarLimits)
		}
		for _, v := range chart.Violations {
			if v.Index < 0 || v.Index >= k || v.Rule < 1 || v.Rule > 4 {
				t.Fatalf("bad violation %+v", v)
			}
		}
	})
}

// The tabulated constants for subgroups 2 to 25 agree with their definitions:
// A2 = 3 / (d2·√n) and, where D3 is not truncated to zero, D3 + D4 = 2.
func TestXBarRConstantsAreConsistent(t *testing.T) {
	for n := 2; n <= MaxSubgroupSize; n++ {
		k, ok := xbarRConstants(n)
		if !ok {
			t.Fatalf("n=%d has no constants", n)
		}
		if a2 := 3 / (k.d2 * math.Sqrt(float64(n))); math.Abs(a2-k.a2) > 0.002 {
			t.Errorf("n=%d: A2 %.3f, 3/(d2·√n) = %.4f", n, k.a2, a2)
		}
		if k.d3 > 0 && math.Abs(k.d3+k.d4-2) > 0.002 {
			t.Errorf("n=%d: D3 + D4 = %.3f", n, k.d3+k.d4)
		}
	}
	for _, n := range []int{1, 26} {
		if _, ok := xbarRConstants(n); ok {
			t.Errorf("n=%d must have no constants", n)
		}
	}
}

// A sampling plan of 13 (Z1.4 code E) gives subgroups of 13, which the chart takes.
func TestComputeXBarRTakesAQLSubgroupOf13(t *testing.T) {
	var groups [][]decimal.Decimal
	for i := 0; i < 3; i++ {
		var g []decimal.Decimal
		for j := 0; j < 13; j++ {
			g = append(g, decimal.NewFromInt(int64(10+j%3)))
		}
		groups = append(groups, g)
	}
	chart, err := ComputeXBarR(groups)
	if err != nil || chart.SubgroupSize != 13 {
		t.Fatalf("n=13: %+v %v", chart, err)
	}
	// Rbar = 2, A2(13) = 0.249: UCL = Xbar-bar + 0.498.
	if math.Abs(chart.XbarLimits.UCL-chart.XbarLimits.CL-0.498) > 1e-9 {
		t.Errorf("UCL - CL = %v, want 0.498", chart.XbarLimits.UCL-chart.XbarLimits.CL)
	}
}

// Samples of constant size also carry the np chart: CL = n·pBar and
// UCL = n·pBar + 3·√(n·pBar·(1 − pBar)).
func TestComputePAddsNPChartForConstantSize(t *testing.T) {
	chart, err := ComputeP([]PSample{{Defectives: 2, Size: 50}, {Defectives: 3, Size: 50}, {Defectives: 5, Size: 50}})
	if err != nil {
		t.Fatal(err)
	}
	if chart.NP == nil || chart.NP.SubgroupSize != 50 || fmt.Sprint(chart.NP.Points) != "[2 3 5]" {
		t.Fatalf("np chart = %+v", chart.NP)
	}
	// pBar = 10/150; n·pBar = 3.333..., spread = 3·√(3.333·0.9333) = 5.292.
	if math.Abs(chart.NP.Limits.CL-10.0/3) > 1e-9 || math.Abs(chart.NP.Limits.UCL-(10.0/3+3*math.Sqrt(10.0/3*(1-1.0/15)))) > 1e-9 || chart.NP.Limits.LCL != 0 {
		t.Errorf("np limits = %+v", chart.NP.Limits)
	}
	varied, err := ComputeP([]PSample{{Defectives: 1, Size: 20}, {Defectives: 1, Size: 30}})
	if err != nil || varied.NP != nil {
		t.Errorf("varying sizes must not carry an np chart: %+v %v", varied.NP, err)
	}
}
