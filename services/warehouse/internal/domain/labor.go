package domain

import (
	"fmt"
	"slices"
	"time"

	"github.com/shopspring/decimal"
)

// TravelMetric is the travel distance method (labor_travel_metric, §6.31.13).
// GRAPH needs the aisle network and is a later slice.
type TravelMetric string

const (
	TravelManhattan TravelMetric = "MANHATTAN"
	TravelEuclidean TravelMetric = "EUCLIDEAN"
)

// ValidTravelMetric reports whether m is a metric this slice computes.
func ValidTravelMetric(m TravelMetric) bool {
	return m == TravelManhattan || m == TravelEuclidean
}

// CodeLaborOutlier is the catalog code for a line excluded from performance.
const CodeLaborOutlier = "WMS-0118"

// Coordinate is a location position in metres.
type Coordinate struct {
	X decimal.Decimal `json:"x"`
	Y decimal.Decimal `json:"y"`
	Z decimal.Decimal `json:"z"`
}

// Travel returns the distance between two positions by the metric.
func Travel(from, to Coordinate, m TravelMetric) (decimal.Decimal, error) {
	dx := to.X.Sub(from.X).Abs()
	dy := to.Y.Sub(from.Y).Abs()
	dz := to.Z.Sub(from.Z).Abs()
	switch m {
	case TravelManhattan:
		return dx.Add(dy).Add(dz), nil
	case TravelEuclidean:
		return decimalSqrt(dx.Mul(dx).Add(dy.Mul(dy)).Add(dz.Mul(dz))), nil
	}
	return decimal.Zero, fmt.Errorf("travel metric %q is not supported", m)
}

// decimalSqrt is Newton's method in decimal arithmetic, rounded to 6 places.
func decimalSqrt(s decimal.Decimal) decimal.Decimal {
	if !s.IsPositive() {
		return decimal.Zero
	}
	x := s
	if s.LessThan(decimal.NewFromInt(1)) {
		x = decimal.NewFromInt(1)
	}
	eps := decimal.New(1, -9)
	two := decimal.NewFromInt(2)
	for range 200 {
		next := x.Add(s.Div(x)).Div(two)
		done := next.Sub(x).Abs().LessThan(eps)
		x = next
		if done {
			break
		}
	}
	return x.Round(6)
}

// Formula is an engineered labor standard (BR-31.14). Coefficients are seconds
// per unit of each driver; SetupS is paid once per line.
type Formula struct {
	SetupS          decimal.Decimal `json:"setupS"`
	SecondsPerMeter decimal.Decimal `json:"secondsPerMeter"`
	SecondsPerUnit  decimal.Decimal `json:"secondsPerUnit"`
	SecondsPerLP    decimal.Decimal `json:"secondsPerLp"`
	SecondsPerKg    decimal.Decimal `json:"secondsPerKg"`
}

// Validate rejects negative coefficients.
func (f Formula) Validate() error {
	for name, v := range map[string]decimal.Decimal{
		"setupS": f.SetupS, "secondsPerMeter": f.SecondsPerMeter, "secondsPerUnit": f.SecondsPerUnit,
		"secondsPerLp": f.SecondsPerLP, "secondsPerKg": f.SecondsPerKg,
	} {
		if v.IsNegative() {
			return fmt.Errorf("formula %s must not be negative", name)
		}
	}
	return nil
}

// Measure holds the drivers of one work line in base units.
type Measure struct {
	TravelM  decimal.Decimal `json:"travelM"`
	Qty      decimal.Decimal `json:"qty"`
	LPs      decimal.Decimal `json:"lps"`
	WeightKg decimal.Decimal `json:"weightKg"`
}

// Planned returns planned seconds: setup + travel×s/m + qty×s/unit + lps×s/lp +
// weight×s/kg, rounded to 0.1 s.
func (f Formula) Planned(m Measure) decimal.Decimal {
	s := f.SetupS.
		Add(m.TravelM.Mul(f.SecondsPerMeter)).
		Add(m.Qty.Mul(f.SecondsPerUnit)).
		Add(m.LPs.Mul(f.SecondsPerLP)).
		Add(m.WeightKg.Mul(f.SecondsPerKg))
	return s.Round(1)
}

// Pause is an interval inside a confirmation.
type Pause struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Confirmation is the captured time of one confirmed work line.
type Confirmation struct {
	LineID      string    `json:"lineId"`
	UserID      string    `json:"userId"`
	WorkClass   string    `json:"workClass"`
	Shift       string    `json:"shift"`
	StartedAt   time.Time `json:"startedAt"`
	ConfirmedAt time.Time `json:"confirmedAt"`
	Pauses      []Pause   `json:"pauses,omitempty"`
}

// DefaultIndirectThreshold is labor_indirect_threshold_s (§6.31.13, default 120 s).
const DefaultIndirectThreshold = 120 * time.Second

