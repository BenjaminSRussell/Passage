package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// Parameters are the per-legal-entity sales settings (PRD 5.27.13) that the
// flows read. A legal entity that never saved them uses DefaultParameters.
// The JSON names follow the PRD parameter names.
type Parameters struct {
	QuotationExpiryDays         int    `json:"quotationExpiryDays"`
	QuotationProbabilityDefault int    `json:"quotationProbabilityDefault"`
	CreditReleaseValidityDays   int    `json:"creditReleaseValidityDays"`
	CreditHoldSLABusinessDays   int    `json:"creditHoldSlaBusinessDays"`
	DefaultOrderType            string `json:"defaultOrderType"`

	// CreditCheckPoints is credit_check_points (FR-27.7): where the credit
	// rules run. Sales runs entry, confirm and release; confirm is required
	// (the only point that holds an order today) and packing_slip and invoice
	// are refused until the fulfillment and billing waves run them.
	CreditCheckPoints []string `json:"creditCheckPoints"`

	// IntakeDedupWindowDays is intake_dedup_window_days (BR-27.10): how long a
	// (channel, externalRef) pair identifies one order.
	IntakeDedupWindowDays int `json:"intakeDedupWindowDays"`

	// OneTimeOrderLimit and its currency are one_time_order_limit (FR-27.22);
	// both are set or both are absent, and absent means the currency defaults.
	OneTimeOrderLimit         *decimal.Decimal `json:"oneTimeOrderLimit,omitempty"`
	OneTimeOrderLimitCurrency *string          `json:"oneTimeOrderLimitCurrency,omitempty"`

	// DeliveryDateControlDefault is delivery_date_control_default (FR-27.3):
	// how a line without its own control gets its confirmed ship date. The PRD
	// default is ATP; until the ATP projection exists the default is NONE (the
	// requested date stands) and only NONE and SALES_LEAD_TIME can be saved.
	// SalesLeadTimeDays is the lead time SALES_LEAD_TIME adds to today.
	DeliveryDateControlDefault string `json:"deliveryDateControlDefault"`
	SalesLeadTimeDays          int    `json:"salesLeadTimeDays"`
}

const (
	// MaxCreditReleaseValidityDays bounds credit_release_validity_days (PRD 5.27.13).
	MaxCreditReleaseValidityDays = 30
	// DefaultCreditHoldSLABusinessDays is credit_hold_sla_business_days when
	// none is saved (FR-27.8).
	DefaultCreditHoldSLABusinessDays = 2
	// MaxCreditHoldSLABusinessDays bounds credit_hold_sla_business_days.
	MaxCreditHoldSLABusinessDays = 10
	// DefaultIntakeDedupWindowDays is intake_dedup_window_days when none is saved.
	DefaultIntakeDedupWindowDays = 30
	// MaxIntakeDedupWindowDays bounds intake_dedup_window_days (1 to 90, PRD 5.27.13).
	MaxIntakeDedupWindowDays = 90
)

// DefaultParameters returns the settings used when none are stored.
func DefaultParameters() Parameters {
	return Parameters{QuotationExpiryDays: QuotationExpiryDays, QuotationProbabilityDefault: DefaultProbabilityPct,
		CreditReleaseValidityDays: CreditReleaseDays, CreditHoldSLABusinessDays: DefaultCreditHoldSLABusinessDays,
		DefaultOrderType: "ORDER", CreditCheckPoints: []string{CheckPointConfirm, CheckPointRelease},
		IntakeDedupWindowDays: DefaultIntakeDedupWindowDays, DeliveryDateControlDefault: DeliveryControlNone}
}

// The credit_check_points values.
const (
	CheckPointEntry       = "entry"
	CheckPointConfirm     = "confirm"
	CheckPointRelease     = "release"
	CheckPointPackingSlip = "packing_slip"
	CheckPointInvoice     = "invoice"
)

// activeCheckPoints are the values a legal entity may set today. packing_slip
// and invoice are PRD values (5.27.13) whose checks belong to the fulfillment
// (w28) and billing (w29) waves; until those run them, a set naming them is
// refused rather than stored as a setting that does nothing.
var activeCheckPoints = map[string]bool{CheckPointEntry: true, CheckPointConfirm: true, CheckPointRelease: true}

// notActiveCheckPoints are the PRD values whose checks are not built yet.
var notActiveCheckPoints = map[string]bool{CheckPointPackingSlip: true, CheckPointInvoice: true}

// blockingCheckPoints place a CREDIT hold on a failed check. entry is advisory
// and release only records the position, so a set without a blocking point
// would switch credit control off; turning credit control off for a customer
// is limit type NONE on the profile, not the check points. packing_slip and
// invoice join this set when their waves wire them.
var blockingCheckPoints = map[string]bool{CheckPointConfirm: true}

