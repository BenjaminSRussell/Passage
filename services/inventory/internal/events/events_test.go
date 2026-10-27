package events

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	pevents "github.com/BenjaminSRussell/passage/platform/events"
)

func readSpec(t *testing.T) string {
	t.Helper()
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return string(spec)
}

func TestAsyncAPIDeclaresEveryEvent(t *testing.T) {
	declared := map[string]bool{}
	for _, m := range regexp.MustCompile(`x-passage-type: (\S+)`).FindAllStringSubmatch(readSpec(t), -1) {
		declared[m[1]] = true
	}
	for _, typ := range All() {
		if !pevents.ValidType(typ) || !declared[typ] {
			t.Errorf("%s must be a valid type and declared in asyncapi.yaml", typ)
		}
		delete(declared, typ)
	}
	for typ := range declared {
		t.Errorf("asyncapi.yaml declares %s which All() does not list", typ)
	}
}

// requiredFields returns the required list of a top-level schema in the spec.
func requiredFields(t *testing.T, spec, schema string) []string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^    ` + schema + `:\n      type: object\n      required: \[([^\]]*)\]`).FindStringSubmatch(spec)
	if m == nil {
		t.Fatalf("schema %s has no required list", schema)
	}
	var out []string
	for _, f := range strings.Split(m[1], ",") {
		out = append(out, strings.TrimSpace(f))
	}
	slices.Sort(out)
	return out
}

func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Every payload field is documented as required, and nothing else is: the
// AsyncAPI schemas match the structs the outbox serializes.
func TestPayloadsMatchAsyncAPISchemas(t *testing.T) {
	spec := readSpec(t)
	payloads := map[string]any{
		"TransactionPostedData": TransactionPostedData{},
		"OnhandChangedData":     OnhandChangedData{},
		"JournalPostedData":     JournalPostedData{},
		"ReservationData":       ReservationData{},
		"BlockingData":          BlockingData{},
	}
	for schema, payload := range payloads {
		if got, want := jsonKeys(t, payload), requiredFields(t, spec, schema); !slices.Equal(got, want) {
			t.Errorf("%s: payload keys %v, asyncapi required %v", schema, got, want)
		}
	}
}
