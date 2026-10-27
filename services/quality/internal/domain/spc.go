package domain

import (
	"math"
	"strconv"

	"github.com/shopspring/decimal"
)

// SPCWindow is the number of most recent subgroups used to compute control limits (FR-33.11).
const SPCWindow = 25

// Chart names used in violations.
const (
	ChartXbar = "XBAR"
	ChartR    = "R"
	ChartP    = "P"
)

// Violation is a flagged point: Rule is a Western Electric rule (1-4) or 1 for a
// range or p point beyond its limits. Index is zero-based within the series.
type Violation struct {
	Chart string `json:"chart"`
	Index int    `json:"index"`
	Rule  int    `json:"rule"`
}

// Limits are a centre line and its control limits.
type Limits struct {
	CL  float64 `json:"cl"`
	UCL float64 `json:"ucl"`
	LCL float64 `json:"lcl"`
}

// XBarR is an X-bar and R chart computed from fixed-size subgroups.
type XBarR struct {
	SubgroupSize int         `json:"subgroupSize"`
	Xbar         []float64   `json:"xbar"`
	Range        []float64   `json:"range"`
	XbarLimits   Limits      `json:"xbarLimits"`
	RLimits      Limits      `json:"rLimits"`
	Sigma        float64     `json:"sigma"`
	Violations   []Violation `json:"violations"`
}

// chartK holds the tabulated control-chart constants for one subgroup size.
type chartK struct{ a2, d2, d3, d4 float64 }

// xbarRTable holds A2, d2, D3 and D4 for subgroup sizes 2 to 25 (ASTM E2587,
// Montgomery Appendix VI). Index 0 is subgroup size 2.
var xbarRTable = [...]chartK{
	{1.880, 1.128, 0, 3.267}, {1.023, 1.693, 0, 2.574}, {0.729, 2.059, 0, 2.282}, {0.577, 2.326, 0, 2.114},
	{0.483, 2.534, 0, 2.004}, {0.419, 2.704, 0.076, 1.924}, {0.373, 2.847, 0.136, 1.864}, {0.337, 2.970, 0.184, 1.816},
	{0.308, 3.078, 0.223, 1.777}, {0.285, 3.173, 0.256, 1.744}, {0.266, 3.258, 0.283, 1.717}, {0.249, 3.336, 0.307, 1.693},
	{0.235, 3.407, 0.328, 1.672}, {0.223, 3.472, 0.347, 1.653}, {0.212, 3.532, 0.363, 1.637}, {0.203, 3.588, 0.378, 1.622},
	{0.194, 3.640, 0.391, 1.608}, {0.187, 3.689, 0.403, 1.597}, {0.180, 3.735, 0.415, 1.585}, {0.173, 3.778, 0.425, 1.575},
	{0.167, 3.819, 0.434, 1.566}, {0.162, 3.858, 0.443, 1.557}, {0.157, 3.895, 0.451, 1.548}, {0.153, 3.931, 0.459, 1.541},
}

// MaxSubgroupSize is the largest subgroup an X-bar/R chart takes.
const MaxSubgroupSize = 25

// xbarRConstants returns A2, d2, D3 and D4 for subgroup sizes 2 to 25.
func xbarRConstants(n int) (chartK, bool) {
	if n < 2 || n > MaxSubgroupSize {
		return chartK{}, false
	}
	return xbarRTable[n-2], true
}

// ComputeXBarR builds the X-bar and R charts. Every subgroup has the same size,
// between 2 and 25. Limits come from the last SPCWindow subgroups; the rules are
// applied to all points, using sigma = Rbar / d2 for the X-bar zones.
func ComputeXBarR(subgroups [][]decimal.Decimal) (XBarR, error) {
	k := len(subgroups)
	if k == 0 {
		return XBarR{}, fail(CodeSPCUnsupported, "at least one subgroup is required")
	}
	n := len(subgroups[0])
	ck, ok := xbarRConstants(n)
	if !ok {
		return XBarR{}, fail(CodeSPCUnsupported, "an X-bar/R chart needs subgroups of 2 to 25 samples; these orders have "+strconv.Itoa(n))
	}
	out := XBarR{SubgroupSize: n, Xbar: make([]float64, k), Range: make([]float64, k)}
	for i, g := range subgroups {
		if len(g) != n {
			return XBarR{}, fail(CodeSPCUnsupported, "all subgroups must have the same size")
		}
		lo, hi, sum := g[0].InexactFloat64(), g[0].InexactFloat64(), 0.0
		for _, v := range g {
			f := v.InexactFloat64()
			sum += f
			lo = math.Min(lo, f)
			hi = math.Max(hi, f)
		}
		out.Xbar[i] = sum / float64(n)
		out.Range[i] = hi - lo
	}
	start := max(0, k-SPCWindow)
	xbarbar := mean(out.Xbar[start:])
	rbar := mean(out.Range[start:])
	out.XbarLimits = Limits{CL: xbarbar, UCL: xbarbar + ck.a2*rbar, LCL: xbarbar - ck.a2*rbar}
	out.RLimits = Limits{CL: rbar, UCL: ck.d4 * rbar, LCL: ck.d3 * rbar}
	out.Sigma = rbar / ck.d2

	sigmas := make([]float64, k)
	for i := range sigmas {
		sigmas[i] = out.Sigma
	}
	out.Violations = weRules(ChartXbar, out.Xbar, xbarbar, sigmas)
	for i, r := range out.Range {
		if r > out.RLimits.UCL || r < out.RLimits.LCL {
			out.Violations = append(out.Violations, Violation{Chart: ChartR, Index: i, Rule: 1})
		}
	}
	return out, nil
}

