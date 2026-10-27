package events

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

// TestTypesFollowThePlatformRule guards against an event type the kernel's
// events.New would refuse at publish time (it panics on an invalid name).
func TestTypesFollowThePlatformRule(t *testing.T) {
	for _, typ := range []string{ProjectCreated, ProjectUpdated, WBSScheduled, ChangeOrderApplied} {
		if !events.ValidType(typ) {
			t.Errorf("%s is not a valid dev.passage.<context>.<entity>.<verb>.v<n> type", typ)
		}
	}
}
