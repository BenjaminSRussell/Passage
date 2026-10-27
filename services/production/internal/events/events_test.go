package events

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

func TestEventTypesFollowTheKernelRule(t *testing.T) {
	seen := map[string]bool{}
	for _, typ := range All() {
		if !events.ValidType(typ) {
			t.Errorf("%s does not follow dev.passage.<context>.<entity>.<verb>.v<n>", typ)
		}
		if seen[typ] {
			t.Errorf("%s listed twice", typ)
		}
		seen[typ] = true
		want := "passage.production.order"
		switch typ {
		case JournalPosted:
			want = "passage.production.journal"
		case AsBuiltAssigned, AsBuiltDetached:
			want = "passage.production.asbuilt"
		case TriggerPointFired:
			want = "passage.production.triggerpoint"
		case SubcontractPORequested:
			want = "passage.production.subcontract"
		case OperationStatusChanged:
			want = "passage.production.operation"
		}
		if events.Topic(typ) != want {
			t.Errorf("%s routes to %s, want %s", typ, events.Topic(typ), want)
		}
	}
}

// Every published type has an AsyncAPI message whose payload lists every field the code sends.
func TestAsyncAPIMatchesThePayloads(t *testing.T) {
	raw, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	cases := []struct {
		message, typ string
		payload      any
	}{
		{"orderCreated", OrderCreated, OrderCreatedData{}},
		{"orderUpdated", OrderUpdated, OrderUpdatedData{}},
		{"orderStatusChanged", OrderStatusChanged, OrderStatusChangedData{}},
		{"orderSettled", OrderSettled, OrderSettledData{}},
		{"journalPosted", JournalPosted, JournalPostedData{}},
		{"asBuiltAssigned", AsBuiltAssigned, AsBuiltData{}},
		{"asBuiltDetached", AsBuiltDetached, AsBuiltData{}},
		{"orderBOMEdited", OrderBOMEdited, OrderBOMEditedData{}},
		{"triggerPointFired", TriggerPointFired, TriggerPointFiredData{}},
		{"subcontractPORequested", SubcontractPORequested, SubcontractPORequestedData{}},
		{"operationStatusChanged", OperationStatusChanged, OperationStatusChangedData{}},
	}
	if len(cases) != len(All()) {
		t.Fatalf("%d cases for %d event types", len(cases), len(All()))
	}
	for _, tc := range cases {
		start := strings.Index(spec, "    "+tc.message+":\n")
		if start < 0 {
			t.Fatalf("message %s missing", tc.message)
		}
		end := strings.Index(spec[start:], "x-passage-type: "+tc.typ)
		if end < 0 {
			t.Fatalf("message %s does not carry x-passage-type %s", tc.message, tc.typ)
		}
		section := spec[start : start+end]
		encoded, _ := json.Marshal(tc.payload)
		var fields map[string]any
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		for name := range fields {
			if !strings.Contains(section, "          "+name+": ") {
				t.Errorf("%s: property %s missing from the asyncapi payload", tc.message, name)
			}
		}
	}
}
