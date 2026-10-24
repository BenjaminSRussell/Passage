package domain_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/metadata/internal/domain"
)

func TestDiffCatchesPublishedContractChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.Entity)
	}{
		{"entity name", func(e *domain.Entity) { e.Name = "Supplier" }},
		{"domain path", func(e *domain.Entity) { e.Domain = "purchasing" }},
		{"legal entity scope", func(e *domain.Entity) { e.PerLegalEntity = true }},
		{"enum code", func(e *domain.Entity) { e.Enums[0].Values[0].Code = 7 }},
		{"unique index added", func(e *domain.Entity) { e.Indexes[0].Unique = true }},
		{"index replaced", func(e *domain.Entity) { e.Indexes[0].Fields = []string{"score"} }},
		{"relation target", func(e *domain.Entity) {
			e.Relations = []domain.Relation{{FromField: "owner_id", ToEntity: "User", ToField: "id", OnDelete: "restrict"}}
			e.Fields = append(e.Fields, domain.Field{Name: "owner_id", Type: "uuid"})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := vendor(t)
			next := vendor(t)
			if tt.name == "relation target" {
				old.Fields = append(old.Fields, domain.Field{Name: "owner_id", Type: "uuid"})
				old.Relations = []domain.Relation{{FromField: "owner_id", ToEntity: "Person", ToField: "id", OnDelete: "restrict"}}
				copyEntity := *old
				copyEntity.Relations = append([]domain.Relation(nil), old.Relations...)
				next = &copyEntity
			}
			tt.mutate(next)
			if err := domain.CompareVersions(old, next); err == nil {
				t.Fatal("breaking contract change passed compatibility gate")
			}
		})
	}
}
