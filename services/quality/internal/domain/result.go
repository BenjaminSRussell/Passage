package domain

import (
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// Test kinds.
const (
	KindVariable  = "VARIABLE"
	KindAttribute = "ATTRIBUTE"
)

// TestLine is one test inside a test group version (FR-33.1).
type TestLine struct {
	TestID          string           `json:"testId"`
	Name            string           `json:"name"`
	Kind            string           `json:"kind"`
	Required        bool             `json:"required"`
	Decimals        int              `json:"decimals"`
	Min             *decimal.Decimal `json:"min,omitempty"`
	Max             *decimal.Decimal `json:"max,omitempty"`
	PassingOutcomes []string         `json:"passingOutcomeIds,omitempty"`
	Destructive     bool             `json:"destructive"`
}

// Result is one recorded result for a test on one sample (FR-33.4).
type Result struct {
	TestID       string           `json:"testId"`
	SampleNo     int              `json:"sampleNo"`
	RetestNo     int              `json:"retestNo"`
	Value        *decimal.Decimal `json:"value,omitempty"`
	OutcomeID    string           `json:"outcomeId,omitempty"`
	Passed       bool             `json:"passed"`
	InstrumentID string           `json:"instrumentId,omitempty"`
	RecordedBy   string           `json:"recordedBy"`
	RecordedAt   time.Time        `json:"recordedAt"`
}

// EvaluateVariable rounds v half-up (half away from zero, so -1.25 becomes -1.3
// at one decimal, the same as 1.25 becomes 1.3) to the test's declared decimals
// (BR-33.6) and passes when min <= rounded <= max, bounds inclusive; a missing
// bound is open.
func EvaluateVariable(l TestLine, v decimal.Decimal) (decimal.Decimal, bool) {
	rounded := v.Round(int32(l.Decimals))
	if l.Min != nil && rounded.LessThan(*l.Min) {
		return rounded, false
	}
	if l.Max != nil && rounded.GreaterThan(*l.Max) {
		return rounded, false
	}
	return rounded, true
}

// EvaluateAttribute passes when the recorded outcome is one of the test's passing outcomes.
func EvaluateAttribute(l TestLine, outcome string) bool {
	return slices.Contains(l.PassingOutcomes, outcome)
}

// TestGroup is one version of a test group (FR-33.1); versions are immutable.
type TestGroup struct {
	ID      string     `json:"id"`
	Code    string     `json:"code"`
	Version int        `json:"version"`
	Tests   []TestLine `json:"tests"`
}

var groupCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{1,31}$`)

// ValidateTestGroup returns field errors for a new test group version, empty when valid.
func ValidateTestGroup(code string, tests []TestLine) map[string]string {
	fields := map[string]string{}
	if !groupCode.MatchString(code) {
		fields["code"] = "must be 2 to 32 characters of A-Z, 0-9, _ or -"
	}
	if len(tests) == 0 || len(tests) > 200 {
		fields["tests"] = "must hold 1 to 200 tests"
		return fields
	}
	seen := map[string]bool{}
	for i, l := range tests {
		key := "tests[" + strconv.Itoa(i) + "]"
		switch {
		case !ids.Valid(l.TestID):
			fields[key+".testId"] = "must be a UUID"
		case seen[l.TestID]:
			fields[key+".testId"] = "appears twice in the group"
		}
		seen[l.TestID] = true
		if l.Decimals < 0 || l.Decimals > 6 {
			fields[key+".decimals"] = "must be 0 to 6"
		}
		switch l.Kind {
		case KindVariable:
			if l.Min != nil && l.Max != nil && l.Min.GreaterThan(*l.Max) {
				fields[key+".min"] = "must not exceed max"
			}
		case KindAttribute:
			if len(l.PassingOutcomes) == 0 {
				fields[key+".passingOutcomeIds"] = "an attribute test needs at least one passing outcome"
			}
		default:
			fields[key+".kind"] = "must be VARIABLE or ATTRIBUTE"
		}
	}
	return fields
}
