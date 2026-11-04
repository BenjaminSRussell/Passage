package domain

import (
	"fmt"
	"regexp"
	"sort"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// MaxApprovers is the size of a customer's named approver group.
const MaxApprovers = 20

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

// ApprovalPolicy is how changes to one customer's confirmed orders are
// approved (FR-27.9). Required is the slice 13 switch; Threshold, when set,
// lets changes through without approval while the value they move stays below
// that amount in ThresholdCurrency; Approvers, when not empty, names the only
// users who may decide a request or change a confirmed order directly (they
// still need the approve duty). Approvers are canonical identity user ids.
type ApprovalPolicy struct {
	Required          bool
	Threshold         *decimal.Decimal
	ThresholdCurrency string
	Approvers         []string
}

// NeedsApproval reports whether a change must wait for approval. moved is
// what the changes applied to the order without approval have already moved,
// change what this one moves (both are sizes, never negative; a cancellation
// passes the whole total). The two are added, so a large change split into
// several small ones still reaches the threshold. Without a threshold every
// change needs approval. An order in another currency than the threshold has
// no rate to convert with (w12), so it needs approval rather than slipping
// under the limit.
func (p ApprovalPolicy) NeedsApproval(moved, change decimal.Decimal, orderCurrency string) bool {
	if !p.Required {
		return false
	}
	if p.Threshold == nil || p.ThresholdCurrency != orderCurrency {
		return true
	}
	return moved.Abs().Add(change.Abs()).GreaterThanOrEqual(*p.Threshold)
}

// UsesThreshold reports whether changes of an order in orderCurrency are
// measured against the threshold (and so count towards the moved value).
func (p ApprovalPolicy) UsesThreshold(orderCurrency string) bool {
	return p.Required && p.Threshold != nil && p.ThresholdCurrency == orderCurrency
}

// MayDecide reports whether user may approve or reject: always when no group is
// named, otherwise only a member. User ids compare in canonical UUID form.
func (p ApprovalPolicy) MayDecide(user string) bool {
	if len(p.Approvers) == 0 {
		return true
	}
	u, ok := ids.Canonical(user)
	if !ok {
		return false
	}
	for _, a := range p.Approvers {
		if a == u {
			return true
		}
	}
	return false
}

// NormalizeApprovers puts each user id in canonical UUID form, drops repeats
// and sorts the group. An id that is not a UUID is returned in invalid, so
// the caller can refuse the policy rather than store a member nobody can be.
func NormalizeApprovers(in []string) (out, invalid []string) {
	seen := map[string]bool{}
	out = []string{}
	for _, a := range in {
		c, ok := ids.Canonical(a)
		if !ok || c == ids.Nil {
			invalid = append(invalid, a)
			continue
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out, invalid
}

// ValidateApprovalPolicy returns the field errors of a policy: a positive
// threshold with at most four decimal places needs a three-letter currency
// (and a currency needs a threshold), and the group is at most MaxApprovers
// users.
func ValidateApprovalPolicy(p ApprovalPolicy) map[string]string {
	f := map[string]string{}
	switch {
	case p.Threshold == nil && p.ThresholdCurrency != "":
		f["approvalThreshold"] = "is required with thresholdCurrency"
	case p.Threshold != nil:
		if !p.Threshold.IsPositive() {
			f["approvalThreshold"] = "must be greater than 0"
		} else if !p.Threshold.Round(4).Equal(*p.Threshold) {
			f["approvalThreshold"] = "allows at most 4 decimal places"
		} else if p.Threshold.GreaterThanOrEqual(decimal.New(1, 15)) {
			f["approvalThreshold"] = "must be below 10^15"
		}
		if !currencyPattern.MatchString(p.ThresholdCurrency) {
			f["thresholdCurrency"] = "must be a three-letter ISO code such as USD"
		}
	}
	if len(p.Approvers) > MaxApprovers {
		f["approverIds"] = fmt.Sprintf("must name at most %d users", MaxApprovers)
	}
	return f
}
