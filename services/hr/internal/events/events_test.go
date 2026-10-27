package events

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

func TestEveryTypeFollowsTheKernelNamingRule(t *testing.T) {
	want := map[string]bool{
		"dev.passage.hr.worker.created.v1":             true,
		"dev.passage.hr.employment.changed.v1":         true,
		"dev.passage.hr.positionassignment.changed.v1": true,
		"dev.passage.hr.absence.approved.v1":           true,
	}
	for _, typ := range All() {
		if !events.ValidType(typ) {
			t.Errorf("%s does not follow dev.passage.<ctx>.<entity>.<verb>.v<n>", typ)
		}
		if !want[typ] {
			t.Errorf("%s is not documented in events/asyncapi.yaml", typ)
		}
		delete(want, typ)
	}
	for typ := range want {
		t.Errorf("%s is documented but not published", typ)
	}
}
