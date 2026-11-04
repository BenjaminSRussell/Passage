// Package events holds the scheduling event types and payloads.
package events

import (
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
)

// Context is the bounded context name used in event types.
const Context = "scheduling"

var (
	RunCompleted      = events.Type(Context, "run", "completed", 1)
	JobScheduled      = events.Type(Context, "job", "scheduled", 1)
	OrderDatesChanged = events.Type(Context, "orderdates", "changed", 1)
	CampaignSequenced = events.Type(Context, "campaign", "sequenced", 1)
	CampaignStarted   = events.Type(Context, "campaign", "started", 1)
	CampaignCompleted = events.Type(Context, "campaign", "completed", 1)
	CampaignCancelled = events.Type(Context, "campaign", "cancelled", 1)

	CapacityOverrideCreated = events.Type(Context, "capacityoverride", "created", 1)
	CapacityOverrideDeleted = events.Type(Context, "capacityoverride", "deleted", 1)

	ResourceUpdated = events.Type(Context, "resource", "updated", 1)

	CapacityBlockUpdated = events.Type(Context, "capacityblock", "updated", 1)
	NetworkUpdated       = events.Type(Context, "network", "updated", 1)

	OrderImportRecorded = events.Type(Context, "orderimport", "recorded", 1)

	OperationUpdated = events.Type(Context, "operation", "updated", 1)
	OperationDeleted = events.Type(Context, "operation", "deleted", 1)
	OrderUnscheduled = events.Type(Context, "order", "unscheduled", 1)

	ParametersUpdated = events.Type(Context, "parameters", "updated", 1)
)

// All lists every event type this service publishes.
func All() []string {
	return []string{RunCompleted, JobScheduled, OrderDatesChanged, CampaignSequenced, CampaignStarted, CampaignCompleted, CampaignCancelled, CapacityOverrideCreated, CapacityOverrideDeleted, ResourceUpdated, CapacityBlockUpdated, NetworkUpdated, OrderImportRecorded, OperationUpdated, OperationDeleted, OrderUnscheduled, ParametersUpdated}
}

type RunCompletedData struct {
	RunID                string  `json:"runId"`
	Scope                string  `json:"scope"`
	Operations           int     `json:"operations"`
	MakespanMinutes      int     `json:"makespanMinutes"`
	TotalLatenessMinutes int     `json:"totalLatenessMinutes"`
	SolverStatus         *string `json:"solverStatus"`
}

// JobScheduledData is one placed operation. ChangeoverMinutes is the
// changeover at the start of the job (FR-23.15), inside [start, end).
// ChangeoverCost (an exact decimal string) and ChangeoverCurrency come from the
// setup matrix entry that priced it, with the cleaning standard operation set
// it requires; all three are omitted when no entry priced the job.
//
// OrderRef and OperationRef are the keys shop floor execution (wave 25) uses:
// the production order id and the operation number as text without leading
// zeros.
type JobScheduledData struct {
	JobID              string `json:"jobId"`
	OrderID            string `json:"orderId"`
	OperNumber         int    `json:"operNumber"`
	OrderRef           string `json:"orderRef"`
	OperationRef       string `json:"operationRef"`
	ResourceID         string `json:"resourceId"`
	Start              string `json:"start"`
	End                string `json:"end"`
	ChangeoverMinutes  int    `json:"changeoverMinutes"`
	ChangeoverCost     string `json:"changeoverCost,omitempty"`
	ChangeoverCurrency string `json:"changeoverCurrency,omitempty"`
	CleaningSetID      string `json:"cleaningSetId,omitempty"`
	// SecondaryResourceIDs are the resources booked with the job (BR-23.4).
	SecondaryResourceIDs []string `json:"secondaryResourceIds,omitempty"`
	RunID                string   `json:"runId"`
	Firmed               bool     `json:"firmed"`
}

type OrderDatesChangedData struct {
	OrderID     string `json:"orderId"`
	Start       string `json:"start"`
	End         string `json:"end"`
	LateMinutes int    `json:"lateMinutes"`
}