// Actual returns direct seconds and indirect seconds for a confirmation. Pauses
// longer than threshold are indirect time and leave the direct figure (BR-31.14).
// Every pause must lie inside [StartedAt, ConfirmedAt] and pauses must not
// overlap, or the same time would be subtracted twice.
func Actual(c Confirmation, threshold time.Duration) (actual, indirect decimal.Decimal, err error) {
	if c.ConfirmedAt.Before(c.StartedAt) {
		return actual, indirect, fmt.Errorf("line %q confirmed before it started", c.LineID)
	}
	pauses := slices.Clone(c.Pauses)
	slices.SortFunc(pauses, func(a, b Pause) int { return a.Start.Compare(b.Start) })
	direct := c.ConfirmedAt.Sub(c.StartedAt)
	var indirectNS time.Duration
	for i, p := range pauses {
		d := p.End.Sub(p.Start)
		switch {
		case d < 0:
			return actual, indirect, fmt.Errorf("line %q has a pause that ends before it starts", c.LineID)
		case p.Start.Before(c.StartedAt) || p.End.After(c.ConfirmedAt):
			return actual, indirect, fmt.Errorf("line %q has a pause outside the confirmation interval", c.LineID)
		case i > 0 && p.Start.Before(pauses[i-1].End):
			return actual, indirect, fmt.Errorf("line %q has overlapping pauses", c.LineID)
		}
		if d > threshold {
			direct -= d
			indirectNS += d
		}
	}
	const nsPerSecond = int64(time.Second)
	actual = decimal.NewFromInt(int64(direct)).Div(decimal.NewFromInt(nsPerSecond)).Round(3)
	indirect = decimal.NewFromInt(int64(indirectNS)).Div(decimal.NewFromInt(nsPerSecond)).Round(3)
	return actual, indirect, nil
}

// LineResult is a planned and actual pair for one confirmed line.
type LineResult struct {
	LineID    string          `json:"lineId"`
	UserID    string          `json:"userId"`
	WorkClass string          `json:"workClass"`
	Shift     string          `json:"shift"`
	Planned   decimal.Decimal `json:"planned"`
	Actual    decimal.Decimal `json:"actual"`
	Indirect  decimal.Decimal `json:"indirect"`
	Outlier   bool            `json:"outlier"`
}

// Outlier reports whether actual falls outside 0.2× to 5× planned (BR-31.14).
// Outliers are excluded from performance.
func Outlier(planned, actual decimal.Decimal) bool {
	low := planned.Mul(decimal.New(2, -1))
	high := planned.Mul(decimal.NewFromInt(5))
	return actual.LessThan(low) || actual.GreaterThan(high)
}

// LaborLine computes the planned and actual pair of one confirmed line and flags
// it as an outlier (BR-31.14). The warning is the WMS-0118 text, or empty.
func LaborLine(f Formula, m Measure, c Confirmation, threshold time.Duration) (LineResult, string, error) {
	if err := f.Validate(); err != nil {
		return LineResult{}, "", err
	}
	actual, indirect, err := Actual(c, threshold)
	if err != nil {
		return LineResult{}, "", err
	}
	planned := f.Planned(m)
	r := LineResult{LineID: c.LineID, UserID: c.UserID, WorkClass: c.WorkClass, Shift: c.Shift,
		Planned: planned, Actual: actual, Indirect: indirect, Outlier: Outlier(planned, actual)}
	var warning string
	if r.Outlier {
		warning = fmt.Sprintf("%s: labor line %s excluded as outlier (actual %s s vs planned %s s)", CodeLaborOutlier, c.LineID, actual, planned)
	}
	return r, warning, nil
}

// Performance is Σ planned ÷ Σ actual × 100 over non-outlier lines.
type Performance struct {
	Lines    int             `json:"lines"`
	Outliers int             `json:"outliers"`
	Planned  decimal.Decimal `json:"planned"`
	Actual   decimal.Decimal `json:"actual"`
	Percent  decimal.Decimal `json:"percent"`
}

// Performances groups line results by key (user, work class, shift or
// warehouse) and computes performance per group. A group with no actual time
// reports 0 percent.
func Performances(lines []LineResult, key func(LineResult) string) map[string]Performance {
	out := map[string]Performance{}
	for _, l := range lines {
		k := key(l)
		p := out[k]
		p.Lines++
		if l.Outlier {
			p.Outliers++
		} else {
			p.Planned = p.Planned.Add(l.Planned)
			p.Actual = p.Actual.Add(l.Actual)
		}
		out[k] = p
	}
	for k, p := range out {
		if p.Actual.IsPositive() {
			p.Percent = p.Planned.Div(p.Actual).Mul(decimal.NewFromInt(100)).Round(1)
		}
		out[k] = p
	}
	return out
}
