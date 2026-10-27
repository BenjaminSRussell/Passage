package events

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

func TestEveryTypeFollowsTheNamingRule(t *testing.T) {
	want := map[string]bool{
		"dev.passage.scheduling.run.completed.v1":      true,
		"dev.passage.scheduling.job.scheduled.v1":      true,
		"dev.passage.scheduling.orderdates.changed.v1": true,
	}
	for _, typ := range All() {
		if !events.ValidType(typ) {
			t.Fatalf("%s breaks dev.passage.<context>.<entity>.<verb>.v<n>", typ)
		}
		if !want[typ] {
			t.Fatalf("%s is not documented in events/asyncapi.yaml", typ)
		}
		delete(want, typ)
	}
	if len(want) != 0 {
		t.Fatalf("documented types not published: %v", want)
	}
}
