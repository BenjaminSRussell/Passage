package gen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/metadata/internal/domain"
	"github.com/BenjaminSRussell/passage/services/metadata/internal/gen"
)

const (
	referenceDir = "../../testdata/entities/_reference"
	goldenDir    = "../../testdata/golden"
)

func TestGenerate_GoldenReferenceEntities(t *testing.T) {
	entities, err := gen.LoadDir(referenceDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) != 10 {
		t.Fatalf("expected 10 reference entities, got %d", len(entities))
	}
	for _, e := range entities {
		out, err := gen.Generate(e)
		if err != nil {
			t.Fatal(err)
		}
		goldenFiles := map[string]string{
			e.Name + ".sql":          out.Migration,
			e.Name + ".openapi.json": out.OpenAPI,
			e.Name + ".client.ts":    out.Client,
		}
		for name, content := range goldenFiles {
			path := filepath.Join(goldenDir, name)
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			golden, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if string(golden) != content {
				t.Errorf("%s drifted from golden; review the change and rerun with UPDATE_GOLDEN=1", name)
			}
		}
		for _, want := range []string{`"x-passage-duty"`, `"Idempotency-Key"`, `"If-Match"`, `"ETag"`, "conflict_version", "application/problem+json", "serviceCode", `"/v1/`} {
			if !strings.Contains(out.OpenAPI, want) {
				t.Errorf("%s OpenAPI lacks %s", e.Name, want)
			}
		}
	}
}

func TestGenerate_DeterministicThreeRuns(t *testing.T) {
	entities, err := gen.LoadDir(referenceDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entities {
		first, _ := gen.Generate(e)
		for range 2 {
			again, _ := gen.Generate(e)
			if again != first {
				t.Fatalf("%s: output differs between runs", e.Name)
			}
		}
	}
}

func TestGenerate_RLSAndTenantFirst(t *testing.T) {
	entities, err := gen.LoadDir(referenceDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entities {
		out, _ := gen.Generate(e)
		sql := out.Migration
		if !regexp.MustCompile(`create table "[a-z_]+" \(\n  "tenant_id" uuid not null,\n  "id" uuid not null,`).MatchString(sql) {
			t.Errorf("%s: tenant_id must be the first column", e.Name)
		}
		if !strings.Contains(strings.ToUpper(sql), "FORCE ROW LEVEL SECURITY") || !strings.Contains(sql, "_tenant_access") || strings.Contains(sql, "app.tenant_id") {
			t.Errorf("%s: tenant RLS missing", e.Name)
		}
		if e.PerLegalEntity != strings.Contains(sql, "_legal_entity_scope") {
			t.Errorf("%s: legal-entity scope policy must exist exactly when per_legal_entity", e.Name)
		}
	}
}

func TestGenerate_RejectsInjectedIdentifier(t *testing.T) {
	e := &domain.Entity{Name: "Foo", Domain: "test", TableGroup: "main", Fields: []domain.Field{{Name: `a"; drop table x;--`, Type: "bool"}}}
	if _, err := gen.Generate(e); err == nil || !strings.Contains(err.Error(), "MET-0003") {
		t.Fatalf("injected identifier must fail with MET-0003, got %v", err)
	}
}

const (
	phiDir       = "../../testdata/phi"
	phiGoldenDir = "../../testdata/golden-phi"
)

// TestGenerate_PHIGolden pins the artifacts of an entity with phi fields: the classification reaches the JSON schema,
// OpenAPI and the generated Go list, and a sealed field is stored as bytea.
func TestGenerate_PHIGolden(t *testing.T) {
	entities, err := gen.LoadDir(phiDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entities {
		out, err := gen.Generate(e)
		if err != nil {
			t.Fatal(err)
		}
		for name, content := range out.Files(e.Name) {
			path := filepath.Join(phiGoldenDir, name)
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			golden, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if string(golden) != content {
				t.Errorf("%s drifted from golden; review the change and rerun with UPDATE_GOLDEN=1", name)
			}
		}
		for file, want := range map[string][]string{
			out.JSONSchema: {`"x-passage-phi":true`, `"x-passage-encrypted":true`},
			out.OpenAPI:    {`"x-passage-phi":true`, `"x-passage-phi-fields":["coverageTier","medicalLeaveReason","accommodationNote"]`},
			out.Domain:     {`func BenefitElectionPHIFields() []string { return []string{"coverageTier", "medicalLeaveReason", "accommodationNote"} }`},
			out.Migration:  {`"coverage_tier" bytea`, `"accommodation_note" bytea`, `"payroll_memo" bytea`},
		} {
			for _, fragment := range want {
				if !strings.Contains(file, fragment) {
					t.Errorf("%s artifact lacks %s", e.Name, fragment)
				}
			}
		}
		var schema struct {
			Properties map[string]map[string]any `json:"properties"`
		}
		if err := json.Unmarshal([]byte(out.JSONSchema), &schema); err != nil {
			t.Fatal(err)
		}
		if _, tagged := schema.Properties["planCode"]["x-passage-phi"]; tagged {
			t.Errorf("a non-phi field must not carry the phi classification")
		}
		if schema.Properties["payrollMemo"]["x-passage-phi"] != nil || schema.Properties["payrollMemo"]["x-passage-encrypted"] != true {
			t.Errorf("an encrypted non-phi field is encrypted but not phi: %v", schema.Properties["payrollMemo"])
		}
	}
}

func TestGenerate_PHIFieldMustBeEncrypted(t *testing.T) {
	e := &domain.Entity{Name: "Foo", Domain: "test", TableGroup: "main", Fields: []domain.Field{{Name: "diagnosis", Type: "text", PHI: true}}}
	if _, err := gen.Generate(e); err == nil || !strings.Contains(err.Error(), "MET-0010") {
		t.Fatalf("phi without encrypted must fail with MET-0010, got %v", err)
	}
	e.Fields[0].Encrypted = true
	if _, err := gen.Generate(e); err != nil {
		t.Fatalf("phi with encrypted must generate: %v", err)
	}
}

func TestGenerate_NoPHIMeansNoPHIArtifacts(t *testing.T) {
	out, err := gen.Generate(&domain.Entity{Name: "Foo", Domain: "test", TableGroup: "main", Fields: []domain.Field{{Name: "a", Type: "bool"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{out.JSONSchema, out.OpenAPI, out.Domain} {
		if strings.Contains(s, "phi") || strings.Contains(s, "PHI") {
			t.Errorf("an entity without phi fields must not mention phi: %s", s)
		}
	}
}
