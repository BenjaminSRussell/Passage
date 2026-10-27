// Package events names the event types EAM publishes and their payloads.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

const Context = "eam"

// Event types. Where the PRD name has four segments after the namespace the
// kernel regex allows three, so the middle segments are joined (as org does).
var (
	RequestCreated       = events.Type(Context, "request", "created", 1)
	WorkOrderCreated     = events.Type(Context, "workorder", "created", 1)
	WorkOrderStatus      = events.Type(Context, "workorderstatus", "changed", 1)
	WorkOrderCompleted   = events.Type(Context, "workorder", "completed", 1)
	CounterReadingLogged = events.Type(Context, "counterreading", "registered", 1)
)

// All lists every event type this service publishes (checked by the registry test).
func All() []string {
	return []string{RequestCreated, WorkOrderCreated, WorkOrderStatus, WorkOrderCompleted, CounterReadingLogged}
}

type WorkOrderCompletedData struct {
	WorkOrderID  string `json:"workOrderId"`
	AssetID      string `json:"assetId"`
	Currency     string `json:"currency"`
	PartsCost    string `json:"partsCost"`
	LaborCost    string `json:"laborCost"`
	DowntimeMins int    `json:"downtimeMinutes"`
	CompletedAt  string `json:"completedAt"`
}

type WorkOrderStatusData struct {
	WorkOrderID string `json:"workOrderId"`
	From        string `json:"from"`
	To          string `json:"to"`
	At          string `json:"at"`
	Actor       string `json:"actor,omitempty"`
}

type RequestCreatedData struct {
	RequestID string `json:"requestId"`
	AssetID   string `json:"assetId"`
	Source    string `json:"source"`
	Severity  int    `json:"severity"`
	DedupeKey string `json:"dedupeKey,omitempty"`
}

type WorkOrderCreatedData struct {
	WorkOrderID string `json:"workOrderId"`
	AssetID     string `json:"assetId"`
	Type        string `json:"type"`
	PlanLineID  string `json:"planLineId,omitempty"`
}

type CounterReadingData struct {
	CounterID string  `json:"counterId"`
	AssetID   string  `json:"assetId"`
	Value     float64 `json:"value"`
	At        string  `json:"at"`
	Source    string  `json:"source"`
}
