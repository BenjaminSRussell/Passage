package httpx

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func TestOwnerLegalEntity(t *testing.T) {
	a, b := ids.New(), ids.New()
	ctxFor := func(scope string) context.Context {
		return tenant.With(context.Background(), tenant.Principal{TenantID: ids.New(), LegalEntityID: scope})
	}
	tests := []struct {
		name      string
		scope     string
		requested string
		want      string
		status    int
	}{
		{"single scope defaults to its entity", a, "", a, 0},
		{"all-entity caller must name the entity", "", "", "", http.StatusUnprocessableEntity},
		{"claim without a valid UUID fails closed", "le-test", "", "", http.StatusForbidden},
		{"invalid entries are dropped from the scope", "le-test," + a, "", a, 0},
		{"all-entity caller may name one", "", a, a, 0},
		{"several scopes require a choice", a + "," + b, "", "", http.StatusUnprocessableEntity},
		{"several scopes accept a member", a + "," + b, b, b, 0},
		{"entity outside scope is forbidden", a, b, "", http.StatusForbidden},
		{"malformed entity is rejected", "", "nope", "", http.StatusUnprocessableEntity},
		{"nil entity is rejected", "", ids.Nil, "", http.StatusUnprocessableEntity},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := OwnerLegalEntity(ctxFor(tc.scope), tc.requested)
			if tc.status == 0 {
				if err != nil || got != tc.want {
					t.Fatalf("got %q, %v; want %q", got, err, tc.want)
				}
				return
			}
			var p *Problem
			if !errors.As(err, &p) || p.Status != tc.status {
				t.Fatalf("err = %v, want status %d", err, tc.status)
			}
		})
	}
	if _, err := OwnerLegalEntity(context.Background(), ""); err == nil {
		t.Fatal("no principal must fail")
	}
}
