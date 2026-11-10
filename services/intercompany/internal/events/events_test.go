package events

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

// TestEventsMatchAsyncAPI keeps events/asyncapi.yaml in step with the code:
// every type follows the naming rule and is documented, and every payload
// field is listed in the spec.
func TestEventsMatchAsyncAPI(t *testing.T) {
	raw, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	payloads := map[string]any{
		PartnerCreated: PartnerCreatedData{}, PartnerUpdated: PartnerUpdatedData{},
		JournalPosted: JournalData{}, JournalCompensated: JournalData{}, ChargePosted: ChargePostedData{}, ReconciliationFlagged: ReconciliationFlaggedData{},
	}
	for typ, payload := range payloads {
		if !events.ValidType(typ) {
			t.Errorf("%s does not follow dev.passage.<ctx>.<entity>.<verb>.v<n>", typ)
		}
		if !strings.Contains(spec, "name: "+typ) {
			t.Errorf("%s is not in asyncapi.yaml", typ)
		}
		rt := reflect.TypeOf(payload)
		if rt == reflect.TypeOf(JournalData{}) {
			// Journal events share one payload schema; its entry fields are checked below.
			payloads2 := []any{JournalEntryData{}}
			for _, p2 := range payloads2 {
				rt2 := reflect.TypeOf(p2)
				for j := 0; j < rt2.NumField(); j++ {
					field := strings.Split(rt2.Field(j).Tag.Get("json"), ",")[0]
					if !strings.Contains(spec, " "+field+":") {
						t.Errorf("%s field %s is not in asyncapi.yaml", rt2.Name(), field)
					}
				}
			}
		}
		for i := 0; i < rt.NumField(); i++ {
			field := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
			if !strings.Contains(spec, " "+field+":") {
				t.Errorf("%s field %s is not in asyncapi.yaml", rt.Name(), field)
			}
		}
	}
}
