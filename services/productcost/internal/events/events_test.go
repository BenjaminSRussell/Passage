package events

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

var published = []string{CostComponentCreated, CostGroupCreated, VersionCreated, VersionTransitioned, StandardCostActivated, PostingAccountsChanged}

func TestEventTypesFollowTheNamingRule(t *testing.T) {
	for _, typ := range published {
		if !events.ValidType(typ) || !strings.HasPrefix(typ, "dev.passage.costing.") {
			t.Errorf("event type %q breaks dev.passage.costing.<entity>.<verb>.v<n>", typ)
		}
	}
}

// TestAsyncAPI_ListsExactlyThePublishedEvents checks events/asyncapi.yaml names
// every event the service writes to the outbox, on its topic, and no other.
func TestAsyncAPI_ListsExactlyThePublishedEvents(t *testing.T) {
	raw, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	var names []string
	for _, m := range regexp.MustCompile(`(?m)^      name: (\S+)$`).FindAllStringSubmatch(spec, -1) {
		names = append(names, m[1])
	}
	want := append([]string(nil), published...)
	sort.Strings(names)
	sort.Strings(want)
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("asyncapi messages %v, published %v", names, want)
	}
	for _, typ := range published {
		if !strings.Contains(spec, "address: "+events.Topic(typ)+"\n") {
			t.Errorf("no channel on topic %s for %s", events.Topic(typ), typ)
		}
	}
}
