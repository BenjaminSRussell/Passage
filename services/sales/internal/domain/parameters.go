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

	// OneTimeOrderLimit and its currency are one_time_order_limit (FR-27.22);
	// both are set or both are absent, and absent means the currency defaults.
	OneTimeOrderLimit         *decimal.Decimal `json:"oneTimeOrderLimit,omitempty"`
	OneTimeOrderLimitCurrency *string          `json:"oneTimeOrderLimitCurrency,omitempty"`
}

const (
	// MaxCreditReleaseValidityDays bounds credit_release_validity_days (PRD 5.27.13).
	MaxCreditReleaseValidityDays = 30
	// DefaultCreditHoldSLABusinessDays is credit_hold_sla_business_days when
	// none is saved (FR-27.8).
	DefaultCreditHoldSLABusinessDays = 2
	// MaxCreditHoldSLABusinessDays bounds credit_hold_sla_business_days.
	MaxCreditHoldSLABusinessDays = 10
)

// DefaultParameters returns the settings used when none are stored.
func DefaultParameters() Parameters {
	return Parameters{QuotationExpiryDays: QuotationExpiryDays, QuotationProbabilityDefault: DefaultProbabilityPct,
		CreditReleaseValidityDays: CreditReleaseDays, CreditHoldSLABusinessDays: DefaultCreditHoldSLABusinessDays,
		DefaultOrderType: "ORDER"}
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
