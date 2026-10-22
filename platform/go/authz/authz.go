// Package authz enforces duties on routes. Wave 2 swaps the local check for an
// OPA policy decision point behind the same Policy interface.
package authz

import (
	"context"
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Policy answers whether a principal may perform an action on a resource.
type Policy interface {
	Allow(ctx context.Context, p tenant.Principal, action, resource string) (bool, error)
}

// DutyPolicy allows an action when the principal holds the duty named by action.
type DutyPolicy struct{}

// Allow implements Policy.
func (DutyPolicy) Allow(_ context.Context, p tenant.Principal, action, _ string) (bool, error) {
	return p.HasDuty(action), nil
}

// RequireDuty is route middleware: 401 without a principal, 403 without the duty.
func RequireDuty(duty string) func(http.Handler) http.Handler {
	return Require(DutyPolicy{}, duty, "")
}

// Require is route middleware over an arbitrary Policy.
func Require(pol Policy, action, resource string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := tenant.FromContext(r.Context())
			if !ok {
				httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "no principal"))
				return
			}
			allowed, err := pol.Allow(r.Context(), p, action, resource)
			if err != nil {
				httpx.Fail(w, r, err)
				return
			}
			if !allowed {
				httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "missing duty "+action))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
