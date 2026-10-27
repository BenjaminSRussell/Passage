package events

import "github.com/BenjaminSRussell/passage/platform/events"

const Context = "planning"

// PlanCompleted is published when a version becomes the current version of its plan.
var PlanCompleted = events.Type(Context, "plan", "completed", 1)

// All lists the event types this service publishes.
func All() []string {
	return []string{PlanCompleted}
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
