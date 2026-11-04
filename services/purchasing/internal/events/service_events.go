package events

// Service line events (FR-17.23). servicespec.changed.v1 has the PO id as its subject;
// modelservicespec.* the model's id.
const (
	// ServiceSpecChanged is written when the service specification of a PO line is saved or
	// removed. Wave 18 reads the limits of a service line from it.
	ServiceSpecChanged = "dev.passage.procurement.servicespec.changed.v1"
	// ModelServiceSpecCreated is written when a model service specification is created.
	ModelServiceSpecCreated = "dev.passage.procurement.modelservicespec.created.v1"
	// ModelServiceSpecChanged is written when a model service specification is changed or deactivated.
	ModelServiceSpecChanged = "dev.passage.procurement.modelservicespec.changed.v1"
)

// ServiceSpecData is the data member of servicespec.changed.v1. Removed is true when the line is
// a plain line again; the other fields then describe the specification that was removed.
type ServiceSpecData struct {
	POID                    string `json:"po_id"`
	Number                  string `json:"number"`
	LineNo                  int    `json:"line_no"`
	Currency                string `json:"currency"`
	ValueLimit              string `json:"value_limit"`
	ExpectedValue           string `json:"expected_value"`
	PlannedTotal            string `json:"planned_total"`
	OutlineItems            int    `json:"outline_items"`
	ContractAgreementLineID string `json:"contract_agreement_line_id,omitempty"`
	ModelID                 string `json:"model_id,omitempty"`
	Removed                 bool   `json:"removed"`
	ChangedBy               string `json:"changed_by,omitempty"`
}

// ModelServiceSpecData is the data member of modelservicespec.created.v1 and .changed.v1.
type ModelServiceSpecData struct {
	ModelID       string `json:"model_id"`
	LegalEntityID string `json:"legal_entity_id"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	Currency      string `json:"currency"`
	Status        string `json:"status"`
	PlannedTotal  string `json:"planned_total"`
	OutlineItems  int    `json:"outline_items"`
	Version       int    `json:"version"`
	ChangedBy     string `json:"changed_by,omitempty"`
}
