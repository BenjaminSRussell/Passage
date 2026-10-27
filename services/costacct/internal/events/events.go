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
	// HierarchyDistributed is published when a collecting node's cost is
	// distributed to its child nodes (FR-38.18).
	HierarchyDistributed = events.Type(Context, "hierarchy", "distributed", 1)
)

// All lists every event type this service publishes.
func All() []string { return []string{CostEntryCreated, AllocationCompleted, HierarchyDistributed} }

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
	TemplateID    string          `json:"template_id,omitempty"`
	Period        string          `json:"period"`
	Method        string          `json:"method"`
	Iterations    int             `json:"iterations"`
	Converged     bool            `json:"converged"`
	TotalIn       decimal.Decimal `json:"total_in"`
	TotalOut      decimal.Decimal `json:"total_out"`
	Residual      decimal.Decimal `json:"residual"`
	Swept         decimal.Decimal `json:"swept"`
	// ReversedRunID names the run a rerun reversed (BR-38.7).
	ReversedRunID string `json:"reversed_run_id,omitempty"`
}

// HierarchyDistributedData is the payload of hierarchy.distributed.v1. Each
// share names the receiving node and its cost object; wave 39 counts the
// distributed cost as actual input of category Distributed.
type HierarchyDistributedData struct {
	RunID         string                 `json:"run_id"`
	LegalEntityID string                 `json:"legal_entity_id"`
	HierarchyID   string                 `json:"hierarchy_id"`
	NodeID        string                 `json:"node_id"`
	CostElementID string                 `json:"cost_element_id"`
	Period        string                 `json:"period"`
	Currency      string                 `json:"currency"`
	Basis         string                 `json:"basis"`
	Amount        decimal.Decimal        `json:"amount"`
	Distributed   decimal.Decimal        `json:"distributed"`
	Residual      decimal.Decimal        `json:"residual"`
	Shares        []HierarchyShareRecord `json:"shares"`
}

// HierarchyShareRecord is one receiving node of a distribution.
type HierarchyShareRecord struct {
	NodeID       string          `json:"node_id"`
	CostObjectID string          `json:"cost_object_id"`
	Amount       decimal.Decimal `json:"amount"`
}
