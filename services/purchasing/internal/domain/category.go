package domain

import (
	"fmt"
	"sort"
	"time"
)

// Category access codes (PRD 3.17.18) and the category-setup codes of this service.
const (
	// CodeCategoryAccess (PRC-CAT-002): a vendor is not assigned to a restricted category.
	CodeCategoryAccess = "PRC-CAT-002"
	// CodeCategoryProvisional is the warning for a vendor whose assignment is PROVISIONAL.
	CodeCategoryProvisional = "PRC-CAT-010"
)

// Category access modes (policy category_access_mode, PRD 3.17.10).
const (
	AccessHard = "HARD"
	AccessSoft = "SOFT"
	AccessOff  = "OFF"
)

// Vendor assignment statuses (FR-17.20).
const (
	AssignmentApproved    = "APPROVED"
	AssignmentProvisional = "PROVISIONAL"
	AssignmentBlocked     = "BLOCKED"
)

// ValidAccessMode reports whether m is HARD, SOFT or OFF.
func ValidAccessMode(m string) bool { return m == AccessHard || m == AccessSoft || m == AccessOff }

// ValidAssignmentStatus reports whether s is a vendor assignment status.
func ValidAssignmentStatus(s string) bool {
	return s == AssignmentApproved || s == AssignmentProvisional || s == AssignmentBlocked
}

// CategoryVendor assigns a vendor to a procurement category of a legal entity for a
// validity window (FR-17.20). ValidTo is open when nil.
type CategoryVendor struct {
	ID            string    `json:"id"`
	LegalEntityID string    `json:"legalEntityId"`
	Category      string    `json:"category"`
	VendorID      string    `json:"vendorId"`
	Status        string    `json:"status"`
	ValidFrom     Day       `json:"validFrom"`
	ValidTo       *Day      `json:"validTo,omitempty"`
	Notes         string    `json:"notes,omitempty"`
	Version       int       `json:"version"`
	CreatedBy     string    `json:"createdBy,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedBy     string    `json:"updatedBy,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// ValidOn reports whether the validity window holds d.
func (c CategoryVendor) ValidOn(d Day) bool {
	if d.Before(c.ValidFrom.Time) {
		return false
	}
	return c.ValidTo == nil || !d.After(c.ValidTo.Time)
}

// CategoryManager names a worker who manages a procurement category (FR-17.20).
type CategoryManager struct {
	ID            string    `json:"id"`
	LegalEntityID string    `json:"legalEntityId"`
	Category      string    `json:"category"`
	WorkerID      string    `json:"workerId"`
	CreatedBy     string    `json:"createdBy,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
}

// AccessUse is one requisition or PO line to check: the vendor it would buy from in a category.
// Ref names it in messages (for example "requisition PR-1 line 2").
type AccessUse struct {
	LineNo   int
	Ref      string
	Category string
	VendorID string
}

// AccessFinding is one use the category access rule did not accept as it stands.
type AccessFinding struct {
	Use    AccessUse
	Code   string
	Detail string
}

// CheckCategoryAccess applies the category access rule (FR-17.20) to uses on day d.
// A category is restricted when it has at least one APPROVED or PROVISIONAL assignment,
// whatever its window (an approved-vendor list whose rows have all expired still restricts);
// a category with only BLOCKED assignments, or none, is open. A BLOCKED assignment whose
// window holds d is a violation in every category, open or restricted: it blocks that one
// vendor, never the others. Outside its window a BLOCKED row is not in force and the vendor
// counts as unassigned (open category: passes; restricted: violation). In a restricted
// category the vendor needs an APPROVED assignment valid on d: PROVISIONAL passes with a
// warning, and expired, not yet valid or missing is a violation. A use with no category or
// no vendor is not checked.
// HARD returns the violations as an error (PRC-CAT-002, the first in line order, naming
// every one); SOFT turns them into warnings; OFF checks nothing. An empty mode is SOFT.
// assignments may hold other categories; only those of the uses matter.
func CheckCategoryAccess(mode string, assignments []CategoryVendor, uses []AccessUse, d Day) ([]AccessFinding, error) {
	if mode == "" {
		mode = AccessSoft
	}
	if mode == AccessOff {
		return nil, nil
	}
	byCategory := map[string][]CategoryVendor{}
	restricted := map[string]bool{}
	for _, a := range assignments {
		byCategory[a.Category] = append(byCategory[a.Category], a)
		if a.Status != AssignmentBlocked {
			restricted[a.Category] = true
		}
	}
	var warnings, violations []AccessFinding
	for _, u := range uses {
		rows := byCategory[u.Category]
		if u.Category == "" || u.VendorID == "" || len(rows) == 0 {
			continue
		}
		status := ""
		for _, a := range rows {
			if a.VendorID == u.VendorID && a.ValidOn(d) {
				status = a.Status
			}
		}
		if status != AssignmentBlocked && !restricted[u.Category] {
			continue
		}
		switch status {
		case AssignmentApproved:
		case AssignmentProvisional:
			warnings = append(warnings, AccessFinding{Use: u, Code: CodeCategoryProvisional,
				Detail: fmt.Sprintf("vendor %s is only provisionally assigned to category %s", u.VendorID, u.Category)})
		default:
			why := "is not assigned to"
			if status == AssignmentBlocked {
				why = "is blocked for"
			}
			violations = append(violations, AccessFinding{Use: u, Code: CodeCategoryAccess,
				Detail: fmt.Sprintf("%s: vendor %s %s category %s", u.Ref, u.VendorID, why, u.Category)})
		}
	}
	sort.SliceStable(violations, func(i, j int) bool { return violations[i].Use.LineNo < violations[j].Use.LineNo })
	if len(violations) == 0 {
		return warnings, nil
	}
	if mode == AccessHard {
		detail := violations[0].Detail
		if len(violations) > 1 {
			detail += fmt.Sprintf(" (and %d more)", len(violations)-1)
		}
		return nil, fail(CodeCategoryAccess, "%s", detail+"; choose an assigned vendor or request a provisional assignment")
	}
	return append(warnings, violations...), nil
}
