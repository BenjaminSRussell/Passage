package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fencedRouter(f *Fence) http.Handler {
	r := NewRouterWithoutTenant()
	r.Get("/v1/thing", func(w http.ResponseWriter, _ *http.Request) { JSON(w, 200, map[string]string{"ok": "1"}) })
	r.Get("/healthz/region", func(w http.ResponseWriter, _ *http.Request) { JSON(w, 200, map[string]string{"role": "FENCED"}) })
	r.Get("/metrics", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	return f.Middleware(r)
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestNilFencePassesEverything(t *testing.T) {
	var f *Fence
	if rec := get(fencedRouter(f), "/v1/thing"); rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if FenceFromEnv(context.Background(), func(string) string { return "" }) != nil {
		t.Fatal("no source configured must give no fence")
	}
}

func TestFencedRegionRefusesWith503AndRetryAfter(t *testing.T) {
	var fenced atomic.Bool
	fenced.Store(true)
	f := NewFence(t.Context(), func(context.Context) (bool, error) { return fenced.Load(), nil }, time.Second)
	h := fencedRouter(f)
	rec := get(h, "/v1/thing")
	if rec.Code != 503 || rec.Header().Get("Retry-After") != "30" {
		t.Fatalf("status %d retry-after %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type %q", ct)
	}
	for _, p := range []string{"/healthz", "/readyz", "/healthz/region", "/metrics"} {
		if rec := get(h, p); rec.Code != 200 {
			t.Fatalf("%s must stay reachable while fenced, got %d", p, rec.Code)
		}
	}
}

func TestFenceFollowsTheSourceAndKeepsStateOnError(t *testing.T) {
	var fenced atomic.Bool
	var fail atomic.Bool
	src := func(context.Context) (bool, error) {
		if fail.Load() {
			return false, errors.New("down")
		}
		return fenced.Load(), nil
	}
	f := &Fence{}
	f.refresh(t.Context(), src)
	if f.Fenced() {
		t.Fatal("starts not fenced")
	}
	fenced.Store(true)
	f.refresh(t.Context(), src)
	if !f.Fenced() {
		t.Fatal("must fence")
	}
	fail.Store(true)
	f.refresh(t.Context(), src)
	if !f.Fenced() {
		t.Fatal("an unreadable source keeps the last state")
	}
	fail.Store(false)
	fenced.Store(false)
	f.refresh(t.Context(), src)
	if f.Fenced() {
		t.Fatal("must unfence")
	}
}

func TestUnreadableSourceAtStartIsNotFenced(t *testing.T) {
	f := NewFence(t.Context(), func(context.Context) (bool, error) { return false, errors.New("down") }, time.Second)
	if get(fencedRouter(f), "/v1/thing").Code != 200 {
		t.Fatal("an unreadable source at start must not take the region down")
	}
}

func TestFileSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "REGION_ROLE")
	src := FileFenceSource(path)
	if fenced, err := src(t.Context()); err != nil || fenced {
		t.Fatalf("missing file: fenced=%v err=%v", fenced, err)
	}
	for role, want := range map[string]bool{"FENCED\n": true, "fenced": true, "PRIMARY\n": false, "PASSIVE": false} {
		if err := os.WriteFile(path, []byte(role), 0o600); err != nil {
			t.Fatal(err)
		}
		if fenced, err := src(t.Context()); err != nil || fenced != want {
			t.Fatalf("%q: fenced=%v err=%v", role, fenced, err)
		}
	}
}

