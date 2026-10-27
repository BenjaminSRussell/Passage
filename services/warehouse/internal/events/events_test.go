package events

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
)

// Every published type follows the platform naming rule and is a message
// name in events/asyncapi.yaml, and every payload field is documented there.
func TestAsyncAPIDocumentsEveryEvent(t *testing.T) {
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(spec)
	for _, typ := range All() {
		if !events.ValidType(typ) {
			t.Errorf("%s breaks the event naming rule", typ)
		}
		if !strings.Contains(doc, "name: "+typ+"\n") {
			t.Errorf("asyncapi.yaml has no message named %s", typ)
		}
	}
	for _, payload := range []any{WorkCreatedData{}, WorkCompletedData{}, Line{}, ReceiptRegisteredData{}, ItemQty{}} {
		rt := reflect.TypeOf(payload)
		for i := 0; i < rt.NumField(); i++ {
			name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
			if !strings.Contains(doc, "        "+name+":") {
				t.Errorf("asyncapi.yaml does not document %s.%s", rt.Name(), name)
			}
		}
	}
}
