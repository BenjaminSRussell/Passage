package events

// PolicyChanged is written when a legal entity's purchasing policy is saved
// (FR-17.13). Audit and workflow consumers read it; the subject is the legal
// entity id.
const PolicyChanged = "dev.passage.procurement.policy.changed.v1"

// PolicyChangedData is the data member of policy.changed.v1.
type PolicyChangedData struct {
	LegalEntityID             string `json:"legal_entity_id"`
	Version                   int    `json:"version"`
	ChangeManagementDefault   bool   `json:"change_management_default"`
	POFourEyes                bool   `json:"po_four_eyes"`
	AllowInvoiceBeforeReceipt bool   `json:"allow_invoice_before_receipt"`
	UpdatedBy                 string `json:"updated_by,omitempty"`
}
