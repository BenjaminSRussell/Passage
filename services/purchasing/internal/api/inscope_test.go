package api

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func TestInScope_ComparesCanonicalUUIDs(t *testing.T) {
	const entity = "00000000-0000-7000-8000-0000000001e9"
	p := tenant.Principal{TenantID: "00000000-0000-7000-8000-000000000001", LegalEntityID: entity}
	tests := []struct {
		name   string
		entity string
		admit  bool
	}{
		{"same case", entity, true},
		{"upper case", "00000000-0000-7000-8000-0000000001E9", true},
		{"other entity", "00000000-0000-7000-8000-0000000001e8", false},
		{"not a uuid", "nope", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := inScope(p, tc.entity); (err == nil) != tc.admit {
				t.Fatalf("inScope(%q) = %v, want admit=%v", tc.entity, err, tc.admit)
			}
		})
	}
}

func TestInScope_EmptyScopeAdmitsNothing(t *testing.T) {
	const entity = "00000000-0000-7000-8000-0000000001e9"
	p := tenant.Principal{TenantID: "00000000-0000-7000-8000-000000000001"}
	if err := inScope(p, entity); err == nil {
		t.Fatal("a caller with no legal-entity scope must be refused")
	}
}
