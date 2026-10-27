package events

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The AsyncAPI document declares exactly the event types the service
// publishes, each with a CloudEvents type of the dev.passage.edi.*.v1 form.
func TestAsyncAPIDeclaresEveryPublishedType(t *testing.T) {
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var declared []string
	for _, m := range regexp.MustCompile(`(?m)^      name: (\S+)$`).FindAllStringSubmatch(string(spec), -1) {
		declared = append(declared, m[1])
	}
	published := All()
	sort.Strings(declared)
	sort.Strings(published)
	if strings.Join(declared, ",") != strings.Join(published, ",") {
		t.Fatalf("asyncapi declares %v, code publishes %v", declared, published)
	}
	typeRe := regexp.MustCompile(`^dev\.passage\.edi\.[a-z]+\.[a-z]+\.v1$`)
	for _, typ := range published {
		if !typeRe.MatchString(typ) {
			t.Errorf("event type %q does not follow dev.passage.<ctx>.<entity>.<verb>.v1", typ)
		}
	}
}
