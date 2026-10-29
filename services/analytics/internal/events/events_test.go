package events

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestAsyncAPI_MatchesPayloads keeps events/asyncapi.yaml in step with the
// payload structs: every published type is documented, and the data members
// listed under each message are exactly the struct's JSON fields.
func TestAsyncAPI_MatchesPayloads(t *testing.T) {
	raw, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	payloads := map[string]any{
		MetricReconciled:      MetricReconciledData{},
		DQScored:              DQScoredData{},
		ArchivePartitionMoved: ArchivePartitionMovedData{},
	}
	nameLine := regexp.MustCompile(`(?m)^      name: (\S+)$`)
	documented := map[string]bool{}
	for _, m := range nameLine.FindAllStringSubmatch(doc, -1) {
		documented[m[1]] = true
	}
	for typ, payload := range payloads {
		if !documented[typ] {
			t.Errorf("%s is published but not documented", typ)
			continue
		}
		// The message block runs from its name to the next message name.
		block := doc[strings.Index(doc, "name: "+typ):]
		if next := nameLine.FindStringIndex(block[1:]); next != nil {
			block = block[:next[0]+1]
		}
		dataAt := strings.Index(block, "          data:")
		if dataAt < 0 {
			t.Errorf("%s has no data schema", typ)
			continue
		}
		props := regexp.MustCompile(`(?m)^              ([a-z_]+):$`).FindAllStringSubmatch(block[dataAt:], -1)
		var gotDoc []string
		for _, p := range props {
			gotDoc = append(gotDoc, p[1])
		}
		var gotCode []string
		rt := reflect.TypeOf(payload)
		for i := range rt.NumField() {
			gotCode = append(gotCode, strings.Split(rt.Field(i).Tag.Get("json"), ",")[0])
		}
		sort.Strings(gotDoc)
		sort.Strings(gotCode)
		if !reflect.DeepEqual(gotDoc, gotCode) {
			t.Errorf("%s data fields: asyncapi %v, code %v", typ, gotDoc, gotCode)
		}
	}
	if len(documented) != len(payloads) {
		t.Errorf("asyncapi documents %d messages, code publishes %d", len(documented), len(payloads))
	}
}
