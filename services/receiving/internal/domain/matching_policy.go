package domain

import (
	"fmt"
	"strings"
)

const (
	CodeExceptionNotOpen     = "AP-MPX-001"
	CodeExceptionNotAssignee = "AP-MPX-002"

	AuditReasonMatchException = "match exception"
	ExceptionOpen             = "OPEN"
	ExceptionApproved         = "APPROVED"
	ExceptionRejected         = "REJECTED"
)

type MatchingPolicy struct {
	ID            string
	LegalEntityID string
	VendorID      string
	ItemID        string
	OwnerUserID   string
	Policy        Policy
}

type MatchException struct {
	Assignee string
	Reasons  string
}

func (m MatchingPolicy) specificity() int {
	n := 0
	if m.VendorID != "" {
		n += 2
	}
	if m.ItemID != "" {
		n++
	}
	return n
}

func (m MatchingPolicy) covers(vendorID, itemID string) bool {
	return (m.VendorID == "" || strings.EqualFold(m.VendorID, vendorID)) && (m.ItemID == "" || strings.EqualFold(m.ItemID, itemID))
}

func ResolvePolicy(policies []MatchingPolicy, vendorID, itemID string) (MatchingPolicy, bool) {
	var best MatchingPolicy
	found := false
	for _, p := range policies {
		if p.covers(vendorID, itemID) && (!found || p.specificity() > best.specificity()) {
			best, found = p, true
		}
	}
	return best, found
}

func IsVariance(s Status) bool {
	return s == PriceVariance || s == QtyVariance || s == TotalVariance
}

func ExceptionReasons(res Result) string {
	var reasons []string
	for i, l := range res.Lines {
		if IsVariance(l.Status) {
			reasons = append(reasons, fmt.Sprintf("line %d: %s", i+1, l.Status))
		}
	}
	if res.Status == TotalVariance {
		reasons = append(reasons, "total: "+string(TotalVariance))
	}
	return strings.Join(reasons, "; ")
}
