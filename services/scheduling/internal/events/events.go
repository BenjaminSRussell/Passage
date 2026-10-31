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

	CapacityOverrideCreated = events.Type(Context, "capacityoverride", "created", 1)
	CapacityOverrideDeleted = events.Type(Context, "capacityoverride", "deleted", 1)

	ResourceUpdated = events.Type(Context, "resource", "updated", 1)
)

// All lists every event type this service publishes.
func All() []string {
	return []string{RunCompleted, JobScheduled, OrderDatesChanged, CampaignSequenced, CampaignStarted, CapacityOverrideCreated, CapacityOverrideDeleted, ResourceUpdated}
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
type JobScheduledData struct {
	JobID              string `json:"jobId"`
	OrderID            string `json:"orderId"`
	OperNumber         int    `json:"operNumber"`
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
	ResourceID   string         `json:"resourceId"`
	Code         string         `json:"code"`
	Name         string         `json:"name"`
	TimeZone     string         `json:"timeZone"`
	Shifts       []domain.Shift `json:"shifts"`
	Version      int            `json:"version"`
	AffectedJobs []string       `json:"affectedJobs"`
}
