package httpx

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMetricsRecordRoutePatternAndStatus(t *testing.T) {
	shared = &httpMetrics{routes: map[string]*routeStat{}}
	r := NewRouterWithoutTenant()
	r.Get("/v1/things/{id}", func(w http.ResponseWriter, _ *http.Request) { JSON(w, 200, "ok") })
	r.Post("/v1/things", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	r.Get("/v1/boom", func(http.ResponseWriter, *http.Request) { panic("x") })
	for _, c := range []struct{ m, p string }{
		{"GET", "/v1/things/1"}, {"GET", "/v1/things/2"}, {"POST", "/v1/things"}, {"GET", "/nope"},
		{"GET", "/healthz"}, {"GET", "/v1/boom"},
	} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(c.m, c.p, nil))
	}
	rec := httptest.NewRecorder()
	WithMetrics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("own_total 1\n")) })).
		ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	out := rec.Body.String()
	for _, want := range []string{
		"own_total 1\n",
		`http_requests_total{route="/v1/things/{id}",status="200"} 2`,
		`http_requests_total{route="/v1/things",status="500"} 1`,
		`http_requests_total{route="/v1/boom",status="500"} 1`,
		`http_requests_total{route="unmatched",status="404"} 1`,
		`http_request_duration_seconds_count{route="/v1/things/{id}"} 2`,
		`http_request_duration_seconds_bucket{route="/v1/things/{id}",le="+Inf"} 2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "healthz") || strings.Contains(out, "/v1/things/1") {
		t.Errorf("probe or raw path recorded:\n%s", out)
	}
}

func TestHistogramIsCumulativeAndRouteSetIsBounded(t *testing.T) {
	m := &httpMetrics{routes: map[string]*routeStat{}}
	m.observe("/a", 200, 3*time.Millisecond)
	m.observe("/a", 200, 80*time.Millisecond)
	m.observe("/a", 200, 20*time.Second) // above every bound
	var b strings.Builder
	m.write(&b)
	for _, want := range []string{`le="0.005"} 1`, `le="0.1"} 2`, `le="10"} 2`, `le="+Inf"} 3`, `_count{route="/a"} 3`} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q in:\n%s", want, b.String())
		}
	}
	for i := 0; i < maxRoutes+50; i++ {
		m.observe("/r"+strconv.Itoa(i), 200, time.Millisecond)
	}
	if n := len(m.routes); n > maxRoutes+1 {
		t.Errorf("route label set grew to %d", n)
	}
	if m.routes[overflowRoute] == nil {
		t.Error("routes past the cap are not counted under the overflow route")
	}
}

func TestServeWithMetricsServesTheSharedSeriesOnTheAdminPort(t *testing.T) {
	free := func() string {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		_ = l.Close()
		return addr
	}
	api, admin := free(), free()
	t.Setenv("ADMIN_ADDR", admin)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	r := NewRouterWithoutTenant()
	r.Get("/v1/ping", func(w http.ResponseWriter, _ *http.Request) { JSON(w, 200, map[string]string{"ok": "1"}) })
	go func() { done <- ServeWithMetrics(ctx, api, r) }()
	get := func(url string) (int, string) {
		var last error
		for range 50 {
			resp, err := http.Get(url)
			if err == nil {
				defer func() { _ = resp.Body.Close() }()
				b, _ := io.ReadAll(resp.Body)
				return resp.StatusCode, string(b)
			}
			last = err
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal(last)
		return 0, ""
	}
	if code, _ := get("http://" + api + "/v1/ping"); code != 200 {
		t.Fatalf("api %d", code)
	}
	code, body := get("http://" + admin + "/metrics")
	if code != 200 || !strings.Contains(body, `http_requests_total{route="/v1/ping",status="200"}`) {
		t.Fatalf("admin metrics %d: %s", code, body)
	}
	if code, _ := get("http://" + admin + "/v1/ping"); code != 404 {
		t.Fatalf("the admin port serves /metrics only, got %d", code)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ServeWithMetrics did not stop with its context")
	}
}
