package domain

import (
	"errors"

	"github.com/shopspring/decimal"
)

// Supplementary requirement and pricing (FR-27.14, AX PriceDiscSuppItem).
const (
	SuppMandatory = "MANDATORY"
	SuppOptional  = "OPTIONAL"
	SuppFree      = "FREE"
	SuppPriced    = "PRICED"
)

// SupplementaryRule adds supplementary lines for a parent product. CustomerID
// nil means every customer of the legal entity. The quantity is
// QuantityPerBase for each whole BaseQuantity of the parent line (AC-27.10:
// 1 per 10, so 25 gives 2).
type SupplementaryRule struct {
	ID                   string          `json:"id"`
	LegalEntityID        string          `json:"legalEntityId"`
	ParentProductID      string          `json:"parentProductId"`
	CustomerID           *string         `json:"customerId"`
	SupplementaryProduct string          `json:"supplementaryProductId"`
	Unit                 string          `json:"unit"`
	Requirement          string          `json:"requirement"`
	BaseQuantity         decimal.Decimal `json:"baseQuantity"`
	QuantityPerBase      decimal.Decimal `json:"quantityPerBase"`
	Pricing              string          `json:"pricing"`
	UnitPrice            decimal.Decimal `json:"unitPrice"`
}

// SupplementaryLine is one supplementary line a parent line triggers.
// SupplementaryOf is the parent product; FREE lines carry unit price zero.
type SupplementaryLine struct {
	RuleID          string          `json:"ruleId"`
	ProductID       string          `json:"productId"`
	Unit            string          `json:"unit"`
	Requirement     string          `json:"requirement"`
	Quantity        decimal.Decimal `json:"quantity"`
	UnitPrice       decimal.Decimal `json:"unitPrice"`
	SupplementaryOf string          `json:"supplementaryOf"`
}

// ValidateSupplementaryRule checks a rule before it is stored. Field errors are
// keyed by JSON name.
func ValidateSupplementaryRule(r SupplementaryRule) map[string]string {
	f := map[string]string{}
	if r.Requirement != SuppMandatory && r.Requirement != SuppOptional {
		f["requirement"] = "must be MANDATORY or OPTIONAL"
	}
	if r.Pricing != SuppFree && r.Pricing != SuppPriced {
		f["pricing"] = "must be FREE or PRICED"
	}
	if n := len(r.Unit); n == 0 || n > 10 {
		f["unit"] = "must be 1 to 10 characters"
	}
	if err := ValidateQuantity(r.BaseQuantity); err != nil {
		f["baseQuantity"] = err.Error()
	}
	if err := ValidateQuantity(r.QuantityPerBase); err != nil {
		f["quantityPerBase"] = err.Error()
	}
	switch {
	case r.UnitPrice.IsNegative():
		f["unitPrice"] = "must not be negative"
	case r.Pricing == SuppFree && !r.UnitPrice.IsZero():
		f["unitPrice"] = "must be zero for FREE items"
	case r.Pricing == SuppPriced && !r.UnitPrice.IsPositive():
		f["unitPrice"] = "must be greater than zero for PRICED items"
	}
	if r.ParentProductID == r.SupplementaryProduct {
		f["supplementaryProductId"] = "must differ from the parent product"
	}
	return f
}

// ErrNoParent is returned when evaluation is asked for a non-positive parent
// quantity.
var ErrNoParent = errors.New("parent quantity must be greater than zero")

// EvaluateSupplementary returns the supplementary lines a parent line of qty
// units of parentProductID for customerID triggers. For each supplementary
// product a rule for the customer takes precedence over the all-customers
// rule. Rules whose parent quantity is below one base quantity produce no line.
// Lines keep the order of the rules that produced them.
func EvaluateSupplementary(rules []SupplementaryRule, parentProductID, customerID string, qty decimal.Decimal) ([]SupplementaryLine, error) {
	if !qty.IsPositive() {
		return nil, ErrNoParent
	}
	hasCustomerRule := map[string]bool{}
	for _, r := range rules {
		if r.ParentProductID == parentProductID && r.CustomerID != nil && customerID != "" && *r.CustomerID == customerID {
			hasCustomerRule[r.SupplementaryProduct] = true
		}
	}
	out := []SupplementaryLine{}
	for _, r := range rules {
		if r.ParentProductID != parentProductID {
			continue
		}
		if r.CustomerID != nil && *r.CustomerID != customerID {
			continue
		}
		if r.CustomerID == nil && hasCustomerRule[r.SupplementaryProduct] {
			continue
		}
		multiples := qty.Div(r.BaseQuantity).Floor()
		if !multiples.IsPositive() {
			continue
		}
		price := decimal.Zero
		if r.Pricing == SuppPriced {
			price = r.UnitPrice
		}
		out = append(out, SupplementaryLine{RuleID: r.ID, ProductID: r.SupplementaryProduct, Unit: r.Unit,
			Requirement: r.Requirement, Quantity: multiples.Mul(r.QuantityPerBase), UnitPrice: price,
			SupplementaryOf: parentProductID})
	}
	return out, nil
}
