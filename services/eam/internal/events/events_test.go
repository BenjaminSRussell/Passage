package events

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

// TestEventTypes_AreValid checks every published type against the kernel naming rule.
func TestEventTypes_AreValid(t *testing.T) {
	for _, typ := range All() {
		if !events.ValidType(typ) {
			t.Errorf("%s is not a valid event type", typ)
		}
	}
}