// CampaignOrderRef is one order of a sequenced campaign in run order.
type CampaignOrderRef struct {
	OrderID  string `json:"orderId"`
	Sequence int    `json:"sequence"`
}

// CampaignCleanoutRef is a cleanout the campaign plan inserts after an order.
type CampaignCleanoutRef struct {
	AfterOrderID string `json:"afterOrderId"`
	SetupGroup   string `json:"setupGroup"`
	Minutes      int    `json:"minutes"`
	Reason       string `json:"reason"`
}

// CampaignSequencedData tells production the order sequence of a campaign and
// the cleanout orders it needs (FR-23.14, FR-23.15).
type CampaignSequencedData struct {
	CampaignID        string                `json:"campaignId"`
	ResourceID        string                `json:"resourceId"`
	CampaignKey       string                `json:"campaignKey"`
	PlannedStart      string                `json:"plannedStart"`
	Orders            []CampaignOrderRef    `json:"orders"`
	ChangeoverMinutes int                   `json:"changeoverMinutes"`
	Cleanouts         []CampaignCleanoutRef `json:"cleanouts"`
	Version           int                   `json:"version"`
}

// CampaignStartedData marks a campaign frozen: later orders go to the next one.
type CampaignStartedData struct {
	CampaignID string `json:"campaignId"`
	ResourceID string `json:"resourceId"`
	StartedAt  string `json:"startedAt"`
	Version    int    `json:"version"`
}

// CampaignCompletedData marks a started campaign finished.
type CampaignCompletedData struct {
	CampaignID  string `json:"campaignId"`
	ResourceID  string `json:"resourceId"`
	CompletedAt string `json:"completedAt"`
	Version     int    `json:"version"`
}

// CampaignCancelledData marks a campaign cancelled. OrderIDs are the orders
// released from it; they can join another campaign. CounterRestored is true
// when a started campaign gave the resource's cleanout counter back.
type CampaignCancelledData struct {
	CampaignID      string   `json:"campaignId"`
	ResourceID      string   `json:"resourceId"`
	CancelledAt     string   `json:"cancelledAt"`
	Reason          string   `json:"reason"`
	OrderIDs        []string `json:"orderIds"`
	CounterRestored bool     `json:"counterRestored"`
	Version         int      `json:"version"`
}

// CapacityOverrideData announces a downtime or extra shift on a resource
// (FR-23.1). AffectedJobs are the booked jobs a downtime overlaps; production
// and the planner move them.
type CapacityOverrideData struct {
	OverrideID   string   `json:"overrideId"`
	ResourceID   string   `json:"resourceId"`
	Kind         string   `json:"kind"`
	Start        string   `json:"start"`
	End          string   `json:"end"`
	Reason       string   `json:"reason"`
	AffectedJobs []string `json:"affectedJobs"`
}

// ResourceUpdatedData announces a changed resource calendar or name (AC-23.7).
// AffectedJobs are the booked jobs the new calendar no longer covers.
type ResourceUpdatedData struct {
	ResourceID string         `json:"resourceId"`
	Code       string         `json:"code"`
	Name       string         `json:"name"`
	TimeZone   string         `json:"timeZone"`
	Shifts     []domain.Shift `json:"shifts"`
	// EfficiencyPct is the resource's efficiency in whole percent (BR-23.1).
	EfficiencyPct int      `json:"efficiencyPct"`
	Version       int      `json:"version"`
	AffectedJobs  []string `json:"affectedJobs"`
	// EngineeringResourceID is the engineering resource this one stands for,
	// null when unmapped.
	EngineeringResourceID *string `json:"engineeringResourceId"`
}

// CapacityBlockUpdatedData announces a changed capacity block (FR-23.17).
// Schedule runs and moves after the event read the new priority, condition
// and active flag.
type CapacityBlockUpdatedData struct {
	BlockID    string             `json:"blockId"`
	ResourceID string             `json:"resourceId"`
	Code       string             `json:"code"`
	Priority   int                `json:"priority"`
	Active     bool               `json:"active"`
	Condition  []domain.Predicate `json:"condition"`
	Version    int                `json:"version"`
	// AffectedJobs are the booked jobs the resource's active blocks now keep
	// out of the time they hold; they stay where they are until moved or
	// rescheduled.
	AffectedJobs []string `json:"affectedJobs"`
}

