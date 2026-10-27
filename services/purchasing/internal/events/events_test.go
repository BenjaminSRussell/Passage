package events_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	platformevents "github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
)

var allTypes = map[string]string{
	events.RequisitionSubmitted: "requisition",
	events.RequisitionApproved:  "requisition",
	events.AgreementThreshold:   "agreement",
	events.POConfirmed:          "po",
	events.POVersionCreated:     "poversion",
	events.POCancelled:          "po",
	events.PORepriced:           "po",
	events.RFQSent:              "rfq",
	events.RFQAwarded:           "rfq",
}

func TestEventTypesFollowTheNamingRule(t *testing.T) {
	for typ, entity := range allTypes {
		if !platformevents.ValidType(typ) {
			t.Errorf("%s does not match dev.passage.<context>.<entity>.<verb>.v<n>", typ)
		}
		if want := "passage." + events.Context + "." + entity; platformevents.Topic(typ) != want {
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
	raw, _ = json.Marshal(events.AgreementThresholdData{Expiry: "2027-12-31"})
	var threshold map[string]any
	_ = json.Unmarshal(raw, &threshold)
	for k := range threshold {
		keys = append(keys, k)
	}
	// Purchase order payloads with every optional member set, so omitempty
	// fields are checked too.
	for _, v := range []any{
		events.POConfirmedData{SiteID: "s", WarehouseID: "w", Lines: []events.POLineData{{ProductID: "p", Description: "d"}}},
		events.POVersionCreatedData{Patch: []any{}},
		events.POCancelledData{Lines: []events.POCancelledLine{{ProductID: "p"}}},
		events.PORepricedData{Lines: []events.PORepricedLine{{}}, InvoicedLines: []events.PORepricedLine{{}}},
		events.RFQSentData{VendorIDs: []string{"v"}, Lines: []events.RFQLineData{{ProductID: "p", Description: "d", RequiredDate: "2026-11-01"}}},
		events.RFQAwardedData{Awards: []events.RFQAwardedLine{{OverrideReason: "only bid"}}},
	} {
		keys = append(keys, jsonKeys(t, v)...)
	}
	for _, k := range keys {
		if !strings.Contains(string(spec), "\n        "+k+":") && !strings.Contains(string(spec), "\n              "+k+":") {
			t.Errorf("payload field %q is not declared in asyncapi.yaml", k)
		}
	}
	for typ := range allTypes {
		if !strings.Contains(string(spec), "const: "+typ) {
			t.Errorf("event type %s is not in asyncapi.yaml", typ)
		}
	}
}

// jsonKeys returns the JSON member names of v and of the first element of any
// array member.
func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	var out []string
	for k, member := range top {
		out = append(out, k)
		var items []map[string]any
		if json.Unmarshal(member, &items) == nil && len(items) > 0 {
			for ik := range items[0] {
				out = append(out, ik)
			}
		}
	}
	return out
}
