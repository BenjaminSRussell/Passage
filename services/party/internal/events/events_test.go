package events_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/party/internal/events"
)

func TestAllEmittedTypesAreInAsyncAPI(t *testing.T) {
	raw, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range events.All() {
		if !strings.Contains(string(raw), "x-passage-type: "+typ) {
			t.Errorf("%s is not listed in events/asyncapi.yaml", typ)
		}
	}
}

func TestPayloadsCarryNoPersonalData(t *testing.T) {
	for _, payload := range []any{events.PartyCreatedData{}, events.PartyUpdatedData{}} {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"dob", "gender", "tax", "registration"} {
			if strings.Contains(string(raw), banned) {
				t.Errorf("%T payload carries %q", payload, banned)
			}
		}
	}
}
