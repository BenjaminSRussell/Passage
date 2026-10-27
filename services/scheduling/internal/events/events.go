// Package events holds the scheduling event types and payloads.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

// Context is the bounded context name used in event types.
const Context = "scheduling"

var (
	RunCompleted      = events.Type(Context, "run", "completed", 1)
	JobScheduled      = events.Type(Context, "job", "scheduled", 1)
	OrderDatesChanged = events.Type(Context, "orderdates", "changed", 1)
	CampaignSequenced = events.Type(Context, "campaign", "sequenced", 1)
	CampaignStarted   = events.Type(Context, "campaign", "started", 1)
)

// All lists every event type this service publishes.
func All() []string {
	return []string{RunCompleted, JobScheduled, OrderDatesChanged, CampaignSequenced, CampaignStarted}
}

type RunCompletedData struct {
	RunID                string  `json:"runId"`
	Scope                string  `json:"scope"`
	Operations           int     `json:"operations"`
	MakespanMinutes      int     `json:"makespanMinutes"`
	TotalLatenessMinutes int     `json:"totalLatenessMinutes"`
	SolverStatus         *string `json:"solverStatus"`
}

type JobScheduledData struct {
	JobID      string `json:"jobId"`
	OrderID    string `json:"orderId"`
	OperNumber int    `json:"operNumber"`
	ResourceID string `json:"resourceId"`
	Start      string `json:"start"`
	End        string `json:"end"`
	RunID      string `json:"runId"`
	Firmed     bool   `json:"firmed"`
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
