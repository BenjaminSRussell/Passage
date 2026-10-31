package authz_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func call(mw func(http.Handler) http.Handler, p *tenant.Principal) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	if p != nil {
		req = req.WithContext(tenant.With(req.Context(), *p))
	}
	rec := httptest.NewRecorder()
	mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(rec, req)
	return rec
}

func TestRequireAnyDuty(t *testing.T) {
	holds := func(d string) *tenant.Principal { return &tenant.Principal{Duties: map[string]struct{}{d: {}}} }
	mw := authz.RequireAnyDuty("a.scoped", "a.broad")
	if rec := call(mw, holds("a.broad")); rec.Code != http.StatusNoContent {
		t.Fatalf("broad duty = %d", rec.Code)
	}
	if rec := call(mw, holds("a.scoped")); rec.Code != http.StatusNoContent {
		t.Fatalf("scoped duty = %d", rec.Code)
	}
	rec := call(mw, holds("other"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "a.scoped, a.broad") {
		t.Fatalf("missing duty = %d %s; want 403 naming every accepted duty", rec.Code, rec.Body)
	}
	if rec := call(mw, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no principal = %d", rec.Code)
	}
	if rec := call(authz.RequireAnyDuty(), holds("a.broad")); rec.Code != http.StatusForbidden {
		t.Fatalf("no accepted duties must deny, got %d", rec.Code)
	}
}
