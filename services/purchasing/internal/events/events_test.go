package events_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	platformevents "github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

func TestEventTypesFollowTheNamingRule(t *testing.T) {
	for _, typ := range []string{events.RequisitionSubmitted, events.RequisitionApproved} {
		if !platformevents.ValidType(typ) {
			t.Errorf("%s does not match dev.passage.<context>.<entity>.<verb>.v<n>", typ)
		}
		if want := "passage." + events.Context + ".requisition"; platformevents.Topic(typ) != want {
			t.Errorf("%s routes to topic %s, want %s", typ, platformevents.Topic(typ), want)
		}
	}
}

// TestPayloadFieldsAreInTheAsyncAPI keeps internal/events and events/asyncapi.yaml
// in step: every JSON field the payload marshals is declared, and every event
// type constant appears as a const in the spec.
func TestPayloadFieldsAreInTheAsyncAPI(t *testing.T) {
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	data := events.RequisitionData{Lines: []events.RequisitionLine{{ProductID: "p", Category: "c"}}}
	raw, _ := json.Marshal(data)
	var top map[string]json.RawMessage
	_ = json.Unmarshal(raw, &top)
	var lines []map[string]any
	_ = json.Unmarshal(top["lines"], &lines)
	keys := []string{}
	for k := range top {
		keys = append(keys, k)
	}
	for k := range lines[0] {
		keys = append(keys, k)
	}
	for _, k := range keys {
		if !strings.Contains(string(spec), "\n        "+k+":") && !strings.Contains(string(spec), "\n              "+k+":") {
			t.Errorf("payload field %q is not declared in asyncapi.yaml", k)
		}
	}
	for _, typ := range []string{events.RequisitionSubmitted, events.RequisitionApproved} {
		if !strings.Contains(string(spec), "const: "+typ) {
			t.Errorf("event type %s is not in asyncapi.yaml", typ)
		}
	}
}
