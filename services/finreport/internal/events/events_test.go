package events

import (
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

// Every event type the service publishes is a valid CloudEvents type and is
// documented in events/asyncapi.yaml with the topic the outbox relay uses.
func TestAsyncAPIListsEveryEvent(t *testing.T) {
	raw, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, typ := range All() {
		if !events.ValidType(typ) {
			t.Fatalf("%s is not a valid event type", typ)
		}
		if !strings.Contains(doc, "name: "+typ+"\n") {
			t.Errorf("asyncapi.yaml has no message named %s", typ)
		}
		if !strings.Contains(doc, "address: "+events.Topic(typ)+"\n") {
			t.Errorf("asyncapi.yaml has no channel at %s for %s", events.Topic(typ), typ)
		}
	}
	if n := strings.Count(doc, "name: dev.passage."); n != len(All()) {
		t.Errorf("asyncapi.yaml documents %d events, the service publishes %d", n, len(All()))
	}
}
