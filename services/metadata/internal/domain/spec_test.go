package domain_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/metadata/internal/domain"
)

func firstViolation(t *testing.T, data string) domain.Violation {
	t.Helper()
	e, err := domain.Parse([]byte(data))
	if err != nil {
		var v domain.Violation
		if !errors.As(err, &v) {
			t.Fatalf("Parse returned a non-violation error: %v", err)
		}
		return v
	}
	vs := domain.Validate(e)
	if len(vs) == 0 {
		t.Fatalf("expected a violation, definition was accepted:\n%s", data)
	}
	return vs[0]
}

func entityWith(fieldLine string) string {
	return "name: Foo\ndomain: test\ntable_group: main\nfields:\n  - " + fieldLine + "\n"
}

func TestParse_RejectsInvalidFixtures(t *testing.T) {
	cases := map[string][2]string{
		"unknown-key.yaml":          {"MET-0002", "bogus"},
		"bad-name.yaml":             {"MET-0003", "name"},
		"unknown-type.yaml":         {"MET-0004", "fields[0].type"},
		"enum-without-enums.yaml":   {"MET-0007", "fields[0].enum"},
		"title-not-string.yaml":     {"MET-0008", "title_fields[0]"},
		"injection-field-name.yaml": {"MET-0003", "fields[0].name"},
		"phi-not-encrypted.yaml":    {"MET-0010", "fields[0].encrypted"},
		"encrypted-on-int.yaml":     {"MET-0010", "cannot hold a sealed envelope"},
	}
	files, _ := filepath.Glob("../../testdata/invalid/*.yaml")
	if len(files) != len(cases) {
		t.Fatalf("expected %d invalid fixtures, found %d", len(cases), len(files))
	}
	for name, want := range cases {
		data, err := os.ReadFile(filepath.Join("../../testdata/invalid", name))
		if err != nil {
			t.Fatal(err)
		}
		v := firstViolation(t, string(data))
		if v.Code != want[0] || !strings.Contains(v.Path+v.Msg, want[1]) {
			t.Errorf("%s: got %s, want %s naming %q", name, v, want[0], want[1])
		}
	}
}

func TestValidate_InputDomain(t *testing.T) {
	cases := []struct{ name, data, code string }{
		{"empty document", "", "MET-0001"},
		{"fields absent", "name: Foo\ndomain: test\ntable_group: main\n", "MET-0004"},
		{"length as string", entityWith("{name: a, type: string, length: \"50\"}"), "MET-0001"},
		{"fields as object", "name: Foo\ndomain: test\ntable_group: main\nfields: {a: b}\n", "MET-0001"},
		{"required as string", entityWith("{name: a, type: bool, required: \"maybe\"}"), "MET-0001"},
		{"now on string", entityWith("{name: a, type: string, length: 5, default: now()}"), "MET-0005"},
		{"non numeric default", entityWith("{name: a, type: int32, default: \"1; drop\"}"), "MET-0005"},
		{"decimal scale 13", entityWith("{name: a, type: decimal, precision: 20, scale: 13}"), "MET-0006"},
		{"string without length", entityWith("{name: a, type: string}"), "MET-0006"},
		{"money currency column clash", entityWith("{name: credit_limit, type: money}\n  - {name: credit_limit_currency, type: string, length: 3}"), "MET-0009"},
		{"reserved field name", entityWith("{name: tenant_id, type: uuid}"), "MET-0009"},
		{"enum code in extension range", "name: Foo\ndomain: test\ntable_group: main\nenums:\n  - {name: E, values: [{name: a, code: 1000}]}\nfields:\n  - {name: a, type: enum, enum: E}\n", "MET-0007"},
		{"entity name too long", strings.Replace(entityWith("{name: a, type: bool}"), "Foo", "F"+strings.Repeat("a", 48), 1), "MET-0003"},
		{"set_null on required field", "name: Foo\ndomain: test\ntable_group: main\nfields:\n  - {name: a, type: uuid, required: true}\nrelations:\n  - {from_field: a, to_entity: Bar, to_field: id, on_delete: set_null}\n", "MET-0009"},
		{"phi without encrypted", entityWith("{name: a, type: text, phi: true}"), "MET-0010"},
		{"phi money cannot be sealed", entityWith("{name: a, type: money, phi: true, encrypted: true}"), "MET-0010"},
		{"encrypted json cannot be sealed", entityWith("{name: a, type: json, encrypted: true}"), "MET-0010"},
		{"encrypted unique", entityWith("{name: a, type: string, length: 9, encrypted: true, unique: true}"), "MET-0010"},
		{"encrypted default", entityWith("{name: a, type: string, length: 9, encrypted: true, default: x}"), "MET-0010"},
		{"phi as title", "name: Foo\ndomain: test\ntable_group: main\ntitle_fields: [a]\nfields:\n  - {name: a, type: string, length: 9, phi: true, encrypted: true}\n", "MET-0010"},
		{"phi in index", "name: Foo\ndomain: test\ntable_group: main\nfields:\n  - {name: a, type: string, length: 9, phi: true, encrypted: true}\nindexes:\n  - {fields: [a]}\n", "MET-0010"},
		{"phi not a bool", entityWith("{name: a, type: text, phi: \"maybe\"}"), "MET-0001"},
		{"where with injection", "name: Foo\ndomain: test\ntable_group: main\nfields:\n  - {name: a, type: bool}\nindexes:\n  - {fields: [a], where: \"a is null; drop table x\"}\n", "MET-0009"},
	}
	for _, c := range cases {
		if got := firstViolation(t, c.data).Code; got != c.code {
			t.Errorf("%s: got %s, want %s", c.name, got, c.code)
		}
	}
}

func TestParse_ReferenceEntitiesValid(t *testing.T) {
	files, _ := filepath.Glob("../../testdata/entities/_reference/*.yaml")
	if len(files) != 10 {
		t.Fatalf("expected 10 reference entities, found %d", len(files))
	}
	for _, f := range files {
		data, _ := os.ReadFile(f)
		e, err := domain.Parse(data)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if vs := domain.Validate(e); len(vs) > 0 {
			t.Errorf("%s: %v", f, vs)
		}
	}
}

func TestValidate_PHIEncryptedAccepted(t *testing.T) {
	for _, line := range []string{
		"{name: a, type: string, length: 9, phi: true, encrypted: true}",
		"{name: a, type: text, phi: true, encrypted: true}",
		"{name: a, type: bytes, phi: true, encrypted: true}",
		"{name: a, type: text, encrypted: true}",
		"{name: a, type: text}",
	} {
		e, err := domain.Parse([]byte(entityWith(line)))
		if err != nil {
			t.Fatal(err)
		}
		if vs := domain.Validate(e); len(vs) > 0 {
			t.Errorf("%s: %v", line, vs)
		}
	}
	e, _ := domain.Parse([]byte(entityWith("{name: a, type: text, phi: true, encrypted: true}")))
	if got := e.PHIFields(); len(got) != 1 || got[0] != "a" {
		t.Errorf("PHIFields = %v", got)
	}
}

func TestCompareVersions_EncryptionChangeIsBreaking(t *testing.T) {
	load := func(line string) *domain.Entity {
		e, err := domain.Parse([]byte(entityWith(line)))
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	err := domain.CompareVersions(load("{name: a, type: text}"), load("{name: a, type: text, encrypted: true}"))
	if err == nil || !strings.Contains(err.Error(), "MET-0201") {
		t.Fatalf("encrypting a published field must be breaking, got %v", err)
	}
}
