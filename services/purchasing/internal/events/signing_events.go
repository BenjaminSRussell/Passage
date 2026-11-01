package events

// SigningLimitChanged is written when a signing limit is set or changed by an approved
// request, or recertified (FR-17.17). The subject is the signing limit id.
const SigningLimitChanged = "dev.passage.procurement.signinglimit.changed.v1"

// SigningLimitData is the data member of signinglimit.changed.v1.
type SigningLimitData struct {
	LimitID       string `json:"limit_id"`
	LegalEntityID string `json:"legal_entity_id"`
	WorkerID      string `json:"worker_id"`
	DocumentType  string `json:"document_type"`
	Currency      string `json:"currency"`
	SpendingLimit string `json:"spending_limit"`
	ApprovalLimit string `json:"approval_limit"`
	RecertifyBy   string `json:"recertify_by"`
	// Reason is REQUEST_APPROVED or RECERTIFIED.
	Reason    string `json:"reason"`
	Version   int    `json:"version"`
	ChangedBy string `json:"changed_by,omitempty"`
}
