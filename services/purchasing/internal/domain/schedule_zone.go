package domain

import "github.com/shopspring/decimal"

// Scheduling agreement zones (FR-17.15, BR-17.13). A schedule line's zone is set
// by its distance from the run date: inside the firm zone it is FIRM, inside the
// trade-off zone TRADEOFF, otherwise FORECAST.
const (
	ZoneFirm     = "FIRM"
	ZoneTradeoff = "TRADEOFF"
	ZoneForecast = "FORECAST"

	// CodeFirmZone is PRC-SA-001: a firm-zone schedule line was reduced without a reason code.
	CodeFirmZone = "PRC-SA-001"
)

// ZoneProfile holds the zone horizons of a release profile, in days.
type ZoneProfile struct {
	FirmDays     int
	TradeoffDays int
}

// Validate requires 0 ≤ firm ≤ trade-off.
func (z ZoneProfile) Validate() error {
	if z.FirmDays < 0 || z.TradeoffDays < z.FirmDays {
		return fail(CodeValidation, "zone horizons must satisfy 0 <= firm (%d) <= trade-off (%d)", z.FirmDays, z.TradeoffDays)
	}
	return nil
}

// ClassifyZone places a schedule date relative to the run date. A date on or
// before run+firm is FIRM, on or before run+trade-off is TRADEOFF, otherwise
// FORECAST. Past dates are FIRM.
func (z ZoneProfile) ClassifyZone(runDate, scheduleDate Day) string {
	days := int(scheduleDate.Time.Sub(runDate.Time).Hours() / 24)
	switch {
	case days <= z.FirmDays:
		return ZoneFirm
	case days <= z.TradeoffDays:
		return ZoneTradeoff
	}
	return ZoneForecast
}

// CheckScheduleReduction applies BR-17.13 to a quantity change on one schedule
// line. Increases and unchanged quantities pass. A reduction inside the firm zone
// needs a reason code (PRC-SA-001). A reduction inside the trade-off zone passes
// with warning=true. A reduction in the forecast zone passes silently.
func CheckScheduleReduction(zone string, oldQty, newQty decimal.Decimal, reason string) (warning bool, err error) {
	if !newQty.LessThan(oldQty) {
		return false, nil
	}
	switch zone {
	case ZoneFirm:
		if reason == "" {
			return false, fail(CodeFirmZone, "schedule line is in the firm zone; reducing %s to %s needs a reason code", oldQty, newQty)
		}
	case ZoneTradeoff:
		return true, nil
	}
	return false, nil
}

// CheckCumulativeRelease enforces BR-17.13: a cumulative released quantity never
// decreases unless a cumulative reset (a new agreement year) is recorded.
func CheckCumulativeRelease(previous, next decimal.Decimal, reset bool) error {
	if next.LessThan(previous) && !reset {
		return fail(CodeCumulativeDecrease, "cumulative released %s cannot decrease from %s without a recorded reset", next, previous)
	}
	return nil
}
