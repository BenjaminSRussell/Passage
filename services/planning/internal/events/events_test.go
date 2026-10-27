package events

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

func TestEventTypesFollowTheNamingRule(t *testing.T) {
	for _, typ := range All() {
		if !events.ValidType(typ) {
			t.Errorf("%s is not dev.passage.<context>.<entity>.<verb>.v<n>", typ)
		}
	}
	if PlanCompleted != "dev.passage.planning.plan.completed.v1" {
		t.Errorf("PlanCompleted = %s", PlanCompleted)
	}
}
