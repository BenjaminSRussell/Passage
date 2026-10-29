package events

import (
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

func TestTypesFollowNamingRule(t *testing.T) {
	want := map[string]bool{"dev.passage.sod.conflict.detected.v1": true, "dev.passage.sod.mitigation.approved.v1": true,
		"dev.passage.trade.screening.hit.v1": true}
	for _, typ := range All() {
		if !events.ValidType(typ) {
			t.Errorf("%s breaks dev.passage.<ctx>.<entity>.<verb>.v<n>", typ)
		}
		if !want[typ] {
			t.Errorf("unexpected event type %s", typ)
		}
	}
	if len(All()) != len(want) {
		t.Errorf("All() lists %d types, want %d", len(All()), len(want))
	}
}

// Every published type is documented in the AsyncAPI file, on its topic.
func TestAsyncAPIDocumentsEveryType(t *testing.T) {
	raw, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, typ := range All() {
		if !strings.Contains(doc, typ) {
			t.Errorf("asyncapi.yaml does not document %s", typ)
		}
		if topic := events.Topic(typ); !strings.Contains(doc, "address: "+topic) {
			t.Errorf("asyncapi.yaml has no channel on %s for %s", topic, typ)
		}
	}
}
