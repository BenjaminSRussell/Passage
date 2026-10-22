// Package tenant carries the caller's tenant, legal entity, user and duties
// through context. Until wave 2 replaces it with JWT validation at the gateway,
// the values come from trusted headers set by the gateway.
package tenant

import (
	"context"
	"net/http"
	"strings"
)

// Principal is who is calling and in which scope.
type Principal struct {
	TenantID      string
	LegalEntityID string
	UserID        string
	Duties        map[string]struct{}
}

// HasDuty reports whether the principal holds the named duty.
func (p Principal) HasDuty(duty string) bool { _, ok := p.Duties[duty]; return ok }

type ctxKey struct{}

// With returns a context carrying p.
func With(ctx context.Context, p Principal) context.Context { return context.WithValue(ctx, ctxKey{}, p) }

// FromContext returns the principal and whether one was set.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// Headers the gateway sets. Internal only; never trusted from the public edge.
const (
	HeaderTenant      = "X-Passage-Tenant"
	HeaderLegalEntity = "X-Passage-Legal-Entity"
	HeaderUser        = "X-Passage-User"
	HeaderDuties      = "X-Passage-Duties"
)

// Middleware extracts the principal from headers. Requests without a tenant
// are rejected with 401 except for health endpoints.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		t := r.Header.Get(HeaderTenant)
		if t == "" {
			http.Error(w, `{"type":"https://passage.dev/problems/AUTH-0001","title":"Unauthorized","status":401,"code":"AUTH-0001","detail":"missing tenant"}`, http.StatusUnauthorized)
			return
		}
		p := Principal{TenantID: t, LegalEntityID: r.Header.Get(HeaderLegalEntity), UserID: r.Header.Get(HeaderUser), Duties: map[string]struct{}{}}
		for _, d := range strings.Split(r.Header.Get(HeaderDuties), ",") {
			if d = strings.TrimSpace(d); d != "" {
				p.Duties[d] = struct{}{}
			}
		}
		next.ServeHTTP(w, r.WithContext(With(r.Context(), p)))
	})
}
