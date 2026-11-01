package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// DayLayout is the date form of rule validity days and requested dates.
const DayLayout = "2006-01-02"

// AlternativeRule offers AlternativeProductID in place of OrderedProductID
// (FR-27.13). CustomerID nil means every customer of the legal entity.
// Priority 1 is offered first. ValidFrom and ValidTo are optional YYYY-MM-DD
// days, inclusive, judged on the requested delivery date.
type AlternativeRule struct {
	ID                   string  `json:"id"`
	LegalEntityID        string  `json:"legalEntityId"`
	OrderedProductID     string  `json:"orderedProductId"`
	AlternativeProductID string  `json:"alternativeProductId"`
	CustomerID           *string `json:"customerId,omitempty"`
	Priority             int     `json:"priority"`
	ValidFrom            *string `json:"validFrom,omitempty"`
	ValidTo              *string `json:"validTo,omitempty"`
}

// AlternativeOffer is one alternative product with its ATP against the request.
// AvailableOnDate is the quantity (at most the requested one) that can ship by
// the requested date; CoversQuantity says it is all of it.
type AlternativeOffer struct {
	RuleID               string          `json:"ruleId"`
	AlternativeProductID string          `json:"alternativeProductId"`
	Priority             int             `json:"priority"`
	AvailableOnDate      decimal.Decimal `json:"availableOnDate"`
	CoversQuantity       bool            `json:"coversQuantity"`
	Promise              Promise         `json:"-"`
}

// ValidateAlternativeRule checks a rule before it is stored. Field errors are
// keyed by JSON name.
func ValidateAlternativeRule(r AlternativeRule) map[string]string {
	f := map[string]string{}
	if r.OrderedProductID == "" {
		f["orderedProductId"] = "is required"
	}
	if r.AlternativeProductID == "" {
		f["alternativeProductId"] = "is required"
	}
	if r.OrderedProductID != "" && r.OrderedProductID == r.AlternativeProductID {
		f["alternativeProductId"] = "must differ from the ordered product"
	}
	if r.Priority < 1 || r.Priority > 99 {
		f["priority"] = "must be 1 to 99"
	}
	var from, to time.Time
	var errFrom, errTo error
	if r.ValidFrom != nil {
		if from, errFrom = time.Parse(DayLayout, *r.ValidFrom); errFrom != nil {
			f["validFrom"] = "must be a date such as 2027-03-01"
		}
	}
	if r.ValidTo != nil {
		if to, errTo = time.Parse(DayLayout, *r.ValidTo); errTo != nil {
			f["validTo"] = "must be a date such as 2027-03-01"
		}
	}
	if r.ValidFrom != nil && r.ValidTo != nil && errFrom == nil && errTo == nil && to.Before(from) {
		f["validTo"] = "must not be before validFrom"
	}
	return f
}

// appliesOn reports whether the rule's validity includes the day.
func (r AlternativeRule) appliesOn(day string) bool {
	return (r.ValidFrom == nil || *r.ValidFrom <= day) && (r.ValidTo == nil || day <= *r.ValidTo)
}

// ApplicableAlternatives returns the rules that offer an alternative for
// orderedProductID to customerID on the requested day: the rule is for every
// customer or for customerID, and its validity includes the day. For one
// alternative product the customer's own rule wins over the rule for every
// customer. The result is ordered by priority, then by the customer's own rule
// before the general one, then by rule id.
func ApplicableAlternatives(rules []AlternativeRule, customerID, orderedProductID string, requested time.Time) []AlternativeRule {
	day := requested.Format(DayLayout)
	chosen := map[string]AlternativeRule{}
	for _, r := range rules {
		if r.OrderedProductID != orderedProductID || !r.appliesOn(day) {
			continue
		}
		own := r.CustomerID != nil
		if own && (customerID == "" || *r.CustomerID != customerID) {
			continue
		}
		prev, seen := chosen[r.AlternativeProductID]
		if !seen || own && prev.CustomerID == nil {
			chosen[r.AlternativeProductID] = r
		}
	}
	out := make([]AlternativeRule, 0, len(chosen))
	for _, r := range chosen {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if (a.CustomerID != nil) != (b.CustomerID != nil) {
			return a.CustomerID != nil
		}
		return a.ID < b.ID
	})
	return out
}

// AvailableOnDate is the part of the promise that ships on or before the
// requested day (a bucket dated after it is later supply).
func AvailableOnDate(p Promise, requested time.Time) decimal.Decimal {
	sum := decimal.Zero
	for _, s := range p.Schedule {
		if !s.Date.After(requested) {
			sum = sum.Add(s.Quantity)
		}
	}
	return sum
}

// OfferAlternatives places qty of each applicable alternative against its own
// ATP buckets (supplies, by alternative product id) and returns the offers
// that can ship something by the requested day, plus the rules whose product
// came without buckets and so could not be checked. Offers that cover the whole
// quantity come first, then by priority, then by quantity available, then by
// rule id. An alternative whose buckets cannot be placed is left out.
func OfferAlternatives(rules []AlternativeRule, supplies map[string][]Period, requested time.Time, qty decimal.Decimal) (offers []AlternativeOffer, unchecked []AlternativeRule) {
	offers = []AlternativeOffer{}
	unchecked = []AlternativeRule{}
	for _, r := range rules {
		periods, ok := supplies[r.AlternativeProductID]
		if !ok || len(periods) == 0 {
			unchecked = append(unchecked, r)
			continue
		}
		p, err := PromiseDate(periods, requested, qty, false)
		if err != nil {
			continue
		}
		avail := AvailableOnDate(p, requested)
		if !avail.IsPositive() {
			continue
		}
		offers = append(offers, AlternativeOffer{RuleID: r.ID, AlternativeProductID: r.AlternativeProductID, Priority: r.Priority,
			AvailableOnDate: avail, CoversQuantity: avail.GreaterThanOrEqual(qty), Promise: p})
	}
	sort.SliceStable(offers, func(i, j int) bool {
		a, b := offers[i], offers[j]
		if a.CoversQuantity != b.CoversQuantity {
			return a.CoversQuantity
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if c := a.AvailableOnDate.Cmp(b.AvailableOnDate); c != 0 {
			return c > 0
		}
		return a.RuleID < b.RuleID
	})
	return offers, unchecked
}
