// Package events names the cost accounting events and their payloads.
package events

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
)

// Context is the event context of the controlling services.
const Context = "controlling"

// Event types published by costacct (PRD 7.38.5).
var (
	CostEntryCreated    = events.Type(Context, "costentry", "created", 1)
	AllocationCompleted = events.Type(Context, "allocation", "completed", 1)
)

// All lists every event type this service publishes.
func All() []string { return []string{CostEntryCreated, AllocationCompleted} }

// CostEntryCreatedData is the payload of costentry.created.v1.
type CostEntryCreatedData struct {
	EntryID      string          `json:"entry_id"`
	CostObjectID string          `json:"cost_object_id"`
	ElementID    string          `json:"element_id"`
	Period       string          `json:"period"`
	Amount       decimal.Decimal `json:"amount"`
	Origin       string          `json:"origin"`
}

// AllocationCompletedData is the payload of allocation.completed.v1.
type AllocationCompletedData struct {
	RunID         string          `json:"run_id"`
	LegalEntityID string          `json:"legal_entity_id"`
	Period        string          `json:"period"`
	Method        string          `json:"method"`
	Iterations    int             `json:"iterations"`
	Converged     bool            `json:"converged"`
	TotalIn       decimal.Decimal `json:"total_in"`
	TotalOut      decimal.Decimal `json:"total_out"`
	Residual      decimal.Decimal `json:"residual"`
	Swept         decimal.Decimal `json:"swept"`
}