// PSample is one inspection for a p chart: defective units out of Size inspected.
type PSample struct {
	Defectives int `json:"defectives"`
	Size       int `json:"size"`
}

// PChart is a p chart with variable subgroup sizes. When every sample has the
// same size NP is the equivalent np chart (defective counts against n·pBar).
type PChart struct {
	PBar       float64     `json:"pBar"`
	Sizes      []int       `json:"sizes"`
	Points     []float64   `json:"points"`
	Limits     []Limits    `json:"limits"`
	Violations []Violation `json:"violations"`
	NP         *NPChart    `json:"np,omitempty"`
}

// NPChart is an np chart: the number of defective units per sample of constant size.
type NPChart struct {
	SubgroupSize int    `json:"subgroupSize"`
	Points       []int  `json:"points"`
	Limits       Limits `json:"limits"`
}

// ComputeP builds a p chart. pBar is the pooled fraction over the last SPCWindow
// samples; each point has its own limits from its subgroup size, clamped to [0, 1].
func ComputeP(samples []PSample) (PChart, error) {
	if len(samples) == 0 {
		return PChart{}, fail(CodeSPCUnsupported, "at least one sample is required")
	}
	for _, s := range samples {
		if s.Size <= 0 || s.Defectives < 0 || s.Defectives > s.Size {
			return PChart{}, fail(CodeSPCUnsupported, "each sample needs 0 <= defectives <= size, size > 0")
		}
	}
	start := max(0, len(samples)-SPCWindow)
	var def, size int
	for _, s := range samples[start:] {
		def += s.Defectives
		size += s.Size
	}
	pbar := float64(def) / float64(size)
	out := PChart{PBar: pbar, Sizes: make([]int, len(samples)), Points: make([]float64, len(samples)), Limits: make([]Limits, len(samples))}
	sigmas := make([]float64, len(samples))
	constant := true
	for i, s := range samples {
		out.Sizes[i] = s.Size
		constant = constant && s.Size == samples[0].Size
		n := float64(s.Size)
		sigma := math.Sqrt(pbar * (1 - pbar) / n)
		sigmas[i] = sigma
		out.Points[i] = float64(s.Defectives) / n
		out.Limits[i] = Limits{CL: pbar, UCL: math.Min(1, pbar+3*sigma), LCL: math.Max(0, pbar-3*sigma)}
	}
	out.Violations = weRules(ChartP, out.Points, pbar, sigmas)
	if constant {
		n := float64(samples[0].Size)
		spread := 3 * math.Sqrt(n*pbar*(1-pbar))
		np := &NPChart{SubgroupSize: samples[0].Size, Points: make([]int, len(samples)),
			Limits: Limits{CL: n * pbar, UCL: math.Min(n, n*pbar+spread), LCL: math.Max(0, n*pbar-spread)}}
		for i, s := range samples {
			np.Points[i] = s.Defectives
		}
		out.NP = np
	}
	return out, nil
}

// weRules applies Western Electric rules 1-4 to a series. Zone widths are sigmas[i];
// a point with zero sigma sits on the centre line and triggers nothing.
//   - Rule 1: one point beyond 3 sigma.
//   - Rule 2: two of three consecutive points beyond 2 sigma on the same side.
//   - Rule 3: four of five consecutive points beyond 1 sigma on the same side.
//   - Rule 4: eight consecutive points on the same side of the centre line.
func weRules(chart string, x []float64, cl float64, sigmas []float64) []Violation {
	z := make([]float64, len(x))
	for i := range x {
		if sigmas[i] > 0 {
			z[i] = (x[i] - cl) / sigmas[i]
		}
	}
	out := []Violation{}
	add := func(i, rule int) { out = append(out, Violation{Chart: chart, Index: i, Rule: rule}) }
	for i := range z {
		if math.Abs(z[i]) > 3 {
			add(i, 1)
		}
		if i >= 2 && sideCount(z[i-2:i+1], 2) >= 2 {
			add(i, 2)
		}
		if i >= 4 && sideCount(z[i-4:i+1], 1) >= 4 {
			add(i, 3)
		}
		if i >= 7 && sameSide(z[i-7:i+1]) {
			add(i, 4)
		}
	}
	return out
}

// sideCount returns the larger number of points beyond +k or below -k.
func sideCount(z []float64, k float64) int {
	hi, lo := 0, 0
	for _, v := range z {
		if v > k {
			hi++
		}
		if v < -k {
			lo++
		}
	}
	return max(hi, lo)
}

func sameSide(z []float64) bool {
	allPos, allNeg := true, true
	for _, v := range z {
		allPos = allPos && v > 0
		allNeg = allNeg && v < 0
	}
	return allPos || allNeg
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, v := range xs {
		s += v
	}
	return s / float64(len(xs))
}
