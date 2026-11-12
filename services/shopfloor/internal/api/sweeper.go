package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
)

// SweepActor is the user id the sweeper writes into audit columns and logs.
const SweepActor = "system:shopfloor-sweeper"

// Job names of the sweeper, the values of the "job" label on its metrics and logs.
const (
	JobAndon       = "andon"
	JobOEERollup   = "oee-rollup"
	JobAutoClock   = "auto-clock-out"
	JobProcessRetr = "process-retry"
)

var sweepJobs = []string{JobAndon, JobOEERollup, JobAutoClock, JobProcessRetr}

const (
	sweepJobTimeout = 2 * time.Minute
	// rollupBuckets is how many completed buckets each rollup recomputes, so a late machine event
	// is still counted: the rollup upserts by bucket.
	rollupBuckets    = 4
	rollupBucketSize = 15 * time.Minute
)

// TenantRegistry lists the tenants to sweep, for a deployment that keeps them in the identity service
// instead of in configuration.
type TenantRegistry interface {
	Tenants(ctx context.Context) ([]string, error)
}

// SweepConfig configures the sweeper. A zero interval switches that job off. The tenants swept are
// TenantIDs plus, when Registry is set, the tenants it lists at the start of every run.
type SweepConfig struct {
	TenantIDs          []string
	Registry           TenantRegistry
	AndonEvery         time.Duration
	RollupEvery        time.Duration
	AutoClockEvery     time.Duration
	RetryEvery         time.Duration
	AutoClockAfterHour int
}

// DefaultSweepConfig is the PRD schedule: andon and process retries every minute, the OEE rollup
// every 15 minutes and the auto clock-out hourly (BR-25.3, BR-25.10, FR-25.10, FR-25.17).
func DefaultSweepConfig(tenants []string) SweepConfig {
	return SweepConfig{TenantIDs: tenants, AndonEvery: time.Minute, RollupEvery: 15 * time.Minute,
		AutoClockEvery: time.Hour, RetryEvery: time.Minute, AutoClockAfterHour: domain.DefaultAutoClockOutHours}
}

