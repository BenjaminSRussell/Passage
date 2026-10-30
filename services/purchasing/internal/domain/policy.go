package domain

import "time"

// CodeFourEyes (PRC-POL-002) is raised when the policy requires a second person
// and a user who created, submitted or changed the PO tries to approve or
// confirm it (PRD 3.17.17 PO_FOUR_EYES). It is answered as 403.
const CodeFourEyes = "PRC-POL-002"

// Policy is the purchasing policy of one legal entity (FR-17.13). Each flag has
// the default of DefaultPolicy until a policy row is saved.
type Policy struct {
	LegalEntityID string `json:"legalEntityId"`
	// ChangeManagementDefault is the changeManagement value a new PO takes when
	// the request leaves it out.
	ChangeManagementDefault bool `json:"changeManagementDefault"`
	// POFourEyes keeps the users who created, submitted or changed a PO from
	// approving or confirming it.
	POFourEyes bool `json:"poFourEyes"`
	// AllowInvoiceBeforeReceipt lets a PO line quantity drop below the invoiced
	// quantity, never below the received quantity (BR-17.7).
	AllowInvoiceBeforeReceipt bool       `json:"allowInvoiceBeforeReceipt"`
	Version                   int        `json:"version"`
	UpdatedBy                 string     `json:"updatedBy,omitempty"`
	UpdatedAt                 *time.Time `json:"updatedAt,omitempty"`
}

// DefaultPolicy is what applies to a legal entity with no saved policy: change
// management on, no segregation, invoicing only against receipts. Version 0
// means "not saved yet"; the first save is If-Match 0.
func DefaultPolicy(legalEntityID string) Policy {
	return Policy{LegalEntityID: legalEntityID, ChangeManagementDefault: true}
}

// CheckFourEyes refuses an approve or confirm by a user who created, submitted
// or last changed the PO when the policy asks for four eyes. A user that is not
// recorded (a PO from before the columns existed) blocks no one. With the
// policy on, a caller with no user id is refused, since a second person cannot
// be shown.
func CheckFourEyes(p Policy, actorID string, involved ...string) error {
	if !p.POFourEyes {
		return nil
	}
	if actorID == "" {
		return fail(CodeFourEyes, "the four-eyes policy needs a named user to approve or confirm a purchase order")
	}
	for _, u := range involved {
		if u == actorID {
			return fail(CodeFourEyes, "the user who created, submitted or changed the purchase order cannot approve or confirm it (four-eyes policy)")
		}
	}
	return nil
}
