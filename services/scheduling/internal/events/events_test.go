package events

import (
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

func TestEveryTypeFollowsTheNamingRule(t *testing.T) {
	want := map[string]bool{
		"dev.passage.scheduling.run.completed.v1":            true,
		"dev.passage.scheduling.job.scheduled.v1":            true,
		"dev.passage.scheduling.orderdates.changed.v1":       true,
		"dev.passage.scheduling.campaign.sequenced.v1":       true,
		"dev.passage.scheduling.campaign.started.v1":         true,
		"dev.passage.scheduling.campaign.completed.v1":       true,
		"dev.passage.scheduling.campaign.cancelled.v1":       true,
		"dev.passage.scheduling.campaign.placed.v1":          true,
		"dev.passage.scheduling.campaign.unplaced.v1":        true,
		"dev.passage.scheduling.cleanoutcounter.reset.v1":    true,
		"dev.passage.scheduling.capacityoverride.created.v1": true,
		"dev.passage.scheduling.capacityoverride.deleted.v1": true,
		"dev.passage.scheduling.resource.updated.v1":         true,
		"dev.passage.scheduling.capacityblock.updated.v1":    true,
		"dev.passage.scheduling.network.updated.v1":          true,
		"dev.passage.scheduling.orderimport.recorded.v1":     true,
		"dev.passage.scheduling.operation.updated.v1":        true,
		"dev.passage.scheduling.operation.deleted.v1":        true,
		"dev.passage.scheduling.order.unscheduled.v1":        true,
		"dev.passage.scheduling.parameters.updated.v1":       true,
		"dev.passage.scheduling.bottleneck.detected.v1":      true,
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

// Every published type has a message in events/asyncapi.yaml.
func TestAsyncAPIDeclaresEveryType(t *testing.T) {
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range All() {
		if !strings.Contains(string(spec), "name: "+typ+"\n") {
			t.Errorf("events/asyncapi.yaml has no message named %s", typ)
		}
	}
}
