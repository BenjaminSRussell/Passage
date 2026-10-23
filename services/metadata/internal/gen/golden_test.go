package gen_test

import (
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
		for name, content := range out.Files(e.Name) {
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