// NetworkUpdatedData announces a renamed or ended resource network (FR-23.13).
type NetworkUpdatedData struct {
	NetworkID  string  `json:"networkId"`
	Code       string  `json:"code"`
	Name       string  `json:"name"`
	ActiveFrom string  `json:"activeFrom"`
	ActiveTo   *string `json:"activeTo"`
	Version    int     `json:"version"`
}

// OrderImportOperation is one operation registered for a production order.
type OrderImportOperation struct {
	OperationID string   `json:"operationId"`
	OperNumber  int      `json:"operNumber"`
	Minutes     int      `json:"minutes"`
	ResourceIDs []string `json:"resourceIds"`
	// Change is created, updated or unchanged.
	Change string `json:"change"`
}

// OrderImportSkipped is an operation left out of the import.
type OrderImportSkipped struct {
	OperNumber int    `json:"operNumber"`
	Code       string `json:"code"`
	Detail     string `json:"detail"`
}

// OrderImportRecordedData is what scheduling did with an
// order.scheduled.v1 event of production (FR-23.10).
type OrderImportRecordedData struct {
	OrderID            string                 `json:"orderId"`
	OrderNumber        string                 `json:"orderNumber"`
	OrderVersion       int                    `json:"orderVersion"`
	Reason             string                 `json:"reason"`
	Status             string                 `json:"status"`
	Operations         []OrderImportOperation `json:"operations"`
	Skipped            []OrderImportSkipped   `json:"skipped"`
	RemovedOperNumbers []int                  `json:"removedOperNumbers"`
	KeptFirmed         []int                  `json:"keptFirmedOperNumbers"`
	ReplanRequired     bool                   `json:"replanRequired"`
	// WithdrawnTo is the status production moved the order to when status is
	// withdrawn (reason withdraw); empty otherwise.
	WithdrawnTo string `json:"withdrawnTo,omitempty"`
}

// OperationUpdatedData announces a changed operation. JobsRemoved is true when
// the change took the operation's non-firmed jobs off the schedule: the
// operation is open again and the next run places it (consumers holding its
// dates from job.scheduled drop them).
type OperationUpdatedData struct {
	OperationID string   `json:"operationId"`
	OrderID     string   `json:"orderId"`
	OperNumber  int      `json:"operNumber"`
	Minutes     int      `json:"minutes"`
	DueAt       *string  `json:"dueAt"`
	ReadyAt     *string  `json:"readyAt"`
	Priority    int      `json:"priority"`
	ResourceIDs []string `json:"resourceIds"`
	Version     int      `json:"version"`
	JobsRemoved bool     `json:"jobsRemoved"`
}

// OperationDeletedData announces a removed operation and whether its jobs left
// the schedule with it.
type OperationDeletedData struct {
	OperationID string `json:"operationId"`
	OrderID     string `json:"orderId"`
	OperNumber  int    `json:"operNumber"`
	JobsRemoved bool   `json:"jobsRemoved"`
}

// OrderUnscheduledData announces an order taken off the schedule: the listed
// operations lost their non-firmed jobs and are open again; KeptFirmed are the
// operation numbers a firmed job holds, which stay where they are.
type OrderUnscheduledData struct {
	OrderID    string `json:"orderId"`
	Operations []int  `json:"operations"`
	KeptFirmed []int  `json:"keptFirmed"`
}

// ParametersUpdatedData announces changed scheduling parameters: runs and
// moves after the event take the new defaults and propagation limits.
type ParametersUpdatedData struct {
	DefaultMode       string `json:"defaultMode"`
	DefaultDirection  string `json:"defaultDirection"`
	DefaultFinite     bool   `json:"defaultFinite"`
	DragSnapMinutes   int    `json:"dragSnapMinutes"`
	PropagationOrders int    `json:"propagationOrders"`
	PropagationLevels int    `json:"propagationLevels"`
	Version           int    `json:"version"`
}
