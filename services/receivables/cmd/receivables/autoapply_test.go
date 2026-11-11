package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

type fakeAutoApplier struct {
	mu       sync.Mutex
	entities map[string][]string
	runs     []tenant.Principal
}

func (f *fakeAutoApplier) AutoApplyLegalEntities(ctx context.Context) ([]string, error) {
	p, _ := tenant.FromContext(ctx)
	return f.entities[p.TenantID], nil
}

func (f *fakeAutoApplier) AutoApplyDue(ctx context.Context, _ int) (store.AutoApplyResult, error) {
	p, _ := tenant.FromContext(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, p)
	return store.AutoApplyResult{Applied: 1}, nil
}

func TestAutoApplyJob_RunsEachEntityOfEachTenantScoped(t *testing.T) {
	a, b, leA, leB := ids.New(), ids.New(), ids.New(), ids.New()
	f := &fakeAutoApplier{entities: map[string][]string{a: {leA}, b: {leB}}}
	job, err := newAutoApplyJob(env(map[string]string{"RECEIVABLES_AUTO_APPLY_TENANT_IDS": a + "," + b + "," + a}), f)
	if err != nil || job == nil {
		t.Fatalf("job = %v, %v", job, err)
	}
	job.once(context.Background())
	if len(f.runs) != 2 {
		t.Fatalf("%d runs, want one per tenant entity", len(f.runs))
	}
	own := map[string]string{a: leA, b: leB}
	for _, p := range f.runs {
		if p.LegalEntityID != own[p.TenantID] || p.UserID != autoApplyActor {
			t.Errorf("run principal %+v", p)
		}
	}
}

func TestAutoApplyJob_Config(t *testing.T) {
	good := ids.New()
	if j, err := newAutoApplyJob(env(nil), nil); j != nil || err != nil {
		t.Fatalf("unset: %v %v", j, err)
	}
	if j, err := newAutoApplyJob(env(map[string]string{"RECEIVABLES_AUTO_APPLY_TENANT_IDS": good, "RECEIVABLES_AUTO_APPLY_AT": "02:30"}), nil); err != nil || j.sched.at != 150 {
		t.Fatalf("daily: %+v %v", j, err)
	}
	if j, err := newAutoApplyJob(env(map[string]string{"RECEIVABLES_AUTO_APPLY_TENANT_IDS": good}), nil); err != nil || j.sched.at != -1 || j.sched.interval != 15*time.Minute {
		t.Fatalf("default: %+v %v", j, err)
	}
	for name, m := range map[string]map[string]string{
		"bad tenant":     {"RECEIVABLES_AUTO_APPLY_TENANT_IDS": "nope"},
		"bad time":       {"RECEIVABLES_AUTO_APPLY_TENANT_IDS": good, "RECEIVABLES_AUTO_APPLY_AT": "9pm"},
		"short interval": {"RECEIVABLES_AUTO_APPLY_TENANT_IDS": good, "RECEIVABLES_AUTO_APPLY_INTERVAL": "5s"},
		"both":           {"RECEIVABLES_AUTO_APPLY_TENANT_IDS": good, "RECEIVABLES_AUTO_APPLY_AT": "02:30", "RECEIVABLES_AUTO_APPLY_INTERVAL": "1h"},
	} {
		if _, err := newAutoApplyJob(env(m), nil); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestSchedule_NextDailyAndLoopStopsOnCancel(t *testing.T) {
	for _, c := range []struct{ now, want string }{
		{"2027-03-31T02:29:59Z", "2027-03-31T02:30:00Z"},
		{"2027-03-31T02:30:00Z", "2027-04-01T02:30:00Z"},
		{"2027-03-31T23:59:00+01:00", "2027-04-01T02:30:00Z"},
	} {
		now, _ := time.Parse(time.RFC3339, c.now)
		if got := nextDaily(now, 150).Format(time.RFC3339); got != c.want {
			t.Errorf("after %s: %s, want %s", c.now, got, c.want)
		}
	}
	calls := 0
	s := schedule{interval: time.Hour, at: 150, now: time.Now}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.run(ctx, func(context.Context) { calls++ }); close(done) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the daily loop ignored cancellation")
	}
	if calls != 0 {
		t.Errorf("a daily schedule ran %d times before its time", calls)
	}
}
