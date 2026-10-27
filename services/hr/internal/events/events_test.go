package events

import (
	"os"
	"regexp"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

// TestEveryTypeFollowsTheKernelNamingRule keeps All() and events/asyncapi.yaml
// in step: every published type follows the kernel rule and has a message
// named after it, and every documented message is published.
func TestEveryTypeFollowsTheKernelNamingRule(t *testing.T) {
	raw, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^      name: (dev\.passage\.\S+)$`).FindAllStringSubmatch(string(raw), -1) {
		want[m[1]] = true
	}
	if len(want) == 0 {
		t.Fatal("no message names found in asyncapi.yaml")
	}
	for _, typ := range All() {
		if !events.ValidType(typ) {
			t.Errorf("%s does not follow dev.passage.<ctx>.<entity>.<verb>.v<n>", typ)
		}
		if !want[typ] {
			t.Errorf("%s is not documented in events/asyncapi.yaml", typ)
		}
		delete(want, typ)
	}
	for typ := range want {
		t.Errorf("%s is documented but not published", typ)
	}
}
