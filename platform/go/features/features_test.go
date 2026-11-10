package features_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/features"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const tenantA = "11111111-1111-4111-8111-111111111111"

func TestCatalogIsConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range features.Catalog() {
		if seen[f.Key] || f.Name == "" || f.Description == "" || f.Module == "" {
			t.Errorf("feature %+v is duplicated or incomplete", f)
		}
		seen[f.Key] = true
	}
	for _, f := range features.Catalog() {
		for _, dep := range f.DependsOn {
			if !seen[dep] || dep == f.Key {
				t.Errorf("%s depends on %q, which is missing or itself", f.Key, dep)
			}
		}
	}
	all := features.Resolve(nil)
	for _, on := range all {
		if !on {
			t.Fatal("a tenant with nothing stored must have every feature on")
		}
	}
	if _, err := features.Apply(all, nil); err != nil {
		t.Fatalf("the all-on state must satisfy every dependency: %v", err)
	}
}

func TestTemplatesAreClosedUnderDependencies(t *testing.T) {
	for _, tpl := range features.Templates() {
		state, ok := features.TemplateState(tpl.Key)
		if !ok {
			t.Fatalf("template %s has no state", tpl.Key)
		}
		if _, err := features.Apply(nil, state); err != nil {
			t.Errorf("template %s is not self-contained: %v", tpl.Key, err)
		}
	}
	state, _ := features.TemplateState("distribution")
	for _, off := range []string{"manufacturing", "process-manufacturing", "payroll", "edi", "edifact", "projects"} {
		if state[off] {
			t.Errorf("distribution must leave %s off", off)
		}
	}
	if _, ok := features.TemplateState("nope"); ok {
		t.Error("an unknown template must not resolve")
	}
}

