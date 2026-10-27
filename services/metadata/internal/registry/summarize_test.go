package registry

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/metadata/internal/domain"
)

func TestSummarizeCountsEveryChangeKind(t *testing.T) {
	cases := map[string]DiffSummary{
		"field.added":              {Added: 1},
		"enum.value_added":         {Added: 1},
		"index.added":              {Added: 1},
		"relation.added":           {Added: 1},
		"field.removed":            {Removed: 1},
		"enum.removed":             {Removed: 1},
		"enum.value_removed":       {Removed: 1},
		"index.removed":            {Removed: 1},
		"relation.removed":         {Removed: 1},
		"field.required_added":     {Changed: 1},
		"field.unique_added":       {Changed: 1},
		"index.unique_added":       {Changed: 1},
		"index.unique_removed":     {Changed: 1},
		"field.type_changed":       {Changed: 1},
		"field.relaxed":            {Changed: 1},
		"enum.code_changed":        {Changed: 1},
		"index.definition_changed": {Changed: 1},
		"relation.changed":         {Changed: 1},
		"entity.name_changed":      {Changed: 1},
		"entity.domain_changed":    {Changed: 1},
		"entity.scope_changed":     {Changed: 1},
	}
	for kind, want := range cases {
		if got := summarize([]domain.Change{{Kind: kind}}); got != want {
			t.Errorf("%s = %+v, want %+v", kind, got, want)
		}
	}
}

func TestSummarizeDiffOfRealEntities(t *testing.T) {
	old := &domain.Entity{
		Name: "Vendor", Domain: "vendor",
		Fields: []domain.Field{{Name: "code", Type: "string", Length: 20}, {Name: "legacy", Type: "string", Length: 20}},
		Enums:  []domain.Enum{{Name: "Status", Values: []domain.EnumValue{{Name: "active", Code: 1}}}},
	}
	next := &domain.Entity{
		Name: "Vendor", Domain: "vendor",
		Fields:  []domain.Field{{Name: "code", Type: "string", Length: 20}},
		Enums:   []domain.Enum{{Name: "Status", Values: []domain.EnumValue{{Name: "active", Code: 2}}}},
		Indexes: []domain.Index{{Fields: []string{"code"}}},
	}
	got := summarize(domain.Diff(old, next))
	if want := (DiffSummary{Added: 1, Changed: 1, Removed: 1}); got != want {
		t.Fatalf("summary = %+v, want %+v", got, want)
	}
}
