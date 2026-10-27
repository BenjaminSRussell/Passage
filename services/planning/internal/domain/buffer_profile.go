package domain

import (
	"regexp"

	"github.com/shopspring/decimal"
)

// Item types of a buffer profile (data model BufferProfile.item_type).
const (
	BufferManufactured = "manufactured"
	BufferPurchased    = "purchased"
	BufferDistributed  = "distributed"
)

// Defaults of a buffer profile (4.22.13): spike threshold 50 percent of the red
// zone, an ADU window of 90 past and no future days.
const (
	DefaultSpikeThresholdPct = 50
	DefaultADUPastDays       = 90
	minADUWindowDays         = 7
	maxADUWindowDays         = 365
)

var (
	profileCodePattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{1,19}$`)
	// maxFactor is the largest numeric(5,3) value.
	maxFactor = decimal.RequireFromString("99.999")
)

// BufferProfileSpec is a stored buffer profile (FR-22.16). LTF and VF are
// numeric(5,3); the spike threshold is a percent of the red zone (1 to 500);
// a nil SpikeHorizonDays means the horizon is the DLT of the buffered item.
type BufferProfileSpec struct {
	Code              string
	ItemType          string
	LeadTimeFactor    decimal.Decimal
	VariabilityFactor decimal.Decimal
	OrderCycleDays    int
	MinOrderQty       decimal.Decimal
	SpikeThresholdPct decimal.Decimal
	SpikeHorizonDays  *int
	PastDays          int
	FutureDays        int
}

// DefaultBufferProfile is a profile with the 4.22.13 defaults and zero factors.
func DefaultBufferProfile(code, itemType string) BufferProfileSpec {
	return BufferProfileSpec{Code: code, ItemType: itemType, MinOrderQty: decimal.Zero,
		SpikeThresholdPct: decimal.NewFromInt(DefaultSpikeThresholdPct), PastDays: DefaultADUPastDays}
}

// Factors returns the part of the profile the BR-22.11 calculator reads.
func (s BufferProfileSpec) Factors() BufferProfile {
	return BufferProfile{LeadTimeFactor: s.LeadTimeFactor, VariabilityFactor: s.VariabilityFactor,
		OrderCycleDays: s.OrderCycleDays, MinOrderQty: s.MinOrderQty, SpikeThresholdPct: s.SpikeThresholdPct}
}

// ValidProfileCode reports whether code is a well-formed buffer profile code.
func ValidProfileCode(code string) bool { return profileCodePattern.MatchString(code) }

// Validate returns one message per invalid field, keyed by the wire name; nil
// means the spec is valid. The ranges mirror the table checks.
func (s BufferProfileSpec) Validate() map[string]string {
	f := map[string]string{}
	if !ValidProfileCode(s.Code) {
		f["code"] = "must be 2 to 20 characters: A-Z, 0-9, _ or -, starting with a letter or digit"
	}
	switch s.ItemType {
	case BufferManufactured, BufferPurchased, BufferDistributed:
	default:
		f["itemType"] = "must be manufactured, purchased or distributed"
	}
	if msg := factorError(s.LeadTimeFactor); msg != "" {
		f["leadTimeFactor"] = msg
	}
	if msg := factorError(s.VariabilityFactor); msg != "" {
		f["variabilityFactor"] = msg
	}
	if s.OrderCycleDays < 0 || s.OrderCycleDays > 3650 {
		f["orderCycleDays"] = "must be 0 to 3650"
	}
	if msg := coverageQtyError(s.MinOrderQty); msg != "" {
		f["minOrderQty"] = msg
	}
	switch {
	case s.SpikeThresholdPct.LessThan(decimal.NewFromInt(1)) || s.SpikeThresholdPct.GreaterThan(decimal.NewFromInt(500)):
		f["spikeThresholdPct"] = "must be 1 to 500"
	case !s.SpikeThresholdPct.Equal(s.SpikeThresholdPct.Round(2)):
		f["spikeThresholdPct"] = "must have at most 2 decimal places"
	}
	if s.SpikeHorizonDays != nil && (*s.SpikeHorizonDays < 0 || *s.SpikeHorizonDays > 3650) {
		f["spikeHorizonDays"] = "must be 0 to 3650"
	}
	if s.PastDays < 0 || s.PastDays > maxADUWindowDays {
		f["aduWindowPastDays"] = "must be 0 to 365"
	}
	if s.FutureDays < 0 || s.FutureDays > maxADUWindowDays {
		f["aduWindowFutureDays"] = "must be 0 to 365"
	}
	if f["aduWindowPastDays"] == "" && f["aduWindowFutureDays"] == "" && s.PastDays+s.FutureDays < minADUWindowDays {
		f["aduWindowPastDays"] = "the past and future windows together must cover at least 7 days"
	}
	if len(f) == 0 {
		return nil
	}
	return f
}

// factorError checks a lead-time or variability factor against numeric(5,3).
func factorError(d decimal.Decimal) string {
	switch {
	case d.IsNegative() || d.GreaterThan(maxFactor):
		return "must be 0 to 99.999"
	case !d.Equal(d.Round(3)):
		return "must have at most 3 decimal places"
	}
	return ""
}
