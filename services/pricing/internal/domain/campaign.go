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
	// resolve: spend is known only from posted invoices (w29/w30). A revise may set it to any
	// amount at least 0.
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

// CampaignRevision is a change to a campaign at an expected version. Nil fields stay as they
// are; the currency never changes (a budget is in one currency).
type CampaignRevision struct {
	ID                  string
	Version             int
	Name                *string
	ValidFrom           *time.Time
	ValidTo             *time.Time
	TargetCustomerGroup *string
	Budget              *decimal.Decimal
}

// Apply returns c with the revision's fields set.
func (r CampaignRevision) Apply(c Campaign) Campaign {
	if r.Name != nil {
		c.Name = *r.Name
	}
	if r.ValidFrom != nil {
		c.ValidFrom = *r.ValidFrom
	}
	if r.ValidTo != nil {
		c.ValidTo = *r.ValidTo
	}
	if r.TargetCustomerGroup != nil {
		c.TargetCustomerGroup = *r.TargetCustomerGroup
	}
	if r.Budget != nil {
		c.Budget = *r.Budget
	}
	return c
}

// CampaignPastChanges returns field errors where a revision (cur to next) would change how a
// day before today priced: resolve stays reproducible for a past pricing date, so explain and
// any document priced on that day keep the campaign's answer (§5.26.4 invariant; BR-26.7 keeps
// posted invoices un-repriced). Once the campaign has started, its start date and target
// group are fixed and its end date may move only within today and later (ending it yesterday
// stops it now); a campaign that has not started may not start in the past. Kill and restore
// are the explicit exception (FR-26.10). An empty map means the revision keeps the past.
func CampaignPastChanges(cur, next Campaign, today time.Time) map[string]string {
	fields := map[string]string{}
	t, yesterday := dayOf(today), dayOf(today.AddDate(0, 0, -1))
	if dayOf(cur.ValidFrom) >= t {
		if dayOf(next.ValidFrom) < t {
			fields["validFrom"] = "must not be before today (" + t + "): a past day cannot join the campaign"
		}
		return fields
	}
	if dayOf(next.ValidFrom) != dayOf(cur.ValidFrom) {
		fields["validFrom"] = "cannot change: the campaign has priced since " + dayOf(cur.ValidFrom)
	}
	if min(dayOf(next.ValidTo), yesterday) != min(dayOf(cur.ValidTo), yesterday) {
		if dayOf(cur.ValidTo) < t {
			fields["validTo"] = "cannot change: the campaign ended on " + dayOf(cur.ValidTo)
		} else {
			fields["validTo"] = "must be " + yesterday + " or later: the days the campaign already priced stay in it"
		}
	}
	if next.TargetCustomerGroup != cur.TargetCustomerGroup {
		fields["targetCustomerGroup"] = "cannot change after the campaign started on " + dayOf(cur.ValidFrom) + "; create a new campaign"
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
