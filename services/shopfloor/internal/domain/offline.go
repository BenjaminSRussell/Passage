package domain

import (
	"fmt"
	"sort"
	"time"
)

// Offline replay rules (BR-25.7, FR-25.12). They run on the batch of queued kiosk
// actions before the store writes them, so the server applies one ordering.

// KindJobStart is the registration kind that opens a job (FR-25.3).
const KindJobStart = "job-start"

// ConflictJobFinished flags a job-start replayed for a job that was finished on
// the server while the kiosk was offline (BR-25.7, warning SF-0004). The action
// is kept, not lost.
const ConflictJobFinished = "job-finished-conflict"

// CodeQueueFull is SF-0010: the offline queue reached its action limit.
const CodeQueueFull = "SF-0010"

// MaxClockSkew is the device-to-server skew above which the offset is recorded
// and applied to the device's timestamps (BR-25.7).
const MaxClockSkew = 5 * time.Minute

// DefaultOfflineMaxActions is the FR-25.12 queue limit (SF_OFFLINE_MAX_ACTIONS).
const DefaultOfflineMaxActions = 5000

// ReplayAction is one queued kiosk action with the device's own timestamp.
type ReplayAction struct {
	ID       string
	ClientAt time.Time
	Kind     string
	JobID    string
}

// OrderReplay returns the actions in replay order: ascending client timestamp,
// ties broken by ID, so replay is deterministic. A repeated ID is a no-op
// (BR-25.7): only its first occurrence in the input is kept. The input is not
// modified.
func OrderReplay(actions []ReplayAction) []ReplayAction {
	seen := make(map[string]bool, len(actions))
	out := make([]ReplayAction, 0, len(actions))
	for _, a := range actions {
		if seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].ClientAt.Equal(out[j].ClientAt) {
			return out[i].ClientAt.Before(out[j].ClientAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// FinishedConflicts returns the action IDs that must carry ConflictJobFinished: job
// starts whose job the server has already finished. Other kinds never conflict.
func FinishedConflicts(actions []ReplayAction, finished map[string]bool) map[string]string {
	flags := map[string]string{}
	for _, a := range actions {
		if a.Kind == KindJobStart && a.JobID != "" && finished[a.JobID] {
			flags[a.ID] = ConflictJobFinished
		}
	}
	return flags
}

// ClockOffset is the server-minus-device offset measured at reconnect. It is
// stored with the batch, so the device time can be corrected later.
func ClockOffset(deviceAt, serverAt time.Time) time.Duration {
	return serverAt.Sub(deviceAt)
}

// SkewExceeded reports whether the offset is large enough to be recorded as a
// correction (BR-25.7: more than 5 minutes either way).
func SkewExceeded(offset time.Duration) bool {
	if offset < 0 {
		offset = -offset
	}
	return offset > MaxClockSkew
}

// CorrectedClientTime applies the recorded offset to a device timestamp. Offsets
// within the 5 minute tolerance are not applied, so small drift is kept as sent.
func CorrectedClientTime(deviceAt time.Time, offset time.Duration) time.Time {
	if !SkewExceeded(offset) {
		return deviceAt
	}
	return deviceAt.Add(offset)
}

// CheckQueueCapacity refuses one more queued action when the queue already holds
// max actions (FR-25.12, SF-0010). The error is a *Error with the catalog code.
func CheckQueueCapacity(queued, max int) error {
	if max <= 0 {
		max = DefaultOfflineMaxActions
	}
	if queued >= max {
		return &Error{Code: CodeQueueFull, Detail: fmt.Sprintf("offline queue is full (%d actions)", queued)}
	}
	return nil
}
