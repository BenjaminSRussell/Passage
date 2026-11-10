package api

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

const (
	sweepTenantA = "0195f0a0-0000-7000-8000-0000000000a1"
	sweepTenantB = "0195f0a0-0000-7000-8000-0000000000b2"
)

// sweepFake records which tenant each sweep call ran for and can fail one tenant.
type sweepFake struct {
	*fakeStore
	mu       sync.Mutex
	calls    []string
	failFor  string
	afterHrs int
	window   [2]time.Time
}

func (f *sweepFake) record(ctx context.Context, what string) error {
	p, ok := tenant.FromContext(ctx)
	if !ok || p.UserID != SweepActor {
		return errors.New("sweeper ran without its principal")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, what+":"+p.TenantID)
	if p.TenantID == f.failFor {
		return errors.New("database down for " + p.TenantID)
	}
	return nil
}

func (f *sweepFake) SweepAndon(ctx context.Context, _ time.Time) (store.AndonSweepResult, error) {
	return store.AndonSweepResult{DowntimeRaised: 2}, f.record(ctx, JobAndon)
}

func (f *sweepFake) AutoClockOut(ctx context.Context, _ time.Time, after, _ int) ([]store.AutoClosed, error) {
	f.afterHrs = after
	return []store.AutoClosed{{}}, f.record(ctx, JobAutoClock)
}

func (f *sweepFake) ClaimRetries(ctx context.Context, _ time.Time, _ int) ([]store.RetryClaim, error) {
	return []store.RetryClaim{{}, {}}, f.record(ctx, JobProcessRetr)
}

func (f *sweepFake) SaveOEEBuckets(ctx context.Context, r store.OEERollup) (store.OEERollupResult, error) {
	f.window = [2]time.Time{r.From, r.To}
	return store.OEERollupResult{BucketMinutes: r.BucketMinutes, Changed: 3}, f.record(ctx, JobOEERollup)
}

func newSweeper(t *testing.T, f *sweepFake, now time.Time) (*API, *Sweeper) {
	t.Helper()
	a := New(f)
	a.now = func() time.Time { return now }
	sw, err := a.NewSweeper(DefaultSweepConfig([]string{sweepTenantA, sweepTenantB}))
	if err != nil {
		t.Fatal(err)
	}
	return a, sw
}

// Every job runs once for every tenant under the sweeper principal and moves the same counters as
// the HTTP jobs; the rollup recomputes the last four completed 15 minute buckets.
func TestSweeper_RunsEveryJobForEveryTenant(t *testing.T) {
	f := &sweepFake{fakeStore: &fakeStore{}}
	now := time.Date(2026, 11, 10, 9, 37, 0, 0, time.UTC)
	a, sw := newSweeper(t, f, now)
	for _, j := range sweepJobs {
		if failed := sw.RunJob(context.Background(), j); failed != 0 {
			t.Fatalf("%s: %d tenants failed", j, failed)
		}
		if runs, fails := sw.Counts(j); runs != 2 || fails != 0 {
			t.Fatalf("%s: runs %d failures %d", j, runs, fails)
		}
	}
	if len(f.calls) != 8 || f.calls[0] != JobAndon+":"+sweepTenantA || f.calls[1] != JobAndon+":"+sweepTenantB {
		t.Fatalf("calls = %v", f.calls)
	}
	if f.afterHrs != 16 {
		t.Fatalf("auto clock-out after %d hours", f.afterHrs)
	}
	if want := time.Date(2026, 11, 10, 8, 30, 0, 0, time.UTC); !f.window[0].Equal(want) || !f.window[1].Equal(want.Add(time.Hour)) {
		t.Fatalf("rollup window = %v", f.window)
	}
	if a.andonsRaised.Load() != 4 || a.autoClockedOut.Load() != 2 || a.processRetried.Load() != 4 || a.oeeChanged.Load() != 6 {
		t.Fatalf("counters: andon %d clock %d retry %d oee %d", a.andonsRaised.Load(), a.autoClockedOut.Load(), a.processRetried.Load(), a.oeeChanged.Load())
	}
}

// One tenant failing is logged and counted and the next tenant still runs.
func TestSweeper_OneTenantFailingDoesNotStopTheRest(t *testing.T) {
	f := &sweepFake{fakeStore: &fakeStore{}, failFor: sweepTenantA}
	_, sw := newSweeper(t, f, time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC))
	if failed := sw.RunJob(context.Background(), JobAndon); failed != 1 {
		t.Fatalf("failed = %d, want 1", failed)
	}
	if len(f.calls) != 2 {
		t.Fatalf("calls = %v: tenant B did not run", f.calls)
	}
	if runs, fails := sw.Counts(JobAndon); runs != 2 || fails != 1 {
		t.Fatalf("runs %d failures %d", runs, fails)
	}
	var out strings.Builder
	sw.WriteMetrics(&out)
	if !strings.Contains(out.String(), `shopfloor_sweep_failures_total{job="andon"} 1`) {
		t.Fatalf("metrics = %s", out.String())
	}
}

// A cancelled context stops the loop and Run returns; a zero interval switches a job off.
func TestSweeper_RunStopsWithContextAndHonoursOff(t *testing.T) {
	f := &sweepFake{fakeStore: &fakeStore{}}
	a := New(f)
	cfg := DefaultSweepConfig([]string{sweepTenantA})
	cfg.RollupEvery, cfg.AutoClockEvery, cfg.RetryEvery = 0, 0, 0
	sw, err := a.NewSweeper(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sw.Run(ctx); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if runs, _ := sw.Counts(JobAndon); runs > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("andon did not run at start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if runs, _ := sw.Counts(JobOEERollup); runs != 0 {
		t.Fatalf("rollup ran %d times while off", runs)
	}
}

func TestSweepConfig_Validation(t *testing.T) {
	if _, err := ParseSweepTenants("not-a-uuid"); err == nil {
		t.Error("bad tenant id accepted")
	}
	if ids, err := ParseSweepTenants(sweepTenantA + ", " + sweepTenantA + "," + sweepTenantB); err != nil || len(ids) != 2 {
		t.Errorf("tenants = %v, %v", ids, err)
	}
	if err := DefaultSweepConfig(nil).Validate(); err == nil {
		t.Error("no tenants accepted")
	}
	cfg := DefaultSweepConfig([]string{sweepTenantA})
	cfg.AndonEvery = time.Second
	if err := cfg.Validate(); err == nil {
		t.Error("1s interval accepted")
	}
	cfg = DefaultSweepConfig([]string{sweepTenantA})
	cfg.AutoClockAfterHour = 2
	if err := cfg.Validate(); err == nil {
		t.Error("2 hour auto clock-out accepted")
	}
}
