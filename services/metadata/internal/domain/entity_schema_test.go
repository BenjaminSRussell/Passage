package domain_test

import (
	"encoding/json"
	"os"
	"testing"
)

func TestPublishedEntityLanguageSchemaIsJSONSchema202012(t *testing.T) {
	data, err := os.ReadFile("../../schema/entity.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("$schema = %#v", schema["$schema"])
	}
	required, ok := schema["required"].([]any)
	if !ok || len(required) != 4 {
		t.Fatalf("required = %#v", schema["required"])
	}
	properties := schema["properties"].(map[string]any)
	for _, name := range []string{"name", "domain", "table_group", "fields", "enums", "relations", "indexes", "hooks"} {
		if _, ok := properties[name]; !ok {
			t.Errorf("schema lacks %s", name)
		}
	}
}
