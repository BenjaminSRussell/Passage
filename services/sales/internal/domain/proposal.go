package domain

// Product proposal reasons (FR-27.21, SAP VB41 cross-selling and dynamic
// product proposal). Proposals are shown at line commit; they create no line
// unless the user accepts one.
const (
	ProposalCrossSell = "CROSS_SELL"
	ProposalUpSell    = "UP_SELL"
	ProposalAccessory = "ACCESSORY"
)

// ProposalReasons lists the accepted reasons in display order.
var ProposalReasons = []string{ProposalCrossSell, ProposalUpSell, ProposalAccessory}

// ProposalRule proposes ProposedProductID when OrderedProductID is ordered.
// CustomerID nil means every customer of the legal entity. AcceptedCount and
// DeclinedCount are the responses recorded against the rule, from which the
// acceptance rate is read (FR-27.21 "acceptance rate recorded for analytics").
type ProposalRule struct {
	ID                string  `json:"id"`
	LegalEntityID     string  `json:"legalEntityId"`
	OrderedProductID  string  `json:"orderedProductId"`
	ProposedProductID string  `json:"proposedProductId"`
	CustomerID        *string `json:"customerId,omitempty"`
	Reason            string  `json:"reason"`
	AcceptedCount     int     `json:"acceptedCount"`
	DeclinedCount     int     `json:"declinedCount"`
}

// Proposal is one product offered at line commit.
type Proposal struct {
	RuleID            string `json:"ruleId"`
	OrderedProductID  string `json:"orderedProductId"`
	ProposedProductID string `json:"proposedProductId"`
	Reason            string `json:"reason"`
}

// ValidateProposalRule checks a rule before it is stored. Field errors are
// keyed by JSON name.
func ValidateProposalRule(r ProposalRule) map[string]string {
	f := map[string]string{}
	switch r.Reason {
	case ProposalCrossSell, ProposalUpSell, ProposalAccessory:
	default:
		f["reason"] = "must be CROSS_SELL, UP_SELL or ACCESSORY"
	}
	if r.OrderedProductID == "" {
		f["orderedProductId"] = "is required"
	}
	if r.ProposedProductID == "" {
		f["proposedProductId"] = "is required"
	}
	if r.OrderedProductID != "" && r.OrderedProductID == r.ProposedProductID {
		f["proposedProductId"] = "must differ from the ordered product"
	}
	return f
}

// EvaluateProposals returns the products to propose when committingProductIDs
// are committed on an order that already holds currentProductIDs (FR-27.21).
// A rule applies when its ordered product is being committed and it is for
// every customer or for customerID. A product already on the order, being
// committed, or already proposed is not offered again. For one proposed
// product the customer's own rule wins over the rule for every customer, as
// for supplementary items; otherwise the first rule in rules order names it.
// Proposals follow the order of the committing products, then of rules.
func EvaluateProposals(rules []ProposalRule, customerID string, committingProductIDs, currentProductIDs []string) []Proposal {
	excluded := map[string]bool{}
	committing := map[string]bool{}
	for _, id := range committingProductIDs {
		excluded[id] = true
		committing[id] = true
	}
	for _, id := range currentProductIDs {
		excluded[id] = true
	}
	// The rule each proposed product is offered under.
	chosen := map[string]ProposalRule{}
	for _, r := range rules {
		if !committing[r.OrderedProductID] || excluded[r.ProposedProductID] {
			continue
		}
		own := r.CustomerID != nil
		if own && (customerID == "" || *r.CustomerID != customerID) {
			continue
		}
		prev, seen := chosen[r.ProposedProductID]
		if !seen || own && prev.CustomerID == nil {
			chosen[r.ProposedProductID] = r
		}
	}
	out := []Proposal{}
	for _, ordered := range committingProductIDs {
		for _, r := range rules {
			c, ok := chosen[r.ProposedProductID]
			if !ok || c.ID != r.ID || r.OrderedProductID != ordered {
				continue
			}
			delete(chosen, r.ProposedProductID)
			out = append(out, Proposal{RuleID: r.ID, OrderedProductID: ordered, ProposedProductID: r.ProposedProductID, Reason: r.Reason})
		}
	}
	return out
}
