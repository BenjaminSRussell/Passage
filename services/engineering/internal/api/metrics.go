package api

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

var latencyBounds = [...]float64{0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5}

type routeStat struct {
	byClass [6]int64
	buckets [len(latencyBounds) + 1]int64
	sum     float64
}

type routeStats struct {
	mu     sync.Mutex
	routes map[string]*routeStat
}

func (s *routeStats) record(route string, status int, elapsed time.Duration) {
	seconds := elapsed.Seconds()
	bucket := sort.SearchFloat64s(latencyBounds[:], seconds)
	s.mu.Lock()
	defer s.mu.Unlock()
	stat, ok := s.routes[route]
	if !ok {
		stat = &routeStat{}
		s.routes[route] = stat
	}
	stat.byClass[status/100%6]++
	stat.buckets[bucket]++
	stat.sum += seconds
}

func (a *API) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		a.stats.record(chi.RouteContext(r.Context()).RoutePattern(), status, time.Since(start))
	})
}

func (a *API) Metrics() http.Handler {
	counters := []struct {
		name, help string
		value      *atomic.Int64
	}{
		{"engineering_bom_versions_created_total", "BOM versions created.", &a.bomCreated},
		{"engineering_bom_versions_failed_total", "BOM requests that failed in the store or domain with an unexpected error.", &a.bomFailed},
		{"engineering_bom_versions_activated_total", "BOM version activate requests that succeeded, including idempotent repeats.", &a.bomActivated},
		{"engineering_bom_versions_cancelled_total", "BOM version cancel requests that succeeded, including idempotent repeats.", &a.bomCancelled},
		{"engineering_bom_versions_updated_total", "Draft BOM version edits that were saved.", &a.bomUpdated},
		{"engineering_bom_version_conflicts_total", "Draft BOM version edits refused because another save came first (ENG-BOM-VERSION-CONFLICT).", &a.bomConflicts},
		{"engineering_route_versions_updated_total", "Draft route version edits that were saved.", &a.routeUpdated},
		{"engineering_route_version_conflicts_total", "Draft route version edits refused because another save came first (ENG-ROUTE-VERSION-CONFLICT).", &a.routeConflicts},
		{"engineering_ecos_released_total", "ECOs released to production.", &a.ecosReleased},
		{"engineering_ecos_failed_total", "ECO requests that failed in the store or domain with an unexpected error, including an unavailable approver.", &a.ecosFailed},
		{"engineering_ecrs_created_total", "Engineering change requests created as drafts.", &a.ecrsCreated},
		{"engineering_ecr_transitions_total", "ECR submit, review, approve, reject and cancel requests that changed a request.", &a.ecrsDecided},
		{"engineering_ecrs_failed_total", "ECR requests that failed in the store or domain with an unexpected error.", &a.ecrsFailed},
		{"engineering_explosions_total", "BOM explosions served.", &a.explosions},
		{"engineering_calendars_created_total", "Work calendars created.", &a.calendarsCreated},
		{"engineering_resources_created_total", "Resources created.", &a.resourcesCreated},
		{"engineering_resources_updated_total", "Resource edits that were saved.", &a.resourcesUpdated},
		{"engineering_resources_deleted_total", "Resources deleted.", &a.resourcesDeleted},
		{"engineering_resource_version_conflicts_total", "Resource edits refused because another save came first (ENG-RES-VERSION-CONFLICT).", &a.resourceConflicts},
		{"engineering_resource_requests_failed_total", "Resource and calendar requests that failed in the store or domain with an unexpected error.", &a.resourceFailed},
		{"engineering_route_requests_failed_total", "Route version read and activate requests that failed in the store with an unexpected error.", &a.routeFailed},
		{"engineering_route_versions_activated_total", "Route version activate requests that succeeded, including idempotent repeats.", &a.routeActivated},
		{"engineering_route_versions_cancelled_total", "Route version cancel requests that succeeded, including idempotent repeats.", &a.routeCancelled},
		{"engineering_bom_versions_copied_total", "BOM versions copied into a new draft.", &a.bomCopied},
		{"engineering_route_versions_copied_total", "Route versions copied into a new draft.", &a.routeCopied},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		for _, c := range counters {
			fmt.Fprintf(w, "# HELP %[1]s %[2]s\n# TYPE %[1]s counter\n%[1]s %[3]d\n", c.name, c.help, c.value.Load())
		}
		a.stats.write(w)
	})
}

func (s *routeStats) write(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	routes := make([]string, 0, len(s.routes))
	for route := range s.routes {
		routes = append(routes, route)
	}
	sort.Strings(routes)
	fmt.Fprint(w, "# HELP engineering_http_requests_total HTTP requests by route pattern and status class.\n# TYPE engineering_http_requests_total counter\n")
	for _, route := range routes {
		for class, n := range s.routes[route].byClass {
			if n > 0 {
				fmt.Fprintf(w, "engineering_http_requests_total{route=%q,class=\"%dxx\"} %d\n", route, class, n)
			}
		}
	}
	fmt.Fprint(w, "# HELP engineering_http_request_duration_seconds HTTP request duration by route pattern.\n# TYPE engineering_http_request_duration_seconds histogram\n")
	for _, route := range routes {
		stat := s.routes[route]
		var cumulative int64
		for i, n := range stat.buckets {
			cumulative += n
			le := "+Inf"
			if i < len(latencyBounds) {
				le = fmt.Sprint(latencyBounds[i])
			}
			fmt.Fprintf(w, "engineering_http_request_duration_seconds_bucket{route=%q,le=%q} %d\n", route, le, cumulative)
		}
		fmt.Fprintf(w, "engineering_http_request_duration_seconds_sum{route=%q} %g\nengineering_http_request_duration_seconds_count{route=%q} %d\n", route, stat.sum, route, cumulative)
	}
}
