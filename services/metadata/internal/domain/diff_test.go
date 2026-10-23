package domain_test

import (
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/metadata/internal/domain"
	"github.com/BenjaminSRussell/passage/services/metadata/internal/gen"
)

const baseVendor = `name: Vendor
domain: vendor
table_group: main
fields:
  - {name: code, type: string, length: 20, required: true}
  - {name: status, type: enum, enum: Status}
  - {name: score, type: int32}
  - {name: amount, type: decimal, precision: 20, scale: 6}
enums:
  - {name: Status, values: [{name: active, code: 1}, {name: blocked, code: 2}]}
indexes:
  - {fields: [code]}
`

func vendor(t *testing.T) *domain.Entity {
	t.Helper()
	e, err := domain.Parse([]byte(baseVendor))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestClassify_BreakingAndAdditiveCases(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(e *domain.Entity)
		breaking bool
	}{
		{"remove first field", func(e *domain.Entity) { e.Fields = e.Fields[1:] }, true},
		{"remove last field", func(e *domain.Entity) { e.Fields = e.Fields[:3] }, true},
		{"narrow string length", func(e *domain.Entity) { e.Fields[0].Length = 10 }, true},
		{"string to int", func(e *domain.Entity) { e.Fields[0].Type = "int32" }, true},
		{"int32 to string", func(e *domain.Entity) { e.Fields[2].Type = "string" }, true},
		{"optional to required", func(e *domain.Entity) { e.Fields[2].Required = true }, true},
		{"add required without default", func(e *domain.Entity) {
			e.Fields = append(e.Fields, domain.Field{Name: "must", Type: "bool", Required: true})
		}, true},
		{"remove enum value", func(e *domain.Entity) { e.Enums[0].Values = e.Enums[0].Values[:1] }, true},
		{"rename field", func(e *domain.Entity) { e.Fields[0].Name = "code2" }, true},
		{"unique added to existing", func(e *domain.Entity) { e.Fields[2].Unique = true }, true},
		{"narrow decimal precision", func(e *domain.Entity) { e.Fields[3].Precision = 10 }, true},
		{"narrow decimal scale", func(e *domain.Entity) { e.Fields[3].Scale = 2 }, true},
		{"add nullable field", func(e *domain.Entity) { e.Fields = append(e.Fields, domain.Field{Name: "note", Type: "text"}) }, false},
		{"add nullable unique field", func(e *domain.Entity) {
			e.Fields = append(e.Fields, domain.Field{Name: "ref", Type: "uuid", Unique: true})
		}, false},
		{"add required with default", func(e *domain.Entity) {
			e.Fields = append(e.Fields, domain.Field{Name: "on", Type: "bool", Required: true, Default: "true"})
		}, false},
		{"add enum value", func(e *domain.Entity) {
			e.Enums[0].Values = append(e.Enums[0].Values, domain.EnumValue{Name: "preferred", Code: 3})
		}, false},
		{"widen string length", func(e *domain.Entity) { e.Fields[0].Length = 40 }, false},
		{"int32 to int64", func(e *domain.Entity) { e.Fields[2].Type = "int64" }, false},
		{"widen decimal precision", func(e *domain.Entity) { e.Fields[3].Precision = 30 }, false},
		{"add index", func(e *domain.Entity) { e.Indexes = append(e.Indexes, domain.Index{Fields: []string{"score"}}) }, false},
		{"remove index", func(e *domain.Entity) { e.Indexes = nil }, false},
		{"required to optional", func(e *domain.Entity) { e.Fields[0].Required = false }, false},
		{"change label", func(e *domain.Entity) { e.Fields[0].Label = "Vendor code" }, false},
		{"add hook", func(e *domain.Entity) { e.Hooks = append(e.Hooks, domain.Hook{Name: "validateWrite", Expr: "true"}) }, false},
	}
	for _, c := range cases {
		next := vendor(t)
		c.mutate(next)
		changes := domain.Diff(vendor(t), next)
		if got := len(domain.Breaking(changes)) > 0; got != c.breaking {
			t.Errorf("%s: breaking=%v, want %v (%v)", c.name, got, c.breaking, changes)
		}
		err := domain.CompareVersions(vendor(t), next)
		if c.breaking && (err == nil || !strings.Contains(err.Error(), "MET-0201")) {
			t.Errorf("%s: want MET-0201, got %v", c.name, err)
		}
		if !c.breaking && err != nil {
			t.Errorf("%s: additive change rejected: %v", c.name, err)
		}
	}
}

func TestDiff_IdenticalAndFirstPublish(t *testing.T) {
	if changes := domain.Diff(vendor(t), vendor(t)); len(changes) != 0 {
		t.Errorf("identical specs produced %v", changes)
	}
	first := domain.Diff(nil, vendor(t))
	if len(first) < 4 || len(domain.Breaking(first)) != 0 {
		t.Errorf("first publish must report fields as additive, got %v", first)
	}
}

func TestCompareVersions_BreakingFlagAndPath(t *testing.T) {
	next := vendor(t)
	next.Fields = next.Fields[1:]
	if err := domain.CompareVersions(vendor(t), next); err == nil || !strings.Contains(err.Error(), "fields.code") {
		t.Fatalf("removed field must fail naming fields.code, got %v", err)
	}
	next.Breaking = true
	if err := domain.CompareVersions(vendor(t), next); err != nil {
		t.Fatalf("declared breaking change must pass, got %v", err)
	}
}

func TestDiff_AdditiveV2ClientContract(t *testing.T) {
	v2 := vendor(t)
	v2.Fields = append(v2.Fields, domain.Field{Name: "note", Type: "text"})
	v2.Enums[0].Values = append(v2.Enums[0].Values, domain.EnumValue{Name: "preferred", Code: 3})
	out1, err1 := gen.Generate(vendor(t))
	out2, err2 := gen.Generate(v2)
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	for _, line := range strings.Split(strings.TrimSpace(out1.Client), "\n") {
		if strings.Contains(line, "status") {
			continue
		}
		if !strings.Contains(out2.Client, line+"\n") {
			t.Errorf("v1 client line missing from v2 client: %q", line)
		}
	}
	if !strings.Contains(out2.Client, `status?: "active" | "blocked" | "preferred"`) {
		t.Error("v2 client must widen the enum union")
	}
}
