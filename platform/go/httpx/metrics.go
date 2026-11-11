package httpx

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Shared HTTP metrics (PSG-50, FR-5.13). Every router from NewRouter records the same two series, so the
// observability alerts, the burn-rate recording rules and the load campaign's SLO rows read one name and label set:
//
//	http_requests_total{route,status}
//	http_request_duration_seconds{route}   (histogram: _bucket{le}, _sum, _count)
//
// route is the chi route pattern ("/v1/ledger/journal-entries/{id}"), never the raw path, and status is the
// three-digit status code (the alerts match status=~"5.."). The Prometheus job label names the service.
// /healthz and /readyz are not recorded: probes would dilute the latency and availability ratios.
//
// A service exposes the series by wrapping its admin /metrics handler: admin.Handle("/metrics", httpx.WithMetrics(h)).

const (
	unmatchedRoute = "unmatched"
	// maxRoutes bounds the label set. A router has a fixed number of patterns; the cap only stops a bug from
	// growing memory without limit. Routes past it are counted under overflowRoute.
	maxRoutes     = 1000
	overflowRoute = "other"
)

// latencyBounds are the histogram's upper bounds in seconds, dense around the FR-50.1 SLOs (50 ms to 2 s).
var latencyBounds = [...]float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type routeStat struct {
	buckets [len(latencyBounds)]atomic.Int64 // per bound, not cumulative; cumulated when written
	over    atomic.Int64                     // observations above the last bound
	sumNs   atomic.Int64
	status  sync.Map // int -> *atomic.Int64
}

type httpMetrics struct {
	mu     sync.RWMutex
	routes map[string]*routeStat
}

var shared = &httpMetrics{routes: map[string]*routeStat{}}

func (m *httpMetrics) stat(route string) *routeStat {
	m.mu.RLock()
	s := m.routes[route]
	m.mu.RUnlock()
	if s != nil {
		return s
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s = m.routes[route]; s != nil {
		return s
	}
	if len(m.routes) >= maxRoutes {
		route = overflowRoute
		if s = m.routes[route]; s != nil {
			return s
		}
	}
	s = &routeStat{}
	m.routes[route] = s
	return s
}

func (m *httpMetrics) observe(route string, status int, d time.Duration) {
	s := m.stat(route)
	c, ok := s.status.Load(status)
	if !ok {
		c, _ = s.status.LoadOrStore(status, new(atomic.Int64))
	}
	c.(*atomic.Int64).Add(1)
	s.sumNs.Add(int64(d))
	sec := d.Seconds()
	for i, b := range latencyBounds {
		if sec <= b {
			s.buckets[i].Add(1)
			return
		}
	}
	s.over.Add(1)
}

func (m *httpMetrics) write(w io.Writer) {
	m.mu.RLock()
	routes := make([]string, 0, len(m.routes))
	stats := make(map[string]*routeStat, len(m.routes))
	for r, s := range m.routes {
		routes = append(routes, r)
		stats[r] = s
	}
	m.mu.RUnlock()
	sort.Strings(routes)

	_, _ = fmt.Fprint(w, "# HELP http_requests_total HTTP requests by route pattern and status code.\n# TYPE http_requests_total counter\n")
	for _, r := range routes {
		var codes []int
		stats[r].status.Range(func(k, _ any) bool { codes = append(codes, k.(int)); return true })
		sort.Ints(codes)
		for _, code := range codes {
			c, _ := stats[r].status.Load(code)
			_, _ = fmt.Fprintf(w, "http_requests_total{route=%q,status=\"%d\"} %d\n", r, code, c.(*atomic.Int64).Load())
		}
	}
	_, _ = fmt.Fprint(w, "# HELP http_request_duration_seconds HTTP request duration by route pattern.\n# TYPE http_request_duration_seconds histogram\n")
	for _, r := range routes {
		s := stats[r]
		var cum int64
		for i, b := range latencyBounds {
			cum += s.buckets[i].Load()
			_, _ = fmt.Fprintf(w, "http_request_duration_seconds_bucket{route=%q,le=%q} %d\n", r, strconv.FormatFloat(b, 'g', -1, 64), cum)
		}
		cum += s.over.Load()
		_, _ = fmt.Fprintf(w, "http_request_duration_seconds_bucket{route=%q,le=\"+Inf\"} %d\n", r, cum)
		_, _ = fmt.Fprintf(w, "http_request_duration_seconds_sum{route=%q} %g\n", r, time.Duration(s.sumNs.Load()).Seconds())
		_, _ = fmt.Fprintf(w, "http_request_duration_seconds_count{route=%q} %d\n", r, cum)
	}
}

// recordMetrics times each request and counts it under its chi route pattern. It sits outside the recoverer, so
// a panic that becomes a 500 is counted as one.
func recordMetrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		route := unmatchedRoute
		if rc := chi.RouteContext(r.Context()); rc != nil {
			if p := rc.RoutePattern(); p != "" {
				route = p
			}
		}
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		shared.observe(route, status, time.Since(start))
	})
}

// WriteMetrics writes the shared HTTP series in Prometheus text format.
func WriteMetrics(w io.Writer) { shared.write(w) }

// WithMetrics serves h (a service's own /metrics output) followed by the shared HTTP series.
func WithMetrics(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		WriteMetrics(w)
	})
}

// DefaultAdminAddr is where ServeWithMetrics serves /metrics unless ADMIN_ADDR says otherwise.
const DefaultAdminAddr = ":9090"

// ServeWithMetrics serves h on addr like Serve and the shared HTTP series on the admin address (ADMIN_ADDR, default :9090,
// path /metrics only) for a service that has no admin listener of its own. Both listeners drain on shutdown and it returns
// only when both have stopped: when one fails, the other is cancelled and still drains its in-flight requests. The first
// non-nil error is returned.
func ServeWithMetrics(ctx context.Context, addr string, h http.Handler) error {
	adminAddr := os.Getenv("ADMIN_ADDR")
	if adminAddr == "" {
		adminAddr = DefaultAdminAddr
	}
	admin := http.NewServeMux()
	admin.Handle("/metrics", MetricsHandler())
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	for _, l := range []struct {
		addr string
		h    http.Handler
	}{{addr, h}, {adminAddr, admin}} {
		go func() {
			err := Serve(ctx, l.addr, l.h)
			cancel()
			errs <- err
		}()
	}
	first := <-errs
	second := <-errs
	if first != nil {
		return first
	}
	return second
}

// MetricsHandler serves only the shared HTTP series, for a service with no metrics of its own.
func MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		WriteMetrics(w)
	})
}
