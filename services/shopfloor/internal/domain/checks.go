package domain

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

// Check kinds (FR-25.5: text, numeric or pass-fail with limits).
const (
	CheckNumeric  = "numeric"
	CheckPassFail = "pass-fail"
	CheckText     = "text"
)

// Check is a quality check attached to an operation (FR-25.5). Numeric checks pass
// when the value lies within [Min, Max] (inclusive; an unset bound is open).
// Pass-fail checks take the value "pass" or "fail". Text checks record what the
// operator wrote and pass once a value is recorded.
type Check struct {
	Code      string           `json:"code"`
	Name      string           `json:"name"`
	Kind      string           `json:"kind"`
	Min       *decimal.Decimal `json:"min"`
	Max       *decimal.Decimal `json:"max"`
	Mandatory bool             `json:"mandatory"`
}

// CheckInput is a value the operator recorded for one check code.
type CheckInput struct {
	Code  string `json:"code"`
	Value string `json:"value"`
}

// CheckResult is a recorded value with its evaluation.
type CheckResult struct {
	Code  string `json:"code"`
	Value string `json:"value"`
	Pass  bool   `json:"pass"`
}

// ValidateCheck returns field errors for a check definition, keyed by JSON field name.
func ValidateCheck(c Check) map[string]string {
	fields := map[string]string{}
	if n := utf8.RuneCountInString(c.Code); n < 1 || n > 32 || strings.TrimSpace(c.Code) != c.Code {
		fields["code"] = "must be 1 to 32 characters without surrounding blanks"
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(c.Name)); n < 1 || n > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	switch c.Kind {
	case CheckNumeric:
		if c.Min != nil && c.Max != nil && c.Min.GreaterThan(*c.Max) {
			fields["max"] = "must not be below min"
		}
	case CheckPassFail, CheckText:
		if c.Min != nil || c.Max != nil {
			fields["kind"] = "only numeric checks take limits"
		}
	default:
		fields["kind"] = "must be numeric, pass-fail or text"
	}
	return fields
}

// EvaluateCheck evaluates a recorded value against its definition. The second
// return is empty when the value is acceptable; otherwise it is the reason the value
// cannot be evaluated (a malformed number or an unknown pass-fail word).
func EvaluateCheck(c Check, value string) (CheckResult, string) {
	v := strings.TrimSpace(value)
	out := CheckResult{Code: c.Code, Value: v}
	switch c.Kind {
	case CheckNumeric:
		d, err := decimal.NewFromString(v)
		if err != nil {
			return out, "must be a number"
		}
		out.Pass = (c.Min == nil || d.GreaterThanOrEqual(*c.Min)) && (c.Max == nil || d.LessThanOrEqual(*c.Max))
		return out, ""
	case CheckPassFail:
		switch strings.ToLower(v) {
		case "pass":
			out.Pass = true
			return out, ""
		case "fail":
			return out, ""
		}
		return out, "must be pass or fail"
	case CheckText:
		if v == "" {
			return out, "must not be blank"
		}
		out.Pass = true
		return out, ""
	}
	return out, "unknown check kind"
}

// FinishBlockers returns the codes of mandatory checks that stop an operation from
// being finished (FR-25.5, AC-25.9): a mandatory check with no passing result.
// A supervisor override clears every blocker.
func FinishBlockers(checks []Check, results map[string]CheckResult, overridden bool) []string {
	if overridden {
		return nil
	}
	var blocked []string
	for _, c := range checks {
		if !c.Mandatory {
			continue
		}
		if r, ok := results[c.Code]; !ok || !r.Pass {
			blocked = append(blocked, c.Code)
		}
	}
	return blocked
}

// FinishError is the SF-0005 rule failure for blocked mandatory checks.
func FinishError(blocked []string) error {
	return &Error{Code: "SF-0005", Detail: fmt.Sprintf("Mandatory check %s failed; operation cannot be finished.", strings.Join(blocked, ", "))}
}
