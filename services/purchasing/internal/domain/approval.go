package domain

import (
	"slices"

	"github.com/shopspring/decimal"
)

// Approver roles a band can require (FR-17.2). The concrete people are resolved
// by the approval workflow in wave 4; this slice only fixes the chain.
const (
	RoleManager       = "MANAGER"
	RoleCategoryOwner = "CATEGORY_OWNER"
	RoleCFO           = "CFO"
)

// DefaultPolicyVersion labels the built-in policy until the policy engine
// (FR-17.16) stores versions.
const DefaultPolicyVersion = "builtin-1"

// Band routes amounts up to and including UpTo (nil = no upper bound) to Chain.
type Band struct {
	UpTo  *decimal.Decimal
	Chain []string
}

// ApprovalPolicy is an ordered list of bands plus the categories that never
// auto-approve. An empty chain means the requisition is approved on submit.
type ApprovalPolicy struct {
	Bands            []Band
	NoAutoCategories map[string]bool
}

// DefaultApprovalPolicy is the FR-17.2 default: 0 to 1,000 auto-approves,
// 1,000 to 10,000 manager, 10,000 to 100,000 manager and category owner,
// above 100,000 manager, category owner and CFO.
func DefaultApprovalPolicy() ApprovalPolicy {
	return ApprovalPolicy{Bands: []Band{
		{UpTo: ptr(decimal.RequireFromString("1000"))},
		{UpTo: ptr(decimal.RequireFromString("10000")), Chain: []string{RoleManager}},
		{UpTo: ptr(decimal.RequireFromString("100000")), Chain: []string{RoleManager, RoleCategoryOwner}},
		{Chain: []string{RoleManager, RoleCategoryOwner, RoleCFO}},
	}}
}

// Route returns the approver chain for an amount in the requisition currency
// and a category. The first band that holds the amount wins.
func (p ApprovalPolicy) Route(amount decimal.Decimal, category string) []string {
	var chain []string
	for _, b := range p.Bands {
		if b.UpTo == nil || amount.LessThanOrEqual(*b.UpTo) {
			chain = slices.Clone(b.Chain)
			break
		}
	}
	if len(chain) == 0 && p.NoAutoCategories[category] {
		chain = []string{RoleManager}
	}
	return chain
}

func ptr[T any](v T) *T { return &v }
