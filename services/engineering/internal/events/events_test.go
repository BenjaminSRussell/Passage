package events_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	platformevents "github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/events"
)

func TestEventTypes_Valid(t *testing.T) {
	for _, typ := range []string{events.ResourceChanged, events.CalendarGenerated, events.RouteVersionActivated, events.EcrSubmitted, events.EcrApproved, events.EcrRejected, events.EcrCancelled, events.ReleaseSessionCreated, events.ReleaseLineAccepted, events.ReleasedProductChanged, events.DocumentSigned, events.DocumentReleased, events.EbomImported} {
		if !platformevents.ValidType(typ) {
			t.Errorf("%s is not a valid event type", typ)
		}
	}
}

func TestPayloads_MatchAsyncAPI(t *testing.T) {
	raw, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	cases := []struct {
		message string
		typ     string
		payload any
	}{
		{"resourceChanged", events.ResourceChanged, events.ResourceChangedData{ChangedFields: []string{"x"}, SiteID: "s", CalendarID: "c"}},
		{"calendarGenerated", events.CalendarGenerated, events.CalendarGeneratedData{}},
		{"ecrSubmitted", events.EcrSubmitted, events.EcrData{DecidedBy: "d"}},
		{"ecoSubmitted", events.EcoSubmitted, events.EcoData{ApproverID: "a", EcrID: "e", ReleaseDate: "2026-01-01"}},
		{"releaseSessionCreated", events.ReleaseSessionCreated, events.ReleaseSessionCreatedData{Source: "eco"}},
		{"documentSigned", events.DocumentSigned, events.DocumentSignedData{SignedAt: "2026-07-01T12:00:00Z"}},
		{"ebomImported", events.EbomImported, events.EbomImportedData{Added: 1, Exceptions: 0}},
		{"releaseLineAccepted", events.ReleaseLineAccepted, events.ReleaseLineAcceptedData{}},
		{"releasedProductChanged", events.ReleasedProductChanged, events.ReleasedProductChangedData{}},
		{"documentReleased", events.DocumentReleased, events.DocumentReleasedData{}},
	}
	for _, tc := range cases {
		start := strings.Index(spec, "    "+tc.message+":\n")
		end := strings.Index(spec[start:], "x-passage-type: "+tc.typ)
		if start < 0 || end < 0 {
			t.Fatalf("%s with type %s missing from asyncapi.yaml", tc.message, tc.typ)
		}
		section := spec[start : start+end]
		encoded, err := json.Marshal(tc.payload)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		for name := range fields {
			if !strings.Contains(section, "          "+name+": ") {
				t.Errorf("%s: property %s missing from asyncapi payload", tc.message, name)
			}
		}
	}
}