func TestApplyEnforcesDependencies(t *testing.T) {
	all := features.Resolve(nil)

	t.Run("turning off a feature others use is refused and names them", func(t *testing.T) {
		_, err := features.Apply(all, map[string]bool{"inventory": false})
		var fe *features.Error
		if !errors.As(err, &fe) {
			t.Fatalf("err = %v, want a feature error", err)
		}
		msg := fe.Fields["inventory"]
		for _, name := range []string{"Warehouse operations", "Catch weight", "Process manufacturing"} {
			if !strings.Contains(msg, name) {
				t.Errorf("message %q does not name %s", msg, name)
			}
		}
	})

	t.Run("turning off a feature together with its dependents is allowed", func(t *testing.T) {
		next, err := features.Apply(all, map[string]bool{"edi": false, "edifact": false})
		if err != nil || next["edi"] || next["edifact"] || !next["sales"] {
			t.Fatalf("next = %v err = %v", next, err)
		}
	})

	t.Run("turning on a feature without its dependency is refused", func(t *testing.T) {
		off := map[string]bool{"edi": false, "edifact": false}
		_, err := features.Apply(off, map[string]bool{"edifact": true})
		var fe *features.Error
		if !errors.As(err, &fe) || !strings.Contains(fe.Fields["edifact"], "Turn on Electronic data interchange first") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("an unknown key is reported against that key", func(t *testing.T) {
		_, err := features.Apply(all, map[string]bool{"time-travel": true})
		var fe *features.Error
		if !errors.As(err, &fe) || fe.Fields["time-travel"] == "" {
			t.Fatalf("err = %v", err)
		}
	})
}

type fakeSource struct {
	calls atomic.Int32
	state map[string]bool
	err   error
}

func (f *fakeSource) Enabled(context.Context, string) (map[string]bool, error) {
	f.calls.Add(1)
	return f.state, f.err
}

func serve(source features.Source, key, path string, withPrincipal bool) *httptest.ResponseRecorder {
	h := features.Require(source, key)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if withPrincipal {
		req = req.WithContext(tenant.With(req.Context(), tenant.Principal{TenantID: tenantA, UserID: "u1"}))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRequireAnswersADisabledFeatureWithAProblem(t *testing.T) {
	src := &fakeSource{state: map[string]bool{"payroll": false, "sales": true}}
	rec := serve(src, "payroll", "/v1/payroll/runs", true)
	if rec.Code != http.StatusForbidden || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("status = %d type = %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Code != features.CodeDisabled || p.Errors["feature"] != "payroll" || !strings.Contains(p.Detail, "Payroll is turned off") || !strings.Contains(p.Detail, "administrator") {
		t.Fatalf("problem = %+v", p)
	}
	if rec := serve(src, "sales", "/v1/sales/orders", true); rec.Code != http.StatusNoContent {
		t.Fatalf("an enabled feature must pass, got %d", rec.Code)
	}
}

func TestRequirePassesWhatItCannotJudge(t *testing.T) {
	src := &fakeSource{state: map[string]bool{"payroll": false}}
	for name, c := range map[string]struct {
		path      string
		principal bool
	}{
		"health":       {"/healthz", true},
		"readiness":    {"/readyz", true},
		"no principal": {"/v1/payroll/runs", false},
	} {
		if rec := serve(src, "payroll", c.path, c.principal); rec.Code != http.StatusNoContent {
			t.Errorf("%s: status = %d, want it passed to the next handler", name, rec.Code)
		}
	}
	if src.calls.Load() != 0 {
		t.Fatalf("source was asked %d times for requests it cannot judge", src.calls.Load())
	}
}

func TestRequireFailsOpenWhenTheLookupFails(t *testing.T) {
	src := &fakeSource{err: errors.New("identity unreachable")}
	if rec := serve(src, "payroll", "/v1/payroll/runs", true); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want the request allowed", rec.Code)
	}
}

func TestCacheAvoidsALookupPerRequestAndKeepsLastKnownState(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	src := &fakeSource{state: map[string]bool{"payroll": false}}
	cache := features.NewCache(src, 30*time.Second).WithClock(func() time.Time { return now })

	for range 5 {
		if rec := serve(cache, "payroll", "/v1/payroll/runs", true); rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	if src.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 inside the TTL", src.calls.Load())
	}

	now = now.Add(31 * time.Second)
	src.state = map[string]bool{"payroll": true}
	if rec := serve(cache, "payroll", "/v1/payroll/runs", true); rec.Code != http.StatusNoContent || src.calls.Load() != 2 {
		t.Fatalf("after the TTL the new state must apply: status = %d calls = %d", rec.Code, src.calls.Load())
	}

	now = now.Add(31 * time.Second)
	src.state, src.err = nil, errors.New("identity unreachable")
	if rec := serve(cache, "payroll", "/v1/payroll/runs", true); rec.Code != http.StatusNoContent {
		t.Fatalf("a failed refresh must keep the last known state, got %d", rec.Code)
	}
}

func TestHTTPSourceReadsTheTenantsFeatures(t *testing.T) {
	var gotTenant, gotUser, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant, gotUser, gotPath = r.Header.Get(tenant.HeaderTenant), r.Header.Get(tenant.HeaderUser), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"features":[{"key":"payroll","enabled":false},{"key":"sales","enabled":true}],"templates":[]}`))
	}))
	defer srv.Close()

	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantA, UserID: "u1"})
	state, err := features.NewHTTPSource(srv.URL+"/", nil).Enabled(ctx, tenantA)
	if err != nil || state["payroll"] || !state["sales"] {
		t.Fatalf("state = %v err = %v", state, err)
	}
	if gotTenant != tenantA || gotUser != "u1" || gotPath != "/v1/tenant/features" {
		t.Fatalf("request = tenant %q user %q path %q", gotTenant, gotUser, gotPath)
	}
}

func TestHTTPSourceReportsAFailedAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer srv.Close()
	if _, err := features.NewHTTPSource(srv.URL, nil).Enabled(context.Background(), tenantA); err == nil {
		t.Fatal("a 403 must be an error so the middleware can fail open")
	}
}
