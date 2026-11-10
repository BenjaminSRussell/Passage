package events

import (
	"os"
	"reflect"
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

// Every payload field the producers write is a property in events/asyncapi.yaml.
func TestPayloadFieldsAreDocumented(t *testing.T) {
	raw, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	for _, payload := range []any{PackReconciledData{}, PackLoadedData{}, ParallelRunDiffedData{}, CutoverStepCompletedData{}, CutoverPlanRolledBackData{}, CutoverDecisionRecordedData{}, ToleranceConfiguredData{}, CoexistenceChangedData{}} {
		typ := reflect.TypeOf(payload)
		for i := 0; i < typ.NumField(); i++ {
			name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if !strings.Contains(spec, "\n                "+name+":\n") {
				t.Errorf("%s.%s (%s) is not a payload property in events/asyncapi.yaml", typ.Name(), typ.Field(i).Name, name)
			}
		}
	}
}