// ChecksAt reports whether the credit rules run at point.
func (p Parameters) ChecksAt(point string) bool {
	for _, c := range p.CreditCheckPoints {
		if c == point {
			return true
		}
	}
	return false
}

// parameterOrderTypes are the allowed default_order_type values. RETURNED is
// an order type but not an allowed default (PRD 5.27.13).
var parameterOrderTypes = map[string]bool{OrderTypeJournal: true, "ORDER": true, "SUBSCRIPTION": true}

// Validate returns the field errors of p keyed by JSON name.
func (p Parameters) Validate() map[string]string {
	f := map[string]string{}
	if p.QuotationExpiryDays < 1 || p.QuotationExpiryDays > MaxQuotationExpiryDays {
		f["quotationExpiryDays"] = fmt.Sprintf("must be 1 to %d", MaxQuotationExpiryDays)
	}
	if err := ValidateProbability(p.QuotationProbabilityDefault); err != nil {
		f["quotationProbabilityDefault"] = err.Error()
	}
	if p.CreditReleaseValidityDays < 1 || p.CreditReleaseValidityDays > MaxCreditReleaseValidityDays {
		f["creditReleaseValidityDays"] = fmt.Sprintf("must be 1 to %d", MaxCreditReleaseValidityDays)
	}
	if p.CreditHoldSLABusinessDays < 1 || p.CreditHoldSLABusinessDays > MaxCreditHoldSLABusinessDays {
		f["creditHoldSlaBusinessDays"] = fmt.Sprintf("must be 1 to %d", MaxCreditHoldSLABusinessDays)
	}
	if !parameterOrderTypes[p.DefaultOrderType] {
		f["defaultOrderType"] = "must be JOURNAL, ORDER or SUBSCRIPTION"
	}
	if p.IntakeDedupWindowDays < 1 || p.IntakeDedupWindowDays > MaxIntakeDedupWindowDays {
		f["intakeDedupWindowDays"] = fmt.Sprintf("must be 1 to %d", MaxIntakeDedupWindowDays)
	}
	// An empty control is the default, NONE.
	if msg := ValidateDeliveryControl(p.DeliveryDateControlDefault); msg != "" && p.DeliveryDateControlDefault != "" {
		f["deliveryDateControlDefault"] = msg
	}
	if p.SalesLeadTimeDays < 0 || p.SalesLeadTimeDays > MaxSalesLeadTimeDays {
		f["salesLeadTimeDays"] = fmt.Sprintf("must be 0 to %d", MaxSalesLeadTimeDays)
	}
	if msg := validateCheckPoints(p.CreditCheckPoints); msg != "" {
		f["creditCheckPoints"] = msg
	}
	if (p.OneTimeOrderLimit == nil) != (p.OneTimeOrderLimitCurrency == nil) {
		f["oneTimeOrderLimit"] = "oneTimeOrderLimit and oneTimeOrderLimitCurrency are set together"
	} else if p.OneTimeOrderLimit != nil {
		if p.OneTimeOrderLimit.IsNegative() || !p.OneTimeOrderLimit.Round(oneTimeLimitScale).Equal(*p.OneTimeOrderLimit) {
			f["oneTimeOrderLimit"] = "must be 0 or more with at most 2 decimal places"
		}
		if !currencyAlpha3.MatchString(*p.OneTimeOrderLimitCurrency) {
			f["oneTimeOrderLimitCurrency"] = "must be an ISO 4217 alpha-3 code"
		}
	}
	return f
}

// AddBusinessDays returns t moved forward by n Monday-to-Friday days, keeping
// the time of day (the release SLA of a hold, FR-27.8). A start on a weekend
// counts from the following Monday's first day.
func AddBusinessDays(t time.Time, n int) time.Time {
	for n > 0 {
		t = t.AddDate(0, 0, 1)
		if wd := t.Weekday(); wd != time.Saturday && wd != time.Sunday {
			n--
		}
	}
	return t
}

// validateCheckPoints returns why points is not an allowed credit_check_points
// set, or "" when it is.
func validateCheckPoints(points []string) string {
	seen := map[string]bool{}
	blocking := false
	for _, c := range points {
		switch {
		case notActiveCheckPoints[c]:
			return c + " is not active yet: the packing slip and invoice credit checks are built with fulfillment and billing"
		case !activeCheckPoints[c]:
			return "must be entry, confirm or release"
		case seen[c]:
			return "must not repeat a check point"
		}
		seen[c] = true
		blocking = blocking || blockingCheckPoints[c]
	}
	if !blocking {
		return "must include confirm, the check point that places a credit hold; entry and release do not hold an order"
	}
	return ""
}