// ParseSweepTenants parses a comma-separated list of tenant UUIDs. An empty list is no tenants.
func ParseSweepTenants(raw string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	for _, part := range strings.Split(raw, ",") {
		id, ok := ids.Canonical(strings.TrimSpace(part))
		if !ok || id == ids.Nil {
			return nil, fmt.Errorf("tenant id %q is not a UUID", part)
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// Validate checks the schedule.
func (c SweepConfig) Validate() error {
	if len(c.TenantIDs) == 0 && c.Registry == nil {
		return errors.New("no tenants to sweep")
	}
	for name, d := range map[string]time.Duration{"andon": c.AndonEvery, "oee rollup": c.RollupEvery,
		"auto clock-out": c.AutoClockEvery, "process retry": c.RetryEvery} {
		if d < 0 || (d > 0 && d < 10*time.Second) {
			return fmt.Errorf("%s interval must be 0 (off) or at least 10s, got %s", name, d)
		}
	}
	if err := domain.CheckAutoClockOutHours(c.AutoClockAfterHour); err != nil {
		return err
	}
	return nil
}

// Sweeper runs the shop floor's scheduled jobs in process for a list of tenants: the andon sweep,
// the OEE rollup, the auto clock-out and the process message retry. Each job runs per tenant under
// that tenant's row-level security with its own timeout, and a failure in one tenant or job is
// logged and counted and does not stop the rest. Every job is safe to repeat (the HTTP jobs it
// replaces carry no Idempotency-Key), so a second sweeper running by mistake does no harm; the
// chart runs one.
type Sweeper struct {
	a        *API
	cfg      SweepConfig
	runs     map[string]*atomic.Int64
	failures map[string]*atomic.Int64

	mu            sync.Mutex
	lastRegistry  []string // the registry's last good answer, used while it is unreachable
	registryFails atomic.Int64
}

// NewSweeper returns a sweeper over the API's store and counters.
func (a *API) NewSweeper(cfg SweepConfig) (*Sweeper, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	s := &Sweeper{a: a, cfg: cfg, runs: map[string]*atomic.Int64{}, failures: map[string]*atomic.Int64{}}
	for _, j := range sweepJobs {
		s.runs[j], s.failures[j] = new(atomic.Int64), new(atomic.Int64)
	}
	return s, nil
}

// Run starts every enabled job on its own ticker, each once at start, and returns when ctx ends.
func (s *Sweeper) Run(ctx context.Context) {
	every := map[string]time.Duration{JobAndon: s.cfg.AndonEvery, JobOEERollup: s.cfg.RollupEvery,
		JobAutoClock: s.cfg.AutoClockEvery, JobProcessRetr: s.cfg.RetryEvery}
	done := make(chan struct{}, len(sweepJobs))
	started := 0
	for _, name := range sweepJobs {
		d := every[name]
		if d <= 0 {
			slog.Info("shopfloor sweep job off", "job", name)
			continue
		}
		started++
		go func(name string, d time.Duration) {
			defer func() { done <- struct{}{} }()
			s.RunJob(ctx, name)
			t := time.NewTicker(d)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					s.RunJob(ctx, name)
				}
			}
		}(name, d)
	}
	for i := 0; i < started; i++ {
		<-done
	}
}

// Tenants is the list a run sweeps: the configured tenants and the registry's. A registry that cannot be
// read does not stop the run: the last list it gave is used (and the failure counted and logged), so a
// short identity outage does not stop the shop floor's clocks.
func (s *Sweeper) Tenants(ctx context.Context) ([]string, error) {
	out := append([]string(nil), s.cfg.TenantIDs...)
	if s.cfg.Registry == nil {
		return out, nil
	}
	listed, err := s.cfg.Registry.Tenants(ctx)
	s.mu.Lock()
	if err == nil {
		s.lastRegistry = listed
	} else {
		listed = s.lastRegistry
	}
	s.mu.Unlock()
	if err != nil {
		s.registryFails.Add(1)
		slog.Error("shopfloor sweep: tenant registry not read", "err", err, "usingLastList", len(listed))
		if len(listed) == 0 && len(out) == 0 {
			return nil, err
		}
	}
	seen := map[string]bool{}
	for _, id := range out {
		seen[id] = true
	}
	for _, id := range listed {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// RunJob runs one job once for every tenant, in order, and returns the number of tenants that failed.
func (s *Sweeper) RunJob(ctx context.Context, job string) int {
	failed := 0
	tenants, err := s.Tenants(ctx)
	if err != nil {
		s.failures[job].Add(1)
		return 1
	}
	for _, tenantID := range tenants {
		if ctx.Err() != nil {
			return failed
		}
		jctx, cancel := context.WithTimeout(tenant.With(ctx, tenant.Principal{TenantID: tenantID, UserID: SweepActor}), sweepJobTimeout)
		s.runs[job].Add(1)
		if err := s.runTenant(jctx, job); err != nil {
			failed++
			s.failures[job].Add(1)
			slog.Error("shopfloor sweep failed", "job", job, "tenantId", tenantID, "err", err)
		}
		cancel()
	}
	return failed
}

func (s *Sweeper) runTenant(ctx context.Context, job string) error {
	a := s.a
	now := a.now().UTC()
	switch job {
	case JobAndon:
		out, err := a.store.SweepAndon(ctx, now)
		if err != nil {
			return err
		}
		a.andonsRaised.Add(int64(out.DowntimeRaised + out.ScrapRateRaised))
	case JobOEERollup:
		to := now.Truncate(rollupBucketSize)
		_, err := a.rollup(ctx, now, nil, to.Add(-rollupBuckets*rollupBucketSize), to, rollupBucketSize)
		return err
	case JobAutoClock:
		items, err := a.store.AutoClockOut(ctx, now, s.cfg.AutoClockAfterHour, maxAutoClockBatch)
		if err != nil {
			return err
		}
		a.autoClockedOut.Add(int64(len(items)))
	case JobProcessRetr:
		claims, err := a.store.ClaimRetries(ctx, now, maxRetryBatch)
		if err != nil {
			return err
		}
		a.processRetried.Add(int64(len(claims)))
	default:
		return fmt.Errorf("unknown sweep job %q", job)
	}
	return nil
}

// Counts reports runs and failures of a job.
func (s *Sweeper) Counts(job string) (runs, failures int64) {
	if r, ok := s.runs[job]; ok {
		return r.Load(), s.failures[job].Load()
	}
	return 0, 0
}

// WriteMetrics writes the sweeper's counters in Prometheus text format.
func (s *Sweeper) WriteMetrics(w io.Writer) {
	fmt.Fprint(w, "# HELP shopfloor_sweep_runs_total Sweep runs per job and tenant.\n# TYPE shopfloor_sweep_runs_total counter\n")
	for _, j := range sweepJobs {
		fmt.Fprintf(w, "shopfloor_sweep_runs_total{job=%q} %d\n", j, s.runs[j].Load())
	}
	fmt.Fprintf(w, "# HELP shopfloor_sweep_registry_failures_total Reads of the tenant registry that failed.\n# TYPE shopfloor_sweep_registry_failures_total counter\nshopfloor_sweep_registry_failures_total %d\n", s.registryFails.Load())
	fmt.Fprint(w, "# HELP shopfloor_sweep_failures_total Sweep runs that failed, per job.\n# TYPE shopfloor_sweep_failures_total counter\n")
	for _, j := range sweepJobs {
		fmt.Fprintf(w, "shopfloor_sweep_failures_total{job=%q} %d\n", j, s.failures[j].Load())
	}
}
