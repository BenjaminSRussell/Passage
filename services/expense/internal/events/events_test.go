package events

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	kevents "github.com/BenjaminSRussell/passage/platform/events"
)

// The published type follows the naming rule, and events/asyncapi.yaml declares it on
// the channel the platform derives from the type, with the payload's field names.
func TestPolicyViolatedMatchesAsyncAPI(t *testing.T) {
	if !kevents.ValidType(PolicyViolated) {
		t.Fatalf("%s breaks dev.passage.<ctx>.<entity>.<verb>.v<n>", PolicyViolated)
	}
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(spec)
	if !strings.Contains(doc, "name: "+PolicyViolated) {
		t.Errorf("asyncapi.yaml does not declare %s", PolicyViolated)
	}
	if topic := kevents.Topic(PolicyViolated); !strings.Contains(doc, "address: "+topic) {
		t.Errorf("asyncapi.yaml has no channel %s", topic)
	}
	raw, err := json.Marshal(PolicyViolatedData{})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for name := range fields {
		if !strings.Contains(doc, "          "+name+": ") {
			t.Errorf("payload field %s is not in asyncapi.yaml", name)
		}
	}
}
