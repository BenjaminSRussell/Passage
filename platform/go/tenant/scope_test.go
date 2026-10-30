package tenant_test

import (
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func TestInLegalEntityScope(t *testing.T) {
	other := "0190a5f2-7b1c-7d3e-8a4f-1234567890ad"
	allScope := map[string]struct{}{tenant.DutyAllScope: {}}
	tests := []struct {
		name   string
		claim  string
		duties map[string]struct{}
		le     string
		want   bool
	}{
		{"exact match", validLE, nil, validLE, true},
		{"uppercase claim", strings.ToUpper(validLE), nil, validLE, true},
		{"uppercase entity", validLE, nil, strings.ToUpper(validLE), true},
		{"braces entity", validLE, nil, "{" + validLE + "}", true},
		{"urn entity", validLE, nil, "urn:uuid:" + validLE, true},
		{"braces claim", "{" + validLE + "}", nil, validLE, true},
		{"multi-entity first", validLE + "," + other, nil, validLE, true},
		{"multi-entity second with spaces", validLE + " , " + other, nil, other, true},
		{"outside multi-entity", validLE + "," + other, nil, "0190a5f2-7b1c-7d3e-8a4f-1234567890ae", false},
		{"different entity", validLE, nil, other, false},
		{"empty entity scoped", validLE, nil, "", false},
		{"nil entity scoped", validLE, nil, nilUUID, false},
		{"invalid entity", validLE, nil, "not-a-uuid", false},
		{"claim with no valid part", "garbage", nil, validLE, false},
		{"nil uuid claim", nilUUID, nil, nilUUID, false},
		{"star claim never grants", "*", allScope, validLE, false},
		{"empty scope no duty", "", nil, validLE, false},
		{"empty scope other duty", "", map[string]struct{}{"x": {}}, validLE, false},
		{"empty scope all-scope duty", "", allScope, validLE, true},
		{"all-scope duty empty entity", "", allScope, "", false},
		{"all-scope duty nil entity", "", allScope, nilUUID, false},
		{"scoped claim ignores all-scope duty", validLE, allScope, other, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := tenant.Principal{LegalEntityID: tc.claim, Duties: tc.duties}
			if got := tenant.InLegalEntityScope(p, tc.le); got != tc.want {
				t.Fatalf("InLegalEntityScope(%q, %q) = %v, want %v", tc.claim, tc.le, got, tc.want)
			}
		})
	}
}
