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
	DowntimeRecorded     = events.Type(Context, "downtime", "recorded", 1)
	RequestTriaged       = events.Type(Context, "request", "triaged", 1)
	WarrantyClaimable    = events.Type(Context, "warranty", "claimable", 1)
	PermitStatus         = events.Type(Context, "permitstatus", "changed", 1)
)

// All lists every event type this service publishes (checked by the registry test).
func All() []string {
	return []string{RequestCreated, WorkOrderCreated, WorkOrderStatus, WorkOrderCompleted, CounterReadingLogged,
		DowntimeRecorded, RequestTriaged, WarrantyClaimable, PermitStatus}
}

// PermitStatusData is the payload of a permit transition (FR-44.19); the permit
// and its work order let mobile and safety notifications follow the permit.
type PermitStatusData struct {
	PermitID    string `json:"permitId"`
	WorkOrderID string `json:"workOrderId"`
	From        string `json:"from"`
	To          string `json:"to"`
	At          string `json:"at"`
	Actor       string `json:"actor"`
}

// DowntimeRecordedData is a stop of a production resource booked on a work
// order (BR-44.5); wave 23 turns it into a capacity exception for [from, to).
type DowntimeRecordedData struct {
	DowntimeID  string `json:"downtimeId"`
	ResourceID  string `json:"resourceId"`
	WorkOrderID string `json:"workOrderId"`
	AssetID     string `json:"assetId"`
	From        string `json:"from"`
	To          string `json:"to"`
	Minutes     int    `json:"minutes"`
}

// RequestTriagedData is a triage action on a maintenance request (FR-44.6).
type RequestTriagedData struct {
	RequestID string `json:"requestId"`
	AssetID   string `json:"assetId"`
	Action    string `json:"action"`
	From      string `json:"from"`
	To        string `json:"to"`
	At        string `json:"at"`
	Actor     string `json:"actor,omitempty"`
}

// WarrantyClaimableData is emitted when a warranty-covered work order completes
// (BR-44.14). The amounts are decimal strings of the covered parts and labor;
// the vendor claim itself is wave 45 (FR-45.15).
type WarrantyClaimableData struct {
	WorkOrderID   string `json:"workOrderId"`
	AssetID       string `json:"assetId"`
	WarrantyID    string `json:"warrantyId"`
	VendorPartyID string `json:"vendorPartyId"`
	Currency      string `json:"currency"`
	CoveredParts  string `json:"coveredParts"`
	CoveredLabor  string `json:"coveredLabor"`
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
