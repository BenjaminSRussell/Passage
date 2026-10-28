package identity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func forwarded(ctx context.Context, p tenant.Principal) http.Header {
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://upstream/x", nil)
	Forward(req, p)
	return req.Header
}

func TestForwardSendsTheWholePrincipalWithSortedDuties(t *testing.T) {
	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, "req-1")
	h := forwarded(ctx, tenant.Principal{TenantID: "t-1", LegalEntityID: "le-1,le-2", UserID: "u-1", Duties: map[string]struct{}{"b": {}, "a": {}}})
	want := map[string]string{"X-Passage-Tenant": "t-1", "X-Passage-Legal-Entity": "le-1,le-2", "X-Passage-User": "u-1", "X-Passage-Duties": "a,b", "X-Request-Id": "req-1"}
	for k, v := range want {
		if h.Get(k) != v {
			t.Fatalf("%s = %q, want %q", k, h.Get(k), v)
		}
	}
}

func TestForwardOmitsWhatThePrincipalLacks(t *testing.T) {
	h := forwarded(context.Background(), tenant.Principal{TenantID: "t-1"})
	if h.Get("X-Passage-Tenant") != "t-1" {
		t.Fatalf("tenant %v", h)
	}
	for _, k := range []string{"X-Passage-Legal-Entity", "X-Passage-User", "X-Passage-Duties", "X-Request-Id"} {
		if _, present := h[k]; present {
			t.Fatalf("%s sent without a value: %v", k, h)
		}
	}
}

func TestForwardKeepsTheWildcardScopeVerbatim(t *testing.T) {
	if got := forwarded(context.Background(), tenant.Principal{TenantID: "t-1", LegalEntityID: "*"}).Get("X-Passage-Legal-Entity"); got != "*" {
		t.Fatalf("scope %q", got)
	}
}
