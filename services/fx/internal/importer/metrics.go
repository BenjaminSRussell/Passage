package importer

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

type seriesKey struct{ provider, rateType string }

type stateKey struct {
	seriesKey
	tenantID string
}

type state struct {
	degraded    bool
	degradedRun int
	lastSuccess time.Time
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// Metrics keeps state per tenant and publishes one series per (provider, rate_type):
// fx_provider_degraded is the worst state across tenants (1 if any tenant is degraded),
// the runs counter is summed, and the last-success timestamp is the oldest across tenants.
type Metrics struct {
	mu     sync.Mutex
	states map[stateKey]*state
}

func NewMetrics() *Metrics { return &Metrics{states: map[stateKey]*state{}} }

func (m *Metrics) record(ctx context.Context, provider, rateType string, degraded bool, lastSuccess *time.Time) {
	if m == nil {
		return
	}
	p, _ := tenant.FromContext(ctx)
	k := stateKey{seriesKey{provider, rateType}, p.TenantID}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.states[k]
	if st == nil {
		st = &state{}
		m.states[k] = st
	}
	st.degraded = degraded
	if degraded {
		st.degradedRun++
	}
	if lastSuccess != nil {
		st.lastSuccess = *lastSuccess
	}
}

func (m *Metrics) Seed(ctx context.Context, provider, rateType string, lastSuccess *time.Time) {
	if lastSuccess != nil {
		m.record(ctx, provider, rateType, false, lastSuccess)
	}
}

type aggregate struct {
	degraded    bool
	runs        int
	lastSuccess time.Time
}

func (m *Metrics) snapshot() ([]seriesKey, map[seriesKey]*aggregate) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[seriesKey]*aggregate{}
	for k, st := range m.states {
		a := out[k.seriesKey]
		if a == nil {
			a = &aggregate{}
			out[k.seriesKey] = a
		}
		a.degraded = a.degraded || st.degraded
		a.runs += st.degradedRun
		if !st.lastSuccess.IsZero() && (a.lastSuccess.IsZero() || st.lastSuccess.Before(a.lastSuccess)) {
			a.lastSuccess = st.lastSuccess
		}
	}
	keys := make([]seriesKey, 0, len(out))
	for k := range out {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool {
		if keys[a].provider != keys[b].provider {
			return keys[a].provider < keys[b].provider
		}
		return keys[a].rateType < keys[b].rateType
	})
	return keys, out
}

func labels(k seriesKey) string {
	return fmt.Sprintf(`{provider="%s",rate_type="%s"}`, labelEscaper.Replace(k.provider), labelEscaper.Replace(k.rateType))
}

func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		keys, agg := m.snapshot()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		tenant.WriteReplayMetrics(w)
		fmt.Fprint(w, "# HELP fx_provider_degraded 1 when the latest import for the provider and rate type ended in an outage for any tenant.\n# TYPE fx_provider_degraded gauge\n")
		for _, k := range keys {
			v := 0
			if agg[k].degraded {
				v = 1
			}
			fmt.Fprintf(w, "fx_provider_degraded%s %d\n", labels(k), v)
		}
		fmt.Fprint(w, "# HELP fx_provider_degraded_runs_total Import runs that ended in an outage.\n# TYPE fx_provider_degraded_runs_total counter\n")
		for _, k := range keys {
			fmt.Fprintf(w, "fx_provider_degraded_runs_total%s %d\n", labels(k), agg[k].runs)
		}
		fmt.Fprint(w, "# HELP fx_provider_last_success_timestamp_seconds Unix time of the oldest last successful import across tenants.\n# TYPE fx_provider_last_success_timestamp_seconds gauge\n")
		for _, k := range keys {
			if !agg[k].lastSuccess.IsZero() {
				fmt.Fprintf(w, "fx_provider_last_success_timestamp_seconds%s %d\n", labels(k), agg[k].lastSuccess.Unix())
			}
		}
	})
}
