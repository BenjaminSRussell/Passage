// Package events names the CloudEvents the migration service publishes (dev.passage.migration.<entity>.<verb>.v1).
package events

import "github.com/BenjaminSRussell/passage/platform/events"

const (
	Context = "migration"
	Source  = "migration"
)

var (
	SyncRunCompleted = events.Type(Context, "syncrun", "completed", 1)
	SyncRunFailed    = events.Type(Context, "syncrun", "failed", 1)
)

// SyncRunData is the payload of both sync-run events. Status is succeeded or partial on completed, failed on failed.
type SyncRunData struct {
	BindingID           string  `json:"bindingId"`
	Entity              string  `json:"entity"`
	RunID               string  `json:"runId,omitempty"`
	Status              string  `json:"status"`
	Extracted           int     `json:"extracted"`
	Loaded              int     `json:"loaded"`
	Rejected            int     `json:"rejected"`
	Conflicted          int     `json:"conflicted"`
	Watermark           string  `json:"watermark"`
	ConsecutiveFailures int     `json:"consecutiveFailures"`
	NextDueAt           *string `json:"nextDueAt,omitempty"`
	Error               string  `json:"error,omitempty"`
}
