package domain

import "time"

// Minimal collection cases (FR-30.18). The wave 45 case framework replaces them;
// these fields are a subset of its schema. Pure functions, no I/O.
const (
	CaseCategoryCollection = "COLLECTION"
	CaseCategoryDispute    = "DISPUTE"
	CaseCategoryDeduction  = "DEDUCTION"
	CaseOpen               = "OPEN"
	CaseClosed             = "CLOSED"
	CasePriorityNormal     = "NORMAL"

	CodeCaseState    = "AR-CASE-STATE"
	CodeCaseMismatch = "AR-CASE-MISMATCH"
)

// caseSLAHours are the §5.30.13 case_sla_hours defaults per category.
var caseSLAHours = map[string]int{CaseCategoryCollection: 72, CaseCategoryDispute: 120, CaseCategoryDeduction: 240}

// ValidCaseCategory reports whether c is COLLECTION, DISPUTE or DEDUCTION.
func ValidCaseCategory(c string) bool { _, ok := caseSLAHours[c]; return ok }

// ValidCasePriority reports whether p is LOW, NORMAL or HIGH.
func ValidCasePriority(p string) bool { return p == "LOW" || p == CasePriorityNormal || p == "HIGH" }

// CaseSLAHours returns the default SLA in hours for a category.
func CaseSLAHours(category string) (int, bool) {
	h, ok := caseSLAHours[category]
	return h, ok
}

// CaseCanChange reports whether a case in status can still be assigned or closed.
func CaseCanChange(status string) bool { return status == CaseOpen }

// CaseSLABreached reports whether an open case is past its SLA deadline at now (AR-0018).
func CaseSLABreached(status string, due, now time.Time) bool {
	return status == CaseOpen && now.After(due)
}
