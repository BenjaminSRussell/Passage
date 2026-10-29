package store

import (
	"os"
	"strings"
	"testing"
)

func TestAsyncAPINamesEveryEventType(t *testing.T) {
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, eventType := range []string{OpenItemCreatedType, OpenItemSettledType} {
		known[eventType] = true
		if !strings.Contains(string(spec), "name: "+eventType+"\n") {
			t.Errorf("asyncapi.yaml does not name %s", eventType)
		}
	}
	for _, line := range strings.Split(string(spec), "\n") {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), "name: dev.passage."); ok && !known["dev.passage."+name] {
			t.Errorf("asyncapi.yaml names dev.passage.%s, which the service does not publish", name)
		}
	}
}
