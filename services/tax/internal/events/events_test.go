package events

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	kevents "github.com/BenjaminSRussell/passage/platform/events"
)

func TestEventsMatchAsyncAPI(t *testing.T) {
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(spec)
	if !kevents.ValidType(ProviderFallback) {
		t.Fatalf("%s breaks dev.passage.<ctx>.<entity>.<verb>.v<n>", ProviderFallback)
	}
	if !strings.Contains(doc, "name: "+ProviderFallback) {
		t.Errorf("asyncapi.yaml does not declare %s", ProviderFallback)
	}
	if topic := kevents.Topic(ProviderFallback); !strings.Contains(doc, "address: "+topic) {
		t.Errorf("asyncapi.yaml has no channel %s", topic)
	}
	raw, err := json.Marshal(ProviderFallbackData{})
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
