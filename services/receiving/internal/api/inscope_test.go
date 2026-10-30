package api

import (
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func TestInScope_FailsClosed(t *testing.T) {
	const a, b = "00000000-0000-7000-8000-0000000001e9", "00000000-0000-7000-8000-0000000001e8"
	tests := []struct {
		name   string
		claim  string
		entity string
		admit  bool
	}{
		{"same case", a, a, true},
		{"upper case claim", strings.ToUpper(a), a, true},
		{"upper case entity", a, strings.ToUpper(a), true},
		{"multi-entity", b + ", " + a, a, true},
		{"outside multi-entity", b + ",00000000-0000-7000-8000-0000000001e7", a, false},
		{"empty scope", "", a, false},
		{"empty entity", a, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := tenant.Principal{LegalEntityID: tc.claim}
			if err := inScope(p, tc.entity); (err == nil) != tc.admit {
				t.Fatalf("inScope(%q, %q) = %v, want admit=%v", tc.claim, tc.entity, err, tc.admit)
			}
		})
	}
}
