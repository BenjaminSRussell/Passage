package events

import (
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

func TestEventTypesAreValidAndDocumented(t *testing.T) {
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range All() {
		if !events.ValidType(typ) {
			t.Errorf("%s does not follow dev.passage.<ctx>.<entity>.<verb>.v<n>", typ)
		}
		if !strings.Contains(string(spec), "name: "+typ) {
			t.Errorf("%s is not a message in events/asyncapi.yaml", typ)
		}
		if !strings.Contains(string(spec), "address: "+events.Topic(typ)) {
			t.Errorf("%s: channel %s missing from events/asyncapi.yaml", typ, events.Topic(typ))
		}
	}
}
