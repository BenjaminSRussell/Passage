package store

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

// TestAgingSnapshotEvent_MatchesAsyncAPI keeps events/asyncapi.yaml in step
// with the payload the store writes: the type is a valid kernel type, it is
// catalogued as a message (not as a planned event), and every payload key is a
// property of that message.
func TestAgingSnapshotEvent_MatchesAsyncAPI(t *testing.T) {
	if !events.ValidType(AgingSnapshotEventType) {
		t.Fatalf("%s is not a valid event type", AgingSnapshotEventType)
	}
	raw, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	start := strings.Index(spec, "name: "+AgingSnapshotEventType)
	if start < 0 {
		t.Fatalf("asyncapi.yaml has no message named %s", AgingSnapshotEventType)
	}
	if planned := spec[strings.Index(spec, "x-planned-events:"):]; strings.Contains(planned, AgingSnapshotEventType) {
		t.Errorf("%s is emitted but still listed under x-planned-events", AgingSnapshotEventType)
	}
	message := spec[start:]
	if end := strings.Index(message, "\n    CostChanged:"); end > 0 {
		message = message[:end]
	}
	body, err := json.Marshal(agingSnapshotCreated{ValuationLedgerID: "set so omitempty keeps it"})
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(body, &keys); err != nil {
		t.Fatal(err)
	}
	for k := range keys {
		if !strings.Contains(message, "\n          "+k+":") {
			t.Errorf("payload key %q is not a property of the AsyncAPI message", k)
		}
	}
}
