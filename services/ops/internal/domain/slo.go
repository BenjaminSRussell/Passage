package domain

import (
	"sort"
	"time"
)

// SLO report (PSG-50, FR-50.1). The figures come from the shared HTTP series every service exposes
// (platform/go/httpx: http_requests_total{route,status}, http_request_duration_seconds{route}), read from Prometheus per
// scrape job (one job per service). A service breaches when its availability is below the target or its p95 latency is
// above the target over the window.
const (
	CodeSLOUnavailable = "OPS-1725"
	CodeSLOInvalid     = "OPS-1726"
	// DefaultAvailabilityTarget is 99.9 % (the availability burn-rate rules of the observability chart).
	DefaultAvailabilityTarget = 0.999
	// DefaultLatencyP95 is the slowest FR-50.1 interactive target (2 s); a tighter per-service target is a config entry.
	DefaultLatencyP95 = 2 * time.Second
)

// SLOWindows are the report windows (Prometheus range selectors).
var SLOWindows = map[string]time.Duration{"1h": time.Hour, "6h": 6 * time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour}

// SLOTargets are the objectives: one availability and one p95 latency for every service, with per-service latency overrides.
type SLOTargets struct {
	Availability float64
	LatencyP95   time.Duration
	// LatencyP95By overrides LatencyP95 for one service (scrape job).
	LatencyP95By map[string]time.Duration
}

// LatencyFor is the p95 target of service.
func (t SLOTargets) LatencyFor(service string) time.Duration {
	if d, ok := t.LatencyP95By[service]; ok {
		return d
	}
	return t.LatencyP95
}

// SLOSample is one service's measured window: requests, failed (5xx) requests and the p95 latency in seconds (nil when the
// service served no request with a duration in the window).
type SLOSample struct {
	Service  string
	Requests float64
	Failed   float64
	P95      *float64
}

// SLOLine is one service in the report.
type SLOLine struct {
	Service            string   `json:"service"`
	Requests           float64  `json:"requests"`
	Availability       *float64 `json:"availability"`
	AvailabilityTarget float64  `json:"availabilityTarget"`
	LatencyP95Ms       *float64 `json:"latencyP95Ms"`
	LatencyTargetMs    float64  `json:"latencyTargetMs"`
	ErrorBudgetLeft    *float64 `json:"errorBudgetRemaining"`
	AvailabilityBreach bool     `json:"availabilityBreached"`
	LatencyBreach      bool     `json:"latencyBreached"`
	Breached           bool     `json:"breached"`
	BreachedSince      *string  `json:"breachedSince,omitempty"`
	NoTrafficInWindow  bool     `json:"noTraffic"`
}

// SLOReport is GET /v1/ops/slo.
type SLOReport struct {
	Window      string    `json:"window"`
	GeneratedAt time.Time `json:"generatedAt"`
	Services    []SLOLine `json:"services"`
	Breached    int       `json:"breached"`
}

// EvaluateSLO turns samples into report lines, sorted by service. A service with no request in the window is reported with
// no availability and is never breached.
func EvaluateSLO(window string, samples []SLOSample, t SLOTargets, now time.Time) SLOReport {
	rep := SLOReport{Window: window, GeneratedAt: now.UTC(), Services: []SLOLine{}}
	for _, s := range samples {
		target := t.LatencyFor(s.Service)
		line := SLOLine{Service: s.Service, Requests: s.Requests, AvailabilityTarget: t.Availability, LatencyTargetMs: float64(target.Milliseconds())}
		if s.Requests <= 0 {
			line.NoTrafficInWindow = true
		} else {
			avail := 1 - s.Failed/s.Requests
			line.Availability = &avail
			budget := 1 - t.Availability
			if budget > 0 {
				left := 1 - (s.Failed/s.Requests)/budget
				line.ErrorBudgetLeft = &left
			}
			line.AvailabilityBreach = avail < t.Availability
			if s.P95 != nil {
				ms := *s.P95 * 1000
				line.LatencyP95Ms = &ms
				line.LatencyBreach = ms > line.LatencyTargetMs
			}
		}
		line.Breached = line.AvailabilityBreach || line.LatencyBreach
		if line.Breached {
			rep.Breached++
		}
		rep.Services = append(rep.Services, line)
	}
	sort.Slice(rep.Services, func(i, j int) bool { return rep.Services[i].Service < rep.Services[j].Service })
	return rep
}
