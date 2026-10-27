package domain

import (
	"regexp"
	"time"

	"github.com/shopspring/decimal"
)

// Reconciliation outcomes (FR-46.13). BLOCKED is raised by the job when a dbt
// test upstream of the metric failed (FR-46.5).
const (
	ReconOK      = "OK"
	ReconFailed  = "FAILED"
	ReconBlocked = "BLOCKED"

	// TrustThresholdDays is how many consecutive days inside tolerance mark a
	// metric TRUSTED (BR-46.10).
	TrustThresholdDays = 30
)

// DefaultTolerance is the reconciliation tolerance from FR-46.13: 0.005.
func DefaultTolerance() decimal.Decimal { return decimal.New(5, -3) }

// Reconciliation is one metric compared with its source for one legal entity
// on one day.
type Reconciliation struct {
	MetricID          string          `json:"metricId"`
	LegalEntityID     string          `json:"legalEntityId"`
	Date              string          `json:"date"`
	Currency          string          `json:"currency"`
	SemanticValue     decimal.Decimal `json:"semanticValue"`
	SourceValue       decimal.Decimal `json:"sourceValue"`
	Delta             decimal.Decimal `json:"delta"`
	Tolerance         decimal.Decimal `json:"tolerance"`
	Status            string          `json:"status"`
	ConsecutiveOKDays int             `json:"consecutiveOkDays"`
	Trusted           bool            `json:"trusted"`
}

// Compare returns semantic - source and the outcome. A delta exactly equal to
// the tolerance is still OK; only |delta| strictly greater than it fails.
func Compare(semantic, source, tolerance decimal.Decimal) (decimal.Decimal, string) {
	delta := semantic.Sub(source)
	if delta.Abs().GreaterThan(tolerance) {
		return delta, ReconFailed
	}
	return delta, ReconOK
}

// NextTrust advances the consecutive in-tolerance counter. Any non-OK day resets
// it to zero.
func NextTrust(previous int, status string) int {
	if status != ReconOK {
		return 0
	}
	return previous + 1
}

// IsTrusted reports whether a consecutive-OK count reaches the trust threshold.
func IsTrusted(consecutiveOK int) bool { return consecutiveOK >= TrustThresholdDays }

var (
	currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
	uuidText     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// FinalizeReconciliation validates one day's result and derives what the
// caller must not set itself: delta is always semantic - source, and the status
// is the Compare outcome unless the job marked the day BLOCKED (an upstream dbt
// test failed, FR-46.5). The trust counter is left to the store, which reads
// the previous day.
func FinalizeReconciliation(r Reconciliation) (Reconciliation, *Error) {
	fields := map[string]string{}
	if !metricID.MatchString(r.MetricID) {
		fields["metricId"] = "must be a lower snake case metric id"
	}
	if !uuidText.MatchString(r.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if _, err := time.Parse(time.DateOnly, r.Date); err != nil {
		fields["date"] = "must be YYYY-MM-DD"
	}
	if !currencyCode.MatchString(r.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	if r.Tolerance.IsNegative() || r.Tolerance.GreaterThan(decimal.NewFromInt(1)) {
		fields["tolerance"] = "must be between 0 and 1.00"
	}
	if r.Status != "" && r.Status != ReconOK && r.Status != ReconFailed && r.Status != ReconBlocked {
		fields["status"] = "must be OK, FAILED or BLOCKED"
	}
	if len(fields) > 0 {
		return Reconciliation{}, validation(fields)
	}
	delta, status := Compare(r.SemanticValue, r.SourceValue, r.Tolerance)
	if r.Status == ReconBlocked {
		status = ReconBlocked
	}
	r.Delta, r.Status = delta, status
	return r, nil
}
