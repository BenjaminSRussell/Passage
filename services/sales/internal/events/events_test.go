package events

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

// TestAsyncAPIMatchesEventTypes checks that events/asyncapi.yaml documents
// exactly the event types the service publishes, and that each name follows
// dev.passage.<context>.<entity>.<verb>.v<n>.
func TestAsyncAPIMatchesEventTypes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "events", "asyncapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var documented []string
	for _, m := range regexp.MustCompile(`(?m)^\s+name: (dev\.passage\.\S+)$`).FindAllStringSubmatch(string(raw), -1) {
		documented = append(documented, m[1])
	}
	published := All()
	for _, typ := range published {
		if !events.ValidType(typ) {
			t.Errorf("invalid event type %s", typ)
		}
	}
	sort.Strings(documented)
	sort.Strings(published)
	if strings.Join(documented, ",") != strings.Join(published, ",") {
		t.Fatalf("AsyncAPI %v, code %v", documented, published)
	}
}
