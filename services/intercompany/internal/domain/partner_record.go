package domain

import (
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Codes raised by the stored pairings (FR-40.1). They are not in the PRD
// message catalog; NOT-IMPLEMENTED.md lists them for the catalog owner.
const (
	CodePairingExists    = "IC-0840"
	CodeIdempotencyReuse = "IC-0841"
	CodePartnerNotFound  = "IC-0842"
	CodeVersionConflict  = "IC-0843"
)

// MaxCodeLength bounds entity codes and customer / vendor account numbers.
const MaxCodeLength = 64

// PartnerRecord is a stored pairing: the policy plus identity and audit
// fields. Version starts at 1 and increments on every policy change.
type PartnerRecord struct {
	ID        string
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
	Partner
}

// ValidatePairing checks a pairing before it is stored: the pricing and
// tolerance rules of ValidatePartner plus the customer and vendor accounts
// that make it a trading-partner pair (FR-40.1).
func ValidatePairing(p Partner) error {
	if err := ValidatePartner(p); err != nil {
		return err
	}
	for name, v := range map[string]string{
		"seller entity": p.SellerEntity, "buyer entity": p.BuyerEntity,
		"customer account": p.CustomerAccount, "vendor account": p.VendorAccount,
	} {
		switch {
		case strings.TrimSpace(v) == "":
			return invalid("the " + name + " is required")
		case len(v) > MaxCodeLength:
			return invalid("the " + name + " must be at most 64 characters")
		}
	}
	return nil
}

// PartnerPatch changes the policy of a stored pairing. Nil fields are kept.
// The direction (seller and buyer entity) never changes: a new direction is a
// new pairing.
type PartnerPatch struct {
	CustomerAccount   *string
	VendorAccount     *string
	Active            *bool
	Actions           *ActionPolicy
	Pricing           *PricingPolicy
	MarkupPct         *decimal.Decimal
	TolerancePricePct *decimal.Decimal
	ToleranceDateDays *int
}

// Apply returns the pairing with the patch applied and the names of the
// fields that changed, in a fixed order. An empty list means no change.
func (patch PartnerPatch) Apply(p Partner) (Partner, []string) {
	var changed []string
	if patch.CustomerAccount != nil && *patch.CustomerAccount != p.CustomerAccount {
		p.CustomerAccount = *patch.CustomerAccount
		changed = append(changed, "customerAccount")
	}
	if patch.VendorAccount != nil && *patch.VendorAccount != p.VendorAccount {
		p.VendorAccount = *patch.VendorAccount
		changed = append(changed, "vendorAccount")
	}
	if patch.Active != nil && *patch.Active != p.Active {
		p.Active = *patch.Active
		changed = append(changed, "active")
	}
	if patch.Actions != nil && *patch.Actions != p.Actions {
		p.Actions = *patch.Actions
		changed = append(changed, "actions")
	}
	if patch.Pricing != nil && *patch.Pricing != p.Pricing {
		p.Pricing = *patch.Pricing
		changed = append(changed, "pricing")
	}
	if patch.MarkupPct != nil && !patch.MarkupPct.Equal(p.MarkupPct) {
		p.MarkupPct = *patch.MarkupPct
		changed = append(changed, "markupPct")
	}
	tolerance := false
	if patch.TolerancePricePct != nil && !patch.TolerancePricePct.Equal(p.Tolerance.PricePct) {
		p.Tolerance.PricePct = *patch.TolerancePricePct
		tolerance = true
	}
	if patch.ToleranceDateDays != nil && *patch.ToleranceDateDays != p.Tolerance.DateDays {
		p.Tolerance.DateDays = *patch.ToleranceDateDays
		tolerance = true
	}
	if tolerance {
		changed = append(changed, "tolerance")
	}
	return p, changed
}
