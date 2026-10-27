package importer

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
)

type seriesKey struct{ provider, rateType string }

type Metrics struct {
	mu       sync.Mutex
	degraded map[seriesKey]bool
	runs     map[seriesKey]int
}

func NewMetrics() *Metrics {
	return &Metrics{degraded: map[seriesKey]bool{}, runs: map[seriesKey]int{}}
}

func (m *Metrics) set(provider, rateType string, degraded bool) {
	if m == nil {
		return
	}
	k := seriesKey{provider, rateType}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.degraded[k] = degraded
	if degraded {
		m.runs[k]++
	}
}

func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		keys := make([]seriesKey, 0, len(m.degraded))
		for k := range m.degraded {
			keys = append(keys, k)
		}
		gauge := make(map[seriesKey]bool, len(keys))
		runs := make(map[seriesKey]int, len(keys))
		for _, k := range keys {
			gauge[k], runs[k] = m.degraded[k], m.runs[k]
		}
		m.mu.Unlock()
		sort.Slice(keys, func(a, b int) bool {
			if keys[a].provider != keys[b].provider {
				return keys[a].provider < keys[b].provider
			}
			return keys[a].rateType < keys[b].rateType
		})
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprint(w, "# HELP fx_provider_degraded 1 when the latest import for the provider and rate type ended in an outage.\n# TYPE fx_provider_degraded gauge\n")
		for _, k := range keys {
			v := 0
			if gauge[k] {
				v = 1
			}
			fmt.Fprintf(w, "fx_provider_degraded{provider=%q,rate_type=%q} %d\n", k.provider, k.rateType, v)
		}
		fmt.Fprint(w, "# HELP fx_provider_degraded_runs_total Import runs that ended in an outage.\n# TYPE fx_provider_degraded_runs_total counter\n")
		for _, k := range keys {
			fmt.Fprintf(w, "fx_provider_degraded_runs_total{provider=%q,rate_type=%q} %d\n", k.provider, k.rateType, runs[k])
		}
	})
}
