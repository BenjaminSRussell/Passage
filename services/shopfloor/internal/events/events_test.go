package events

import (
	"os"
	"regexp"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

// Every published type follows the naming rule and is documented in AsyncAPI,
// and the AsyncAPI file documents nothing the service does not publish.
func TestEveryTypeIsNamedAndDocumented(t *testing.T) {
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^      name: (dev\.passage\.[a-z0-9.]+)$`).FindAllStringSubmatch(string(spec), -1) {
		documented[m[1]] = true
	}
	for _, typ := range All() {
		if !events.ValidType(typ) {
			t.Fatalf("%s breaks dev.passage.<context>.<entity>.<verb>.v<n>", typ)
		}
		if !documented[typ] {
			t.Fatalf("%s is not documented in events/asyncapi.yaml", typ)
		}
		delete(documented, typ)
	}
	if len(documented) != 0 {
		t.Fatalf("documented types not published: %v", documented)
	}
}
