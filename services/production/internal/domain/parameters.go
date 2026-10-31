package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// CodeAsBuiltRequired refuses `:end` of an order whose site requires the as-built
// genealogy (PRD 4.21.13 "As-built required", FR-21.19) when none is recorded.
const CodeAsBuiltRequired = "PRODUCTION_AS_BUILT_REQUIRED"

// Parameters are the production parameters of a site (PRD 4.21.13): the settings
// the journal, report-as-finished, split, trigger-point and end rules read. A stored
// row applies to one site, or to every site under AllKey; with none stored the
// service defaults (its environment) apply, so a plant that never opens the setup
// page behaves as before.
type Parameters struct {
	SiteID string `json:"siteId"`
	// RAFTolerancePct is the over-delivery allowed on report as finished, 0 to 100.
	RAFTolerancePct decimal.Decimal `json:"rafTolerancePct"`
	// OverConsumptionPct is the picking beyond the required quantity allowed without
	// the over-consumption duty, 0 to 1000.
	OverConsumptionPct decimal.Decimal `json:"overConsumptionPct"`
	// AutoStartOnFeedback lets a route card start a Released order.
	AutoStartOnFeedback bool `json:"autoStartOnFeedback"`
	// EstimateOnCreate estimates a new order in its create transaction.
	EstimateOnCreate bool `json:"estimateOnCreate"`
	// SplitAllocation is the allocation a split of a Started order uses when the
	// request names none (BR-21.11).
	SplitAllocation Allocation `json:"splitAllocation"`
	// TriggerPointsEnabled switches the evaluation of the order's trigger points
	// on route-card feedback (FR-21.17).
	TriggerPointsEnabled bool `json:"triggerPointsEnabled"`
	// AsBuiltRequired makes `:end` refuse an order that reported output without any
	// as-built record (FR-21.19).
	AsBuiltRequired bool `json:"asBuiltRequired"`
}

var (
	hundredPct   = decimal.NewFromInt(100)
	thousandPct  = decimal.NewFromInt(1000)
	maxParamPlac = int32(2)
)

// Validate returns the fields of p that are out of range, keyed by JSON name.
func (p Parameters) Validate() map[string]string {
	fields := map[string]string{}
	if p.SiteID == "" {
		fields["siteId"] = "is required (a site UUID or All)"
	}
	if p.RAFTolerancePct.IsNegative() || p.RAFTolerancePct.GreaterThan(hundredPct) {
		fields["rafTolerancePct"] = "must be between 0 and 100"
	} else if !p.RAFTolerancePct.Equal(p.RAFTolerancePct.Round(maxParamPlac)) {
		fields["rafTolerancePct"] = "at most 2 decimal places"
	}
	if p.OverConsumptionPct.IsNegative() || p.OverConsumptionPct.GreaterThan(thousandPct) {
		fields["overConsumptionPct"] = "must be between 0 and 1000"
	} else if !p.OverConsumptionPct.Equal(p.OverConsumptionPct.Round(maxParamPlac)) {
		fields["overConsumptionPct"] = "at most 2 decimal places"
	}
	if !p.SplitAllocation.Valid() {
		fields["splitAllocation"] = "must be proportional or keep-on-original"
	}
	return fields
}

// ResolveParameters picks the stored parameters of the site, else the ones under
// AllKey, else defaults. stored reports whether a stored row applied.
func ResolveParameters(rows []Parameters, siteID string, defaults Parameters) (p Parameters, stored bool) {
	for _, want := range []string{siteID, AllKey} {
		for _, r := range rows {
			if r.SiteID == want {
				return r, true
			}
		}
	}
	defaults.SiteID = siteID
	if defaults.SplitAllocation == "" {
		defaults.SplitAllocation = AllocProportional
	}
	return defaults, false
}

// CheckAsBuiltRequired enforces "As-built required" at End: an order that reported
// finished output needs at least one attached as-built record. Per-serial
// completeness waits for the tracking dimensions of wave 16.
func CheckAsBuiltRequired(required bool, qtyReported decimal.Decimal, attached int) error {
	if !required || !qtyReported.IsPositive() || attached > 0 {
		return nil
	}
	return &Error{Code: CodeAsBuiltRequired, Detail: fmt.Sprintf("cannot end: %s units were reported finished but no as-built record is attached", qtyReported)}
}
