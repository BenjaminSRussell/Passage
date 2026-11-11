package features_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
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
		if _, err := os.Stat("../../../services/" + f.Module); err != nil {
			t.Errorf("feature %s names module %q, which is not a service: %v", f.Key, f.Module, err)
		}
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

func newSigner(t *testing.T) (*tenant.ServiceSigner, *tenant.ServiceVerifier) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("processmfg", priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer, tenant.NewServiceVerifierKids("identity", map[string]map[string]ed25519.PublicKey{"processmfg": {"": pub}})
}

func TestHTTPSourceReadsTheTenantsFeaturesWithASignedToken(t *testing.T) {
	signer, verifier := newSigner(t)
	var claims tenant.ServiceClaims
	var verr error
	var sawRawHeaders bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRawHeaders = r.Header.Get(tenant.HeaderTenant) != "" || r.Header.Get(tenant.HeaderDuties) != ""
		claims, verr = verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
		if r.URL.Path != "/v1/tenant/features" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"features":[{"key":"payroll","enabled":false},{"key":"sales","enabled":true}],"templates":[]}`))
	}))
	defer srv.Close()

	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantA, LegalEntityID: "", UserID: "u1"})
	state, err := features.NewHTTPSource(srv.URL+"/", nil, signer).Enabled(ctx, tenantA)
	if err != nil || state["payroll"] || !state["sales"] {
		t.Fatalf("state = %v err = %v", state, err)
	}
	if verr != nil || claims.Tid != tenantA || claims.Act != "u1" || claims.Le != "" || claims.Iss != "processmfg" || claims.Aud != "identity" ||
		len(claims.Duties) != 1 || claims.Duties[0] != features.DutyView {
		t.Fatalf("claims = %+v verify err = %v", claims, verr)
	}
	if sawRawHeaders {
		t.Fatal("the request must carry the service token, never raw gateway headers")
	}
}

func TestHTTPSourceRefusesToCallUnsigned(t *testing.T) {
	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called.Add(1) }))
	defer srv.Close()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantA, UserID: "u1"})
	if _, err := features.NewHTTPSource(srv.URL, nil, nil).Enabled(ctx, tenantA); !errors.Is(err, features.ErrUnsigned) {
		t.Fatalf("err = %v, want ErrUnsigned", err)
	}
	if called.Load() != 0 {
		t.Fatal("an unsigned source must not reach identity at all")
	}
}

func TestHTTPSourceReportsAFailedAnswer(t *testing.T) {
	signer, _ := newSigner(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer srv.Close()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantA, UserID: "u1"})
	if _, err := features.NewHTTPSource(srv.URL, nil, signer).Enabled(ctx, tenantA); err == nil {
		t.Fatal("a 403 must be an error so the middleware can fail open")
	}
}

type slowSource struct {
	calls   atomic.Int32
	release chan struct{}
	fail    atomic.Bool
}

func (s *slowSource) Enabled(context.Context, string) (map[string]bool, error) {
	s.calls.Add(1)
	<-s.release
	if s.fail.Load() {
		return nil, errors.New("identity unreachable")
	}
	return map[string]bool{"payroll": false}, nil
}

func TestCacheMakesOneLookupForConcurrentRequestsDuringAnOutage(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	src := &slowSource{release: make(chan struct{})}
	src.fail.Store(true)
	cache := features.NewCache(src, 30*time.Second).WithBackoff(5 * time.Second).WithClock(func() time.Time { return now })

	const n = 20
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = serve(cache, "payroll", "/v1/payroll/runs", true).Code
		}()
	}
	for src.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(src.release)
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusNoContent {
			t.Fatalf("request %d = %d, want it allowed through the outage", i, code)
		}
	}
	if src.calls.Load() != 1 {
		t.Fatalf("calls = %d, want one identity call for %d concurrent requests", src.calls.Load(), n)
	}

	serve(cache, "payroll", "/v1/payroll/runs", true)
	if src.calls.Load() != 1 {
		t.Fatalf("calls = %d, want the failure remembered during the backoff", src.calls.Load())
	}

	now = now.Add(6 * time.Second)
	src.fail.Store(false)
	if rec := serve(cache, "payroll", "/v1/payroll/runs", true); rec.Code != http.StatusForbidden || src.calls.Load() != 2 {
		t.Fatalf("after the backoff identity must be asked again and the real state apply: status %d calls %d", rec.Code, src.calls.Load())
	}
}

type panickingSource struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (s *panickingSource) Enabled(context.Context, string) (map[string]bool, error) {
	if s.calls.Add(1) == 1 {
		close(s.entered)
		<-s.release
		panic("identity client exploded")
	}
	return map[string]bool{"payroll": true}, nil
}

// A panic in the lookup leader must not strand the requests waiting on it, nor leave the lookup marked in flight.
func TestCacheLeaderPanicWakesWaitersAndDoesNotWedgeTheTenant(t *testing.T) {
	src := &panickingSource{entered: make(chan struct{}), release: make(chan struct{})}
	cache := features.NewCache(src, time.Minute).WithBackoff(0)

	leaderDone := make(chan any, 1)
	go func() {
		defer func() { leaderDone <- recover() }()
		_, _ = cache.Enabled(context.Background(), tenantA)
	}()
	<-src.entered
	waiter := make(chan error, 1)
	go func() {
		_, err := cache.Enabled(context.Background(), tenantA)
		waiter <- err
	}()
	time.Sleep(20 * time.Millisecond)
	close(src.release)

	if r := <-leaderDone; r == nil {
		t.Fatal("the panic must still propagate to the leader's caller")
	}
	select {
	case err := <-waiter:
		if err == nil {
			t.Fatal("a waiter of a panicked lookup should see a failed lookup, not a state")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter is stranded behind a lookup that panicked")
	}
	state, err := cache.Enabled(context.Background(), tenantA)
	if err != nil || !state["payroll"] {
		t.Fatalf("a later request must look again: %v %v", state, err)
	}
}

func TestCacheBacksOffAfterAFailedRefreshAndKeepsLastKnownState(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	src := &fakeSource{state: map[string]bool{"payroll": false}}
	cache := features.NewCache(src, 30*time.Second).WithBackoff(5 * time.Second).WithClock(func() time.Time { return now })
	serve(cache, "payroll", "/v1/payroll/runs", true)

	now = now.Add(31 * time.Second)
	src.state, src.err = nil, errors.New("identity unreachable")
	for range 4 {
		if rec := serve(cache, "payroll", "/v1/payroll/runs", true); rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want the last known state (off)", rec.Code)
		}
	}
	if src.calls.Load() != 2 {
		t.Fatalf("calls = %d, want the failed refresh not retried inside the backoff", src.calls.Load())
	}
}

func TestCacheEvictsTheOldestTenantNotEverything(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	src := &fakeSource{state: map[string]bool{"payroll": true}}
	cache := features.NewCache(src, time.Hour).WithClock(func() time.Time { now = now.Add(time.Millisecond); return now })
	for i := range features.MaxTenants + 1 {
		if _, err := cache.Enabled(context.Background(), fmt.Sprintf("tenant-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	before := src.calls.Load()
	if _, err := cache.Enabled(context.Background(), fmt.Sprintf("tenant-%d", features.MaxTenants)); err != nil || src.calls.Load() != before {
		t.Fatalf("a recent tenant must still be cached (calls %d -> %d)", before, src.calls.Load())
	}
	if _, err := cache.Enabled(context.Background(), "tenant-0"); err != nil || src.calls.Load() != before+1 {
		t.Fatalf("the oldest tenant must have been evicted (calls %d -> %d)", before, src.calls.Load())
	}
}
