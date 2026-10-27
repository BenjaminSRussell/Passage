package events

import "github.com/BenjaminSRussell/passage/platform/events"

const Context = "planning"

// PlanCompleted is published when a version becomes the current version of its plan.
var PlanCompleted = events.Type(Context, "plan", "completed", 1)

// PlannedOrderFirmed is published once per planned order when it is firmed
// (FR-22.10). The PRD names it planned-order.firmed.v1; the kernel's type rule
// admits only lowercase letters and digits in the entity segment.
var PlannedOrderFirmed = events.Type(Context, "plannedorder", "firmed", 1)

// PlanStarted is published when a run starts its version (FR-22.1). Runs are
// synchronous, so it is written in the run's transaction ahead of
// plan.completed.v1.
var PlanStarted = events.Type(Context, "plan", "started", 1)

// MessageRaised is published once per action or delay message a run creates
// (FR-22.7), in the run's transaction.
var MessageRaised = events.Type(Context, "message", "raised", 1)

// All lists the event types this service publishes.
func All() []string {
	return []string{PlanStarted, PlanCompleted, PlannedOrderFirmed, MessageRaised}
}

// PlanStartedData is the data member of plan.started.v1. Kind is static until
// the dynamic net-change plan exists; the scope is the whole snapshot of the
// site, so it is carried as site_id.
type PlanStartedData struct {
	PlanID        string `json:"plan_id"`
	PlanCode      string `json:"plan_code"`
	VersionID     string `json:"version_id"`
	VersionNumber int    `json:"version_number"`
	Kind          string `json:"kind"`
	SiteID        string `json:"site_id"`
	AsOf          string `json:"as_of"`
	ItemsPlanned  int    `json:"items_planned"`
}

// MessageRaisedData is the data member of message.raised.v1. SupplyRef is the
// planned order number or receipt ref the message is about (empty for a
// shortage), DemandRef the demand (sales line, forecast, bom line) it affects.
// Qty is a decimal string in the inventory unit.
type MessageRaisedData struct {
	MessageID     string `json:"message_id"`
	PlanID        string `json:"plan_id"`
	PlanCode      string `json:"plan_code"`
	VersionID     string `json:"version_id"`
	SiteID        string `json:"site_id"`
	Action        string `json:"action"`
	ItemID        string `json:"item_id"`
	SupplyRef     string `json:"supply_ref"`
	DemandRef     string `json:"demand_ref"`
	SuggestedDate string `json:"suggested_date"`
	DelayDays     int    `json:"delay_days"`
	Qty           string `json:"qty"`
}

// PlannedOrderFirmedData is the data member of plannedorder.firmed.v1. The
// owning context (purchasing for purchase, production for production and
// transfer orders) creates the target order with target_id; planned orders of
// one group share it and become the lines of one purchase order.
type PlannedOrderFirmedData struct {
	PlannedOrderID string  `json:"planned_order_id"`
	PlanVersionID  string  `json:"plan_version_id"`
	BatchID        string  `json:"batch_id"`
	OrderType      string  `json:"order_type"`
	TargetType     string  `json:"target_type"`
	TargetID       string  `json:"target_id"`
	ItemID         string  `json:"item_id"`
	SiteID         string  `json:"site_id"`
	VendorID       *string `json:"vendor_id"`
	Qty            string  `json:"qty"`
	OrderDate      string  `json:"order_date"`
	DeliveryDate   string  `json:"delivery_date"`
}

// PlanCompletedData is the data member of plan.completed.v1.
type PlanCompletedData struct {
	PlanID        string        `json:"plan_id"`
	PlanCode      string        `json:"plan_code"`
	VersionID     string        `json:"version_id"`
	VersionNumber int           `json:"version_number"`
	ItemsPlanned  int           `json:"items_planned"`
	PlannedOrders PlannedCounts `json:"planned_orders"`
	Messages      int           `json:"messages"`
	DurationMs    int64         `json:"duration_ms"`
	PlanHash      string        `json:"plan_hash"`
}

// PlannedCounts counts planned orders by order type.
type PlannedCounts struct {
	Purchase   int `json:"purchase"`
	Production int `json:"production"`
	Transfer   int `json:"transfer"`
}
