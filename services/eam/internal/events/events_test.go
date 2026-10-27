package events

import (
	"os"
	"regexp"
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

// The AsyncAPI document names exactly the types this package publishes.
func TestAsyncAPIDocumentsEveryType(t *testing.T) {
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s+name: (dev\.passage\.\S+)$`).FindAllStringSubmatch(string(spec), -1) {
		documented[m[1]] = true
	}
	for _, typ := range All() {
		if !documented[typ] {
			t.Errorf("not in asyncapi.yaml: %s", typ)
		}
		delete(documented, typ)
	}
	for typ := range documented {
		t.Errorf("documented but never published: %s", typ)
	}
}
