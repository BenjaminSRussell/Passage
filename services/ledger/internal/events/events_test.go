package events

import (
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

func TestEventTypes_FollowNamingRuleAndAreInAsyncAPI(t *testing.T) {
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{JournalPosted, JournalReversed, BalanceRebuilt} {
		if !events.ValidType(typ) {
			t.Errorf("%s does not follow dev.passage.<ctx>.<entity>.<verb>.v<n>", typ)
		}
		if !strings.Contains(string(spec), "name: "+typ) {
			t.Errorf("%s is not described in events/asyncapi.yaml", typ)
		}
	}
	if got := events.Topic(JournalPosted); !strings.Contains(string(spec), "address: "+got) {
		t.Errorf("topic %s for journal events is not an AsyncAPI channel address", got)
	}
}
