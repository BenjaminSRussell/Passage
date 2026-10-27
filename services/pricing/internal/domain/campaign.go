package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// CodeCampaignNotFound is raised for an unknown campaign id, or a record naming one.
const CodeCampaignNotFound = "PRC-0041"

// Campaign groups condition records under one validity window, target customer group,
// budget and kill switch (FR-26.10; SAP VB22 deactivate, AX trade agreement campaigns).
// A record of a campaign takes part in resolution only while the campaign is live
// (CampaignLive) and, when the campaign has a target group, only for customers in it.
type Campaign struct {
	ID            string
	LegalEntityID string
	Name          string
	ValidFrom     time.Time
	ValidTo       time.Time
	// TargetCustomerGroup is the customer group the campaign is for; empty means every customer.
	TargetCustomerGroup string
	// Budget is the planned campaign spend in Currency. It is recorded, not enforced in
	// resolve: spend is known only from posted invoices (w29/w30).
	Budget   decimal.Decimal
	Currency string
	Killed   bool
	Version  int
}

// ValidateCampaign returns field errors for a campaign definition; an empty map means valid.
func ValidateCampaign(c Campaign) map[string]string {
	fields := map[string]string{}
	if len(c.Name) < 1 || len(c.Name) > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	switch {
	case c.ValidFrom.IsZero() || c.ValidTo.IsZero():
		fields["validity"] = "validFrom and validTo are required"
	case c.ValidTo.Before(c.ValidFrom):
		fields["validity"] = "validTo must not be before validFrom"
	}
	if c.Budget.IsNegative() {
		fields["budget"] = "must not be negative"
	}
	if !isCurrencyCode(c.Currency) {
		fields["currency"] = "must be an ISO 4217 code such as USD"
	}
	if len(c.TargetCustomerGroup) > 64 {
		fields["targetCustomerGroup"] = "must be at most 64 characters"
	}
	return fields
}

// CampaignLive reports whether a campaign's records take part in resolution on day:
// the kill switch is off and day falls inside the campaign's validity (inclusive).
func CampaignLive(c Campaign, day time.Time) bool {
	return !c.Killed && !day.Before(c.ValidFrom) && !day.After(c.ValidTo)
}

// CampaignTargets reports whether a campaign applies to a customer in customerGroup.
func CampaignTargets(c Campaign, customerGroup string) bool {
	return c.TargetCustomerGroup == "" || c.TargetCustomerGroup == customerGroup
}
