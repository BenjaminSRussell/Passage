package domain

import "github.com/shopspring/decimal"

// NCR severities weighted by the supplier scorecard (FR-33.10).
const (
	NCRCritical = "CRITICAL"
	NCRMajor    = "MAJOR"
	NCRMinor    = "MINOR"
)

// Weights are the scorecard component weights (parameter scorecard_weights).
type Weights struct {
	Quality  int `json:"quality"`
	Delivery int `json:"delivery"`
	NCR      int `json:"ncr"`
	Audit    int `json:"audit"`
}

// DefaultWeights returns quality 40 / delivery 30 / NCR 20 / audit 10.
func DefaultWeights() Weights { return Weights{Quality: 40, Delivery: 30, NCR: 20, Audit: 10} }

// ValidateWeights requires non-negative weights that sum to 100.
func ValidateWeights(w Weights) error {
	if w.Quality < 0 || w.Delivery < 0 || w.NCR < 0 || w.Audit < 0 {
		return invalid("scorecard weights must not be negative")
	}
	if w.Quality+w.Delivery+w.NCR+w.Audit != 100 {
		return invalid("scorecard weights must sum to 100")
	}
	return nil
}

// ScorecardInput is the twelve-month evidence for one vendor.
type ScorecardInput struct {
	LotsInspected    int              `json:"lotsInspected"`
	LotsAccepted     int              `json:"lotsAccepted"`
	UnitsInspected   decimal.Decimal  `json:"unitsInspected"`
	UnitsRejected    decimal.Decimal  `json:"unitsRejected"`
	Deliveries       int              `json:"deliveries"`
	OnTimeDeliveries int              `json:"onTimeDeliveries"`
	NCRs             map[string]int   `json:"ncrs"`
	AuditScore       *decimal.Decimal `json:"auditScore,omitempty"`
}

// Scorecard is the computed vendor quality score. Percentages are 0-100 and
// rounded to two places for display; Total is rounded half-up to two places.
type Scorecard struct {
	LotsInspected  int              `json:"lotsInspected"`
	LotsAccepted   int              `json:"lotsAccepted"`
	AcceptanceRate *decimal.Decimal `json:"acceptanceRate,omitempty"`
	PPM            decimal.Decimal  `json:"ppm"`
	OnTimePct      *decimal.Decimal `json:"onTimePct,omitempty"`
	NCRScore       *decimal.Decimal `json:"ncrScore,omitempty"`
	AuditScore     *decimal.Decimal `json:"auditScore,omitempty"`
	Total          decimal.Decimal  `json:"total"`
}

type scorePart struct {
	weight int
	value  decimal.Decimal
}

// ComputeScorecard applies the FR-33.10 formula.
//   - PPM = rejected units / inspected units × 10^6.
//   - Quality component = lot acceptance rate (accepted lots / inspected lots × 100).
//   - Delivery component = on-time deliveries / deliveries × 100.
//   - NCR component = 100 × max(0, 1 − (3·critical + 2·major + minor) / max(1, lots inspected)).
//   - Audit component = the latest supplier audit score (0-100).
//
// A component without evidence (no lots, no deliveries, no audit) is left out and the
// remaining weights are renormalised, so the total stays on the 0-100 scale.
func ComputeScorecard(in ScorecardInput, w Weights) (Scorecard, error) {
	if err := ValidateWeights(w); err != nil {
		return Scorecard{}, err
	}
	if in.LotsInspected < 0 || in.LotsAccepted < 0 || in.LotsAccepted > in.LotsInspected ||
		in.Deliveries < 0 || in.OnTimeDeliveries < 0 || in.OnTimeDeliveries > in.Deliveries {
		return Scorecard{}, invalid("scorecard counts are inconsistent")
	}
	if in.UnitsInspected.IsNegative() || in.UnitsRejected.IsNegative() || in.UnitsRejected.GreaterThan(in.UnitsInspected) {
		return Scorecard{}, invalid("scorecard units are inconsistent")
	}
	hundred := decimal.NewFromInt(100)
	sc := Scorecard{LotsInspected: in.LotsInspected, LotsAccepted: in.LotsAccepted, PPM: decimal.Zero}
	var parts []scorePart

	if in.UnitsInspected.IsPositive() {
		sc.PPM = in.UnitsRejected.Div(in.UnitsInspected).Mul(decimal.NewFromInt(1000000)).Round(2)
	}
	if in.LotsInspected > 0 {
		rate := ratioPct(in.LotsAccepted, in.LotsInspected)
		sc.AcceptanceRate = roundedPtr(rate)
		parts = append(parts, scorePart{w.Quality, rate})
	}
	if in.Deliveries > 0 {
		onTime := ratioPct(in.OnTimeDeliveries, in.Deliveries)
		sc.OnTimePct = roundedPtr(onTime)
		parts = append(parts, scorePart{w.Delivery, onTime})
	}
	if in.LotsInspected > 0 || len(in.NCRs) > 0 {
		weighted := decimal.NewFromInt(int64(3*in.NCRs[NCRCritical] + 2*in.NCRs[NCRMajor] + in.NCRs[NCRMinor]))
		denom := decimal.NewFromInt(int64(max(1, in.LotsInspected)))
		ncr := hundred.Mul(decimal.NewFromInt(1).Sub(weighted.Div(denom)))
		if ncr.IsNegative() {
			ncr = decimal.Zero
		}
		sc.NCRScore = roundedPtr(ncr)
		parts = append(parts, scorePart{w.NCR, ncr})
	}
	if in.AuditScore != nil {
		if in.AuditScore.IsNegative() || in.AuditScore.GreaterThan(hundred) {
			return Scorecard{}, invalid("audit score must be 0 to 100")
		}
		a := *in.AuditScore
		sc.AuditScore = roundedPtr(a)
		parts = append(parts, scorePart{w.Audit, a})
	}
	if len(parts) == 0 {
		return Scorecard{}, invalid("no scorecard evidence")
	}
	totalW := 0
	sum := decimal.Zero
	for _, p := range parts {
		totalW += p.weight
		sum = sum.Add(p.value.Mul(decimal.NewFromInt(int64(p.weight))))
	}
	if totalW == 0 {
		return Scorecard{}, invalid("the weights of the components with evidence are all zero")
	}
	sc.Total = sum.Div(decimal.NewFromInt(int64(totalW))).Round(2)
	return sc, nil
}

// ratioPct returns part / whole × 100, unrounded.
func ratioPct(part, whole int) decimal.Decimal {
	return decimal.NewFromInt(int64(part)).Mul(decimal.NewFromInt(100)).Div(decimal.NewFromInt(int64(whole)))
}

func roundedPtr(d decimal.Decimal) *decimal.Decimal {
	r := d.Round(2)
	return &r
}
