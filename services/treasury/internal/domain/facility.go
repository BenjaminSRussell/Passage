package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// CodeFacilityLimit is raised when an instrument would exceed the facility limit (FR-36.7).
const CodeFacilityLimit = "TRS-0415"

// Instrument kinds drawn on a bank facility.
const (
	InstrumentLCImport  = "lc_import"
	InstrumentLCExport  = "lc_export"
	InstrumentGuarantee = "guarantee"
)

// ValidInstrumentKind reports whether kind is a supported facility instrument.
func ValidInstrumentKind(kind string) bool {
	return kind == InstrumentLCImport || kind == InstrumentLCExport || kind == InstrumentGuarantee
}

// CheckFacilityLimit enforces utilization + amount <= limit. The
// TreasuryFacilityOverride duty (override = true) lets a breach through; the
// caller records that in the instrument's limit_override flag.
func CheckFacilityLimit(limit, utilized, amount decimal.Decimal, override bool) error {
	if !amount.IsPositive() {
		return &Error{Code: CodeFacilityLimit, Detail: "instrument amount must be positive"}
	}
	next := utilized.Add(amount)
	if override || next.LessThanOrEqual(limit) {
		return nil
	}
	excess := next.Sub(limit)
	return &Error{Code: CodeFacilityLimit, Detail: fmt.Sprintf("facility limit exceeded by %s", excess.StringFixed(2))}
}

// Headroom is limit − utilization, never negative.
func Headroom(limit, utilized decimal.Decimal) decimal.Decimal {
	h := limit.Sub(utilized)
	if h.IsNegative() {
		return decimal.Zero
	}
	return h
}
