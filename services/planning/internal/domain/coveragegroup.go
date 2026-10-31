package domain

import (
	"regexp"
	"slices"
)

// CoverageGroupSpec is the setup of one coverage group (FR-22.2, data model
// CoverageGroup). Fences are in days from today with the meaning they have on
// the run input: freeze and firming 0 mean no fence, action and futures 0 mean
// unlimited.
type CoverageGroupSpec struct {
	Code                  string
	CoverageCode          string
	CoverageTimeFenceDays int
	NegativeDays          int
	PositiveDays          int
	PeriodDays            int
	FreezeFenceDays       int
	ActionFenceDays       int
	FuturesFenceDays      int
	FirmingFenceDays      int
	AutoFirm              bool
	GenerateActions       bool
	GenerateFutures       bool
	// SafetyStockPolicy is SafetyToday or SafetyFirstIssue (BR-22.5).
	SafetyStockPolicy string
}

// Safety-stock policies of a coverage group (FR-22.2, BR-22.5).
const (
	SafetyToday      = "today"
	SafetyFirstIssue = "first-issue"
)

const (
	coverageFieldMaxDays  = 3650
	coverageFieldMaxShort = 365
)

var (
	coverageGroupCode  = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{1,19}$`)
	coverageGroupCodes = []string{CoverageRequirement, CoveragePeriod, CoverageMinMax, CoverageManual}
)

// DefaultCoverageGroup returns the FR-22.2 defaults for a coverage code: a
// coverage time fence of 100 days, negative days 1, positive days 0, firming
// and freeze fences 0, auto-firm off (4.22.13), and action and futures
// messages on.
func DefaultCoverageGroup(code, coverageCode string) CoverageGroupSpec {
	return CoverageGroupSpec{
		Code: code, CoverageCode: coverageCode, CoverageTimeFenceDays: 100, NegativeDays: 1,
		GenerateActions: true, GenerateFutures: true, SafetyStockPolicy: SafetyToday,
	}
}

// Validate returns one message per invalid field, keyed by the wire name of the
// field. An empty map means the spec is valid.
func (g CoverageGroupSpec) Validate() map[string]string {
	errs := map[string]string{}
	if !coverageGroupCode.MatchString(g.Code) {
		errs["code"] = "must be 2 to 20 characters of A-Z, 0-9, _ or -"
	}
	if !slices.Contains(coverageGroupCodes, g.CoverageCode) {
		errs["coverageCode"] = "must be requirement, period, minmax or manual"
	}
	if g.CoverageTimeFenceDays < 1 || g.CoverageTimeFenceDays > coverageFieldMaxDays {
		errs["coverageTimeFenceDays"] = "must be 1 to 3650"
	}
	for name, v := range map[string]int{"negativeDays": g.NegativeDays, "positiveDays": g.PositiveDays} {
		if v < 0 || v > coverageFieldMaxShort {
			errs[name] = "must be 0 to 365"
		}
	}
	if g.CoverageCode == CoveragePeriod && (g.PeriodDays < 1 || g.PeriodDays > coverageFieldMaxShort) {
		errs["periodDays"] = "must be 1 to 365 for a period coverage group"
	} else if g.PeriodDays < 0 || g.PeriodDays > coverageFieldMaxShort {
		errs["periodDays"] = "must be 0 to 365"
	}
	for name, v := range map[string]int{"freezeFenceDays": g.FreezeFenceDays, "actionFenceDays": g.ActionFenceDays,
		"futuresFenceDays": g.FuturesFenceDays, "firmingFenceDays": g.FirmingFenceDays} {
		if v < 0 || v > coverageFieldMaxDays {
			errs[name] = "must be 0 to 3650"
		}
	}
	if g.SafetyStockPolicy != SafetyToday && g.SafetyStockPolicy != SafetyFirstIssue {
		errs["safetyStockPolicy"] = "must be today or first-issue"
	}
	if g.AutoFirm && g.FirmingFenceDays == 0 {
		errs["autoFirm"] = "needs a firming fence of at least 1 day"
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}
