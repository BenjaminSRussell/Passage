package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const unmatchedRoute = "unmatched"

var latencyBounds = []float64{0.005, 0.025, 0.1, 0.5, 1, 5}

type requestKey struct {
	route  string
	status int
}

type latency struct {
	buckets []int64
	count   int64
	sum     float64
}

type requestMetrics struct {
	mu       sync.Mutex
	requests map[requestKey]int64
	latency  map[string]*latency
}

func newRequestMetrics() requestMetrics {
	return requestMetrics{requests: map[requestKey]int64{}, latency: map[string]*latency{}}
}

func (m *requestMetrics) observe(route string, status int, seconds float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[requestKey{route, status}]++
	l := m.latency[route]
	if l == nil {
		l = &latency{buckets: make([]int64, len(latencyBounds))}
		m.latency[route] = l
	}
	l.count++
	l.sum += seconds
	for i, bound := range latencyBounds {
		if seconds <= bound {
			l.buckets[i]++
		}
	}
}

func (m *requestMetrics) write(w io.Writer) {
	m.mu.Lock()
	requests := make(map[requestKey]int64, len(m.requests))
	for k, v := range m.requests {
		requests[k] = v
	}
	latencies := make(map[string]latency, len(m.latency))
	for route, l := range m.latency {
		latencies[route] = latency{buckets: append([]int64(nil), l.buckets...), count: l.count, sum: l.sum}
	}
	m.mu.Unlock()
	_, _ = fmt.Fprint(w, "# HELP payables_http_requests_total HTTP requests by route and status.\n# TYPE payables_http_requests_total counter\n")
	keys := make([]requestKey, 0, len(requests))
	for k := range requests {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		return keys[i].status < keys[j].status
	})
	for _, k := range keys {
		_, _ = fmt.Fprintf(w, "payables_http_requests_total{route=%q,status=\"%d\"} %d\n", k.route, k.status, requests[k])
	}
	_, _ = fmt.Fprint(w, "# HELP payables_http_request_duration_seconds HTTP request latency by route.\n# TYPE payables_http_request_duration_seconds histogram\n")
	routes := make([]string, 0, len(latencies))
	for route := range latencies {
		routes = append(routes, route)
	}
	sort.Strings(routes)
	for _, route := range routes {
		l := latencies[route]
		for i, bound := range latencyBounds {
			_, _ = fmt.Fprintf(w, "payables_http_request_duration_seconds_bucket{route=%q,le=\"%s\"} %d\n", route, strconv.FormatFloat(bound, 'g', -1, 64), l.buckets[i])
		}
		_, _ = fmt.Fprintf(w, "payables_http_request_duration_seconds_bucket{route=%q,le=\"+Inf\"} %d\n", route, l.count)
		_, _ = fmt.Fprintf(w, "payables_http_request_duration_seconds_sum{route=%q} %g\n", route, l.sum)
		_, _ = fmt.Fprintf(w, "payables_http_request_duration_seconds_count{route=%q} %d\n", route, l.count)
	}
}

func (a *API) Instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		rctx := chi.NewRouteContext()
		next.ServeHTTP(ww, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx)))
		route := rctx.RoutePattern()
		if route == "" {
			route = unmatchedRoute
		}
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		a.http.observe(route, status, time.Since(start).Seconds())
	})
}
