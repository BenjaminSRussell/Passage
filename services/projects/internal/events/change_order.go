package events

// ChangeOrderApplied is published when an APPROVED change order is applied to a
// project (FR-41.18, BR-41.13). Budget control (wave 38) and analytics consume it.
// The PRD writes the entity as "change-order"; the platform type rule allows
// only [a-z0-9] per segment, so the entity is "changeorder" (as "plannedorder"
// in planning).
const ChangeOrderApplied = "dev.passage.projects.changeorder.applied.v1"

// ChangeOrderAppliedData is the data of ChangeOrderApplied (PRD §8.41.5).
// Amounts are decimal strings in Currency: the deltas are Σ revenue impact and
// Σ cost impact, ContractValue is the new total. NewBaselineVersionID is the
// BASELINE plan version the apply created.
type ChangeOrderAppliedData struct {
	ChangeOrderID        string `json:"changeOrderId"`
	ChangeOrderNumber    string `json:"changeOrderNumber"`
	ProjectID            string `json:"projectId"`
	Type                 string `json:"type"`
	Currency             string `json:"currency"`
	ContractValueDelta   string `json:"contractValueDelta"`
	BudgetDelta          string `json:"budgetDelta"`
	ContractValue        string `json:"contractValue"`
	NewBaselineVersionID string `json:"newBaselineVersionId"`
	VendorPartyID        string `json:"vendorPartyId,omitempty"`
}
