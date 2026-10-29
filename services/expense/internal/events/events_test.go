package events

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	kevents "github.com/BenjaminSRussell/passage/platform/events"
)

// Each published type follows the naming rule, and events/asyncapi.yaml declares it on
// the channel the platform derives from the type, with the payload's field names.
func TestEventsMatchAsyncAPI(t *testing.T) {
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(spec)
	for typ, payload := range map[string]any{PolicyViolated: PolicyViolatedData{}, CashAdvancePaid: CashAdvancePaidData{}} {
		if !kevents.ValidType(typ) {
			t.Fatalf("%s breaks dev.passage.<ctx>.<entity>.<verb>.v<n>", typ)
		}
		if !strings.Contains(doc, "name: "+typ) {
			t.Errorf("asyncapi.yaml does not declare %s", typ)
		}
		if topic := kevents.Topic(typ); !strings.Contains(doc, "address: "+topic) {
			t.Errorf("asyncapi.yaml has no channel %s", topic)
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		for name := range fields {
			if !strings.Contains(doc, "          "+name+": ") {
				t.Errorf("%s payload field %s is not in asyncapi.yaml", typ, name)
			}
		}
	}
}
