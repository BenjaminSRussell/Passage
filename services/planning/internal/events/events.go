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

// ForecastPublished is published when an approver publishes a submitted
// forecast consensus (FR-22.9), in the publishing transaction.
var ForecastPublished = events.Type(Context, "forecast", "published", 1)

// ForecastGenerated is published when a statistical forecast generation wrote
// cells into a model (FR-22.9), in the writing transaction.
var ForecastGenerated = events.Type(Context, "forecast", "generated", 1)

// ScenarioSimulated is published when a simulation run finishes (FR-22.15,
// BR-22.14). It is a separate type from plan.completed.v1 so that consumers of
// the operational plan never see a simulation: no messages, firmed orders or
// plan.completed.v1 are published for a simulation version.
var ScenarioSimulated = events.Type(Context, "scenario", "simulated", 1)

// All lists the event types this service publishes.
func All() []string {
	return []string{PlanStarted, PlanCompleted, PlannedOrderFirmed, MessageRaised, ForecastPublished, ForecastGenerated, ScenarioSimulated}
}

// ScenarioSimulatedData is the data member of scenario.simulated.v1. The
// version is a simulation version of the plan; it is never current.
type ScenarioSimulatedData struct {
	PlanID        string        `json:"plan_id"`
	PlanCode      string        `json:"plan_code"`
	ScenarioID    string        `json:"scenario_id"`
	ScenarioCode  string        `json:"scenario_code"`
	VersionID     string        `json:"version_id"`
	VersionNumber int           `json:"version_number"`
	SiteID        string        `json:"site_id"`
	AsOf          string        `json:"as_of"`
	ItemsPlanned  int           `json:"items_planned"`
	PlannedOrders PlannedCounts `json:"planned_orders"`
	Messages      int           `json:"messages"`
	DurationMs    int64         `json:"duration_ms"`
	PlanHash      string        `json:"plan_hash"`
}

// ForecastPublishedData is the data member of forecast.published.v1. Version
// is the model's ETag after publishing; Items counts the item and site series
// and HorizonPeriods the distinct periods of the published cells, which run
// from PeriodFrom to PeriodTo.
type ForecastPublishedData struct {
	ModelID        string `json:"model_id"`
	ModelCode      string `json:"model_code"`
	Version        int    `json:"version"`
	Items          int    `json:"items"`
	HorizonPeriods int    `json:"horizon_periods"`
	PeriodFrom     string `json:"period_from"`
	PeriodTo       string `json:"period_to"`
	SubmittedBy    string `json:"submitted_by"`
	ApprovedBy     string `json:"approved_by"`
}

// ForecastGeneratedData is the data member of forecast.generated.v1. CellsKept
// counts the cells of the request that already held a manual or consensus value
// and were left as they were.
type ForecastGeneratedData struct {
	ModelID      string `json:"model_id"`
	ModelCode    string `json:"model_code"`
	Version      int    `json:"version"`
	Series       int    `json:"series"`
	CellsWritten int    `json:"cells_written"`
	CellsKept    int    `json:"cells_kept"`
	PeriodFrom   string `json:"period_from"`
	PeriodTo     string `json:"period_to"`
	GeneratedBy  string `json:"generated_by"`
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
	MessageID string `json:"message_id"`
	PlanID    string `json:"plan_id"`
	PlanCode  string `json:"plan_code"`
	VersionID string `json:"version_id"`
	SiteID    string `json:"site_id"`
	Action    string `json:"action"`
	ItemID    string `json:"item_id"`
	// WarehouseID is the warehouse planning scope the message is about (FR-22.18); null for the site scope.
	WarehouseID   *string `json:"warehouse_id"`
	SupplyRef     string  `json:"supply_ref"`
	DemandRef     string  `json:"demand_ref"`
	SuggestedDate string  `json:"suggested_date"`
	DelayDays     int     `json:"delay_days"`
	Qty           string  `json:"qty"`
}

// PlannedOrderFirmedData is the data member of plannedorder.firmed.v1. The
// owning context (purchasing for purchase, production for production and
// transfer orders) creates the target order with target_id; planned orders of
// one group share it and become the lines of one purchase order.
type PlannedOrderFirmedData struct {
	PlannedOrderID string `json:"planned_order_id"`
	PlanVersionID  string `json:"plan_version_id"`
	BatchID        string `json:"batch_id"`
	OrderType      string `json:"order_type"`
	TargetType     string `json:"target_type"`
	TargetID       string `json:"target_id"`
	ItemID         string `json:"item_id"`
	// WarehouseID is the warehouse of a warehouse planning scope (FR-22.18): the
	// to-warehouse of a planned transfer, whose from-warehouse is the site (BR-22.7);
	// null for an order of the site scope.
	WarehouseID *string `json:"warehouse_id"`
	SiteID      string  `json:"site_id"`
	// LegalEntityID is the plan version's legal entity (null for a plan without
	// one); consumers scope their inbox by it, and the CloudEvents legalentityid
	// extension is set from it. Added within v1.
	LegalEntityID *string `json:"legal_entity_id"`
	VendorID      *string `json:"vendor_id"`
	Qty           string  `json:"qty"`
	OrderDate     string  `json:"order_date"`
	DeliveryDate  string  `json:"delivery_date"`
	// QuotaArrangementID and QuotaSourceID are set when a quota arrangement
	// allocated the order (FR-22.17); the owning context stores them on the
	// target order so allocated quantity can be traced.
	QuotaArrangementID *string `json:"quota_arrangement_id"`
	QuotaSourceID      *string `json:"quota_source_id"`
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
