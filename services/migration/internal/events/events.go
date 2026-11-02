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
	// SyncAbsentDetected is published when a full read finds source keys the legacy system no longer has.
	SyncAbsentDetected = events.Type(Context, "syncabsent", "detected", 1)
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

// SyncAbsentData is the payload of SyncAbsentDetected: how many keys a full read of the binding found missing from
// the legacy source, how many of them are new since the last full read, and the first few of the new ones.
type SyncAbsentData struct {
	BindingID   string   `json:"bindingId"`
	Entity      string   `json:"entity"`
	RunID       string   `json:"runId,omitempty"`
	AbsentCount int      `json:"absentCount"`
	NewlyAbsent int      `json:"newlyAbsent"`
	SampleKeys  []string `json:"sampleKeys"`
}

// MaxSampleKeys bounds SyncAbsentData.SampleKeys so one event stays small.
const MaxSampleKeys = 20
