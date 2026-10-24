package gen

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/metadata/internal/domain"
)

func contractEntity() *domain.Entity {
	return &domain.Entity{
		Name:       "Vendor",
		Domain:     "vendor",
		TableGroup: "main",
		Fields: []domain.Field{
			{Name: "code", Type: "string", Length: 20, Required: true},
			{Name: "active", Type: "bool", Required: true, Default: "true"},
		},
	}
}

func TestGenerateEmitsRuntimeArtifacts(t *testing.T) {
	out, err := Generate(contractEntity())
	if err != nil {
		t.Fatal(err)
	}
	files := out.Files("Vendor")
	for name, fragment := range map[string]string{
		"Vendor.schema.json":   `"$schema":"https://json-schema.org/draft/2020-12/schema"`,
		"Vendor.domain.go":     "type Vendor struct",
		"Vendor.repository.go": "type VendorRepository interface",
		"Vendor.handlers.go":   "func RegisterVendorRoutes",
	} {
		if !strings.Contains(files[name], fragment) {
			t.Errorf("%s does not contain %q:\n%s", name, fragment, files[name])
		}
	}
}

func TestOpenAPIPatchIsPartialAndCreateHonorsDefaults(t *testing.T) {
	out, err := Generate(contractEntity())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out.OpenAPI), &doc); err != nil {
		t.Fatal(err)
	}
	components := doc["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	input := schemas["VendorInput"].(map[string]any)
	required := input["required"].([]any)
	if len(required) != 1 || required[0] != "code" {
		t.Fatalf("create required = %#v, want only code", required)
	}
	patch := schemas["VendorPatch"].(map[string]any)
	if required, ok := patch["required"]; ok {
		t.Fatalf("patch schema unexpectedly requires fields: %#v", required)
	}
	paths := doc["paths"].(map[string]any)
	item := paths["/v1/vendor/vendors/{id}"].(map[string]any)
	patchOp := item["patch"].(map[string]any)
	request := patchOp["requestBody"].(map[string]any)
	content := request["content"].(map[string]any)
	schema := content["application/json"].(map[string]any)["schema"].(map[string]any)
	if schema["$ref"] != "#/components/schemas/VendorPatch" {
		t.Fatalf("PATCH schema ref = %#v", schema["$ref"])
	}
}

func TestGenerateNeverPanicsForAcceptedIdentifiers(t *testing.T) {
	for _, name := range []string{"a_", "a__b"} {
		e := contractEntity()
		e.Fields = append(e.Fields, domain.Field{Name: name, Type: "bool"})
		if _, err := Generate(e); err == nil || !strings.Contains(err.Error(), "MET-0003") {
			t.Errorf("field %q error = %v, want MET-0003", name, err)
		}
	}
	e := contractEntity()
	e.Name = "Y"
	if _, err := Generate(e); err != nil {
		t.Fatalf("single-letter entity must generate safely: %v", err)
	}
}