func TestURLSourceReadsBodyOnA503(t *testing.T) {
	role := "FENCED"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"region":"eu-west","role":"` + role + `"}`))
	}))
	defer srv.Close()
	src := URLFenceSource(srv.Client(), srv.URL)
	if fenced, err := src(t.Context()); err != nil || !fenced {
		t.Fatalf("fenced=%v err=%v", fenced, err)
	}
	role = "PASSIVE"
	if fenced, err := src(t.Context()); err != nil || fenced {
		t.Fatalf("PASSIVE is not fenced: fenced=%v err=%v", fenced, err)
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer empty.Close()
	if _, err := URLFenceSource(empty.Client(), empty.URL)(t.Context()); err == nil {
		t.Fatal("a body without a role is an error")
	}
}

func TestFenceFromEnvPicksFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "role")
	if err := os.WriteFile(path, []byte("FENCED"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{FenceFileEnv: path, FenceIntervalEnv: "1s"}
	f := FenceFromEnv(t.Context(), func(k string) string { return env[k] })
	if f == nil || !f.Fenced() {
		t.Fatal("file source must fence at once")
	}
}

func TestURLSourceTreatsAnythingButARoleAsAReadError(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"UNKNOWN role":      {503, `{"region":"eu-west","role":"UNKNOWN"}`},
		"no role (problem)": {503, `{"type":"x","title":"Service Unavailable","status":503,"code":"COM-0503"}`},
		"garbage body":      {200, `<html>bad gateway</html>`},
		"500":               {500, `{"role":"FENCED"}`},
		"404":               {404, `{"role":"FENCED"}`},
		"502 from a proxy":  {502, ``},
		"lower role typo":   {200, `{"role":"PRIMARYY"}`},
	}
	for name, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(c.body))
		}))
		if _, err := URLFenceSource(srv.Client(), srv.URL)(t.Context()); err == nil {
			t.Errorf("%s must be a read error", name)
		}
		srv.Close()
	}
	// and a read error keeps the last state: a fenced region does not unfence because ops cannot read its database
	var body atomic.Value
	body.Store(`{"role":"FENCED"}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	defer srv.Close()
	f := &Fence{}
	src := URLFenceSource(srv.Client(), srv.URL)
	f.refresh(t.Context(), src)
	if !f.Fenced() {
		t.Fatal("must fence")
	}
	body.Store(`{"region":"eu-west","role":"UNKNOWN"}`)
	f.refresh(t.Context(), src)
	if !f.Fenced() {
		t.Fatal("UNKNOWN must keep the fence")
	}
	body.Store(`{"title":"unavailable","status":503}`)
	f.refresh(t.Context(), src)
	if !f.Fenced() {
		t.Fatal("a problem body must keep the fence")
	}
	body.Store(`{"role":"PASSIVE"}`)
	f.refresh(t.Context(), src)
	if f.Fenced() {
		t.Fatal("a real PASSIVE role unfences")
	}
}

func TestFileSourceRejectsAGarbageRole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "role")
	if err := os.WriteFile(path, []byte("UNKNOWN"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := FileFenceSource(path)(t.Context()); err == nil {
		t.Fatal("an unknown role in the file is a read error")
	}
}

func TestFenceStopEndsThePoll(t *testing.T) {
	var reads atomic.Int64
	f := NewFence(t.Context(), func(context.Context) (bool, error) { reads.Add(1); return false, nil }, time.Second)
	f.Stop()
	n := reads.Load()
	time.Sleep(1300 * time.Millisecond)
	if reads.Load() != n {
		t.Fatal("the poll kept running after Stop")
	}
	(*Fence)(nil).Stop() // a nil fence is fine
}

func TestFencedRequestsAreCountedAndLogged(t *testing.T) {
	shared = &httpMetrics{routes: map[string]*routeStat{}}
	var fenced atomic.Bool
	fenced.Store(true)
	f := NewFence(t.Context(), func(context.Context) (bool, error) { return fenced.Load(), nil }, time.Second)
	// the process fence sits in newRouter after metrics; emulate it for this router
	processFence.Store(f)
	processFenceOnce.Do(func() {})
	defer processFence.Store(nil)
	r := NewRouterWithoutTenant()
	r.Get("/v1/thing", func(w http.ResponseWriter, _ *http.Request) { JSON(w, 200, nil) })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/thing", nil))
	if rec.Code != 503 {
		t.Fatalf("status %d", rec.Code)
	}
	var out strings.Builder
	shared.write(&out)
	if !strings.Contains(out.String(), `status="503"`) {
		t.Fatalf("a fenced 503 must be counted: %s", out.String())
	}
}
