// Package tenant carries the caller's tenant, legal entity, user and duties
// through context. Until wave 2 replaces it with JWT validation at the gateway,
// the values come from headers that are trusted only behind the gateway; the
// gateway must strip client-supplied X-Passage-* headers (issue #82, part b;
// see deploy/gateway/README.md). Middleware validates their shape but cannot
// authenticate their origin.
package tenant

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/go-chi/chi/v5/middleware"
)

const maxLegalEntities = 100

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
func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

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

// canonicalUUID returns the lowercase hyphenated form of s, or false when s is
// not a 36-character UUID.
func canonicalUUID(s string) (string, bool) {
	if len(s) != 36 {
		return "", false
	}
	return ids.Canonical(s)
}

func canonicalLegalEntities(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxLegalEntities {
		return "", false
	}
	seen := make(map[string]struct{}, len(parts))
	canonical := parts[:0]
	for _, part := range parts {
		c, ok := canonicalUUID(strings.TrimSpace(part))
		if !ok || c == ids.Nil {
			return "", false
		}
		if _, dup := seen[c]; !dup {
			seen[c] = struct{}{}
			canonical = append(canonical, c)
		}
	}
	return strings.Join(canonical, ","), true
}

// Middleware extracts the principal from headers. Requests without a tenant,
// with a malformed tenant, or with the nil system tenant are rejected with 401;
// a malformed legal entity scope is rejected with 400. Health endpoints pass
// through. The nil tenant is server-side only and is never accepted from headers.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		raw := r.Header.Get(HeaderTenant)
		if raw == "" {
			reject(w, r, http.StatusUnauthorized, "AUTH-0001", "Unauthorized", "missing tenant")
			return
		}
		t, ok := canonicalUUID(raw)
		if !ok || t == ids.Nil {
			reject(w, r, http.StatusUnauthorized, "AUTH-0001", "Unauthorized", "invalid tenant")
			return
		}
		le, ok := canonicalLegalEntities(r.Header.Get(HeaderLegalEntity))
		if !ok {
			reject(w, r, http.StatusBadRequest, "COM-0001", "Bad Request", "invalid legal entity")
			return
		}
		p := Principal{TenantID: t, LegalEntityID: le, UserID: r.Header.Get(HeaderUser), Duties: map[string]struct{}{}}
		for _, d := range strings.Split(r.Header.Get(HeaderDuties), ",") {
			if d = strings.TrimSpace(d); d != "" {
				p.Duties[d] = struct{}{}
			}
		}
		next.ServeHTTP(w, r.WithContext(With(r.Context(), p)))
	})
}

func reject(w http.ResponseWriter, r *http.Request, status int, code, title, detail string) {
	body := map[string]any{
		"type":   "https://passage.dev/problems/" + code,
		"title":  title,
		"status": status,
		"code":   code,
		"detail": detail,
	}
	if id := middleware.GetReqID(r.Context()); id != "" {
		body["traceId"] = id
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
