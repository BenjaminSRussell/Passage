package events

import (
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

// Every published type follows the kernel naming rule and is documented in
// events/asyncapi.yaml, and the AsyncAPI file documents no other fulfillment type.
func TestTypesAreValidAndDocumented(t *testing.T) {
	raw, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, typ := range All() {
		if !events.ValidType(typ) {
			t.Errorf("%s does not follow dev.passage.<context>.<entity>.<verb>.v<n>", typ)
		}
		if !strings.Contains(doc, "name: "+typ+"\n") {
			t.Errorf("%s is not documented in asyncapi.yaml", typ)
		}
	}
	if got, want := strings.Count(doc, "name: dev.passage.fulfillment."), len(All()); got != want {
		t.Errorf("asyncapi.yaml documents %d fulfillment messages, code publishes %d", got, want)
	}
}
