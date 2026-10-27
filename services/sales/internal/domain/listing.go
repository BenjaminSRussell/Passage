package domain

import "time"

// Listing scopes and types (FR-27.15). One entity covers SAP VB01 listing and
// OV04 exclusion procedures.
const (
	ScopeCustomer = "CUSTOMER"
	ScopeGroup    = "GROUP"
	ListingAllow  = "ALLOW"
	ListingDeny   = "DENY"
)

// Listing decision reasons, in precedence order (BR-27.13).
const (
	ReasonCustomerDeny  = "CUSTOMER_DENY"
	ReasonCustomerAllow = "CUSTOMER_ALLOW"
	ReasonGroupDeny     = "GROUP_DENY"
	ReasonGroupAllow    = "GROUP_ALLOW"
	ReasonUnrestricted  = "UNRESTRICTED"
	ReasonNotListed     = "NOT_LISTED"
)

// ListingEntry is one product listing or exclusion. Nil bounds are open-ended.
type ListingEntry struct {
	ScopeType   string
	ScopeKey    string
	ProductID   string
	ListingType string
	ValidFrom   *time.Time
	ValidTo     *time.Time
}

// ListingDecision says whether a product may be ordered and why.
type ListingDecision struct {
	Allowed bool
	Reason  string
}

// validOn reports whether the entry is in force on the day d (inclusive bounds).
func (e ListingEntry) validOn(d time.Time) bool {
	if e.ValidFrom != nil && d.Before(*e.ValidFrom) {
		return false
	}
	if e.ValidTo != nil && d.After(*e.ValidTo) {
		return false
	}
	return true
}

// EvaluateListing applies BR-27.13 to one order line:
//  1. a DENY at customer level beats everything,
//  2. an ALLOW at customer level,
//  3. a DENY at group level,
//  4. an ALLOW at group level,
//  5. no ALLOW entry for the product at all means no restriction,
//  6. otherwise the product is not listed for this customer.
//
// Entries are only considered when valid on orderDate.
func EvaluateListing(entries []ListingEntry, customerID, customerGroup, productID string, orderDate time.Time) ListingDecision {
	var relevant []ListingEntry
	for _, e := range entries {
		if e.ProductID == productID && e.validOn(orderDate) {
			relevant = append(relevant, e)
		}
	}
	has := func(scope, key, typ string) bool {
		if key == "" {
			return false
		}
		for _, e := range relevant {
			if e.ScopeType == scope && e.ScopeKey == key && e.ListingType == typ {
				return true
			}
		}
		return false
	}
	anyAllow := false
	for _, e := range relevant {
		if e.ListingType == ListingAllow {
			anyAllow = true
			break
		}
	}
	switch {
	case has(ScopeCustomer, customerID, ListingDeny):
		return ListingDecision{Reason: ReasonCustomerDeny}
	case has(ScopeCustomer, customerID, ListingAllow):
		return ListingDecision{Allowed: true, Reason: ReasonCustomerAllow}
	case has(ScopeGroup, customerGroup, ListingDeny):
		return ListingDecision{Reason: ReasonGroupDeny}
	case has(ScopeGroup, customerGroup, ListingAllow):
		return ListingDecision{Allowed: true, Reason: ReasonGroupAllow}
	case !anyAllow:
		return ListingDecision{Allowed: true, Reason: ReasonUnrestricted}
	default:
		return ListingDecision{Reason: ReasonNotListed}
	}
}
