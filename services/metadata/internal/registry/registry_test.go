package registry_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/services/metadata/internal/domain"
	"github.com/BenjaminSRussell/passage/services/metadata/internal/registry"
)

func baseEntity() *domain.Entity {
	return &domain.Entity{
		Name:       "Vendor",
		Domain:     "vendor",
		TableGroup: "main",
		Fields:     []domain.Field{{Name: "code", Type: "string", Length: 20, Required: true}},
		Enums: []domain.Enum{{
			Name: "Status", Values: []domain.EnumValue{{Name: "active", Code: 1}},
		}},
	}
}

func TestMemoryRegistryVersionsAndDiffsDefinitions(t *testing.T) {
	ctx := context.Background()
	s := registry.NewMemory()
	v1, err := s.Publish(ctx, "tenant-a", baseEntity())
	if err != nil {
		t.Fatal(err)
	}
	next := baseEntity()
	next.Fields = append(next.Fields, domain.Field{Name: "note", Type: "text"})
	v2, err := s.Publish(ctx, "tenant-a", next)
	if err != nil {
		t.Fatal(err)
	}
	if v1.Number != 1 || v2.Number != 2 || len(v2.Changes) != 1 || v2.Changes[0].Kind != "field.added" {
		t.Fatalf("versions: v1=%#v v2=%#v", v1, v2)
	}
	current, err := s.Current(ctx, "tenant-a", "Vendor")
	if err != nil {
		t.Fatal(err)
	}
	current.Fields[0].Name = "mutated"
	again, _ := s.Current(ctx, "tenant-a", "Vendor")
	if again.Fields[0].Name != "code" {
		t.Fatal("registry returned mutable internal state")
	}
}

func TestMemoryRegistryTenantIsolation(t *testing.T) {
	ctx := context.Background()
	s := registry.NewMemory()
	if _, err := s.Publish(ctx, "tenant-a", baseEntity()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Current(ctx, "tenant-b", "Vendor"); err != registry.ErrNotFound {
		t.Fatalf("cross-tenant read error = %v", err)
	}
}
