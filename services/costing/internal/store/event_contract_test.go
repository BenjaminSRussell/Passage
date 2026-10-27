package store

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

// nextMessage finds the heading of the next message in components.messages.
var nextMessage = regexp.MustCompile(`\n    [A-Z][A-Za-z]*:\n`)

// TestEvents_MatchAsyncAPI keeps events/asyncapi.yaml in step with the payloads
// the store writes: each type is a valid kernel type, it is catalogued as a
// message (not as a planned event), and every payload key (optional ones set,
// so omitempty keeps them) is a property of that message.
func TestEvents_MatchAsyncAPI(t *testing.T) {
	old := "1.00"
	payloads := []struct {
		typ     string
		payload any
	}{
		{AgingSnapshotEventType, agingSnapshotCreated{ValuationLedgerID: "set"}},
		{CostChangedEventType, costChanged{VersionID: "v", VersionCode: "c", AdjustmentID: "a", WarehouseID: "w", ValuationType: "t", OldCost: &old}},
		{ReconciliationDiffEventType, reconciliationDiff{ValuationLedgerID: "set"}},
	}
	raw, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	planned := spec[strings.Index(spec, "x-planned-events:"):]
	for _, tc := range payloads {
		t.Run(tc.typ, func(t *testing.T) {
			if !events.ValidType(tc.typ) {
				t.Fatalf("%s is not a valid event type", tc.typ)
			}
			start := strings.Index(spec, "name: "+tc.typ)
			if start < 0 {
				t.Fatalf("asyncapi.yaml has no message named %s", tc.typ)
			}
			if strings.Contains(planned, tc.typ) {
				t.Errorf("%s is emitted but still listed under x-planned-events", tc.typ)
			}
			message := spec[start:]
			if loc := nextMessage.FindStringIndex(message); loc != nil {
				message = message[:loc[0]]
			}
			if end := strings.Index(message, "\nx-planned-events:"); end > 0 {
				message = message[:end]
			}
			body, err := json.Marshal(tc.payload)
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
		})
	}
}
