// Package events holds the scheduling event types and payloads.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

// Context is the bounded context name used in event types.
const Context = "scheduling"

var (
	RunCompleted      = events.Type(Context, "run", "completed", 1)
	JobScheduled      = events.Type(Context, "job", "scheduled", 1)
	OrderDatesChanged = events.Type(Context, "orderdates", "changed", 1)
)

// All lists every event type this service publishes.
func All() []string {
	return []string{RunCompleted, JobScheduled, OrderDatesChanged}
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
