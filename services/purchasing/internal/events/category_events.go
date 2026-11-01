package events

// Category events (FR-17.20). Each is written to the outbox in the transaction that changes
// the assignment; the subject is the assignment id.
const (
	// CategoryVendorAssigned is written when a vendor is assigned to a procurement category.
	CategoryVendorAssigned = "dev.passage.procurement.categoryvendor.assigned.v1"
	// CategoryVendorChanged is written when an assignment's status or validity window changes.
	CategoryVendorChanged = "dev.passage.procurement.categoryvendor.changed.v1"
	// CategoryManagerAssigned is written when a worker becomes manager of a category.
	CategoryManagerAssigned = "dev.passage.procurement.categorymanager.assigned.v1"
	// CategoryManagerRemoved is written when a worker stops managing a category.
	CategoryManagerRemoved = "dev.passage.procurement.categorymanager.removed.v1"
)

// CategoryVendorData is the data member of categoryvendor.assigned.v1 and categoryvendor.changed.v1.
type CategoryVendorData struct {
	AssignmentID  string `json:"assignment_id"`
	LegalEntityID string `json:"legal_entity_id"`
	Category      string `json:"category"`
	VendorID      string `json:"vendor_id"`
	Status        string `json:"status"`
	ValidFrom     string `json:"valid_from"`
	ValidTo       string `json:"valid_to,omitempty"`
	Version       int    `json:"version"`
	ChangedBy     string `json:"changed_by,omitempty"`
}

// CategoryManagerData is the data member of categorymanager.assigned.v1 and categorymanager.removed.v1.
type CategoryManagerData struct {
	ManagerID     string `json:"manager_id"`
	LegalEntityID string `json:"legal_entity_id"`
	Category      string `json:"category"`
	WorkerID      string `json:"worker_id"`
	ChangedBy     string `json:"changed_by,omitempty"`
}
