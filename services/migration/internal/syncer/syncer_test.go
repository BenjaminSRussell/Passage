package syncer_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/internal/syncer"
	"github.com/BenjaminSRussell/passage/services/migration/internal/testdb"
	"github.com/BenjaminSRussell/passage/services/migration/migrations"
)

type fakeRunner struct {
	calls atomic.Int64
	hold  chan struct{}
	res   func(st store.SyncState) (syncer.Result, error)
	seen  []store.SyncState
	mu    sync.Mutex
}

func (f *fakeRunner) Run(_ context.Context, st store.SyncState, runID string) (syncer.Result, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.seen = append(f.seen, st)
	f.mu.Unlock()
	if f.hold != nil {
		<-f.hold
	}
	if f.res != nil {
		return f.res(st)
	}
	return syncer.Result{RunID: runID, Report: domain.Report{Status: domain.StatusSucceeded, Extracted: 1, Created: 1, Watermark: "W1"}}, nil
}

func setup(t *testing.T, s store.Store, ctx context.Context) (binding store.EntityBinding, project store.Project) {
	t.Helper()
	profile, _, err := s.SaveConnectionProfile(ctx, store.ConnectionProfile{Name: "Acme", Kind: domain.ConnSQL, SourceSystem: "custom_acme", Status: domain.ConnActive,
		Config: map[string]any{"driver": "postgres", "host": "erp.acme.example", "port": float64(5432), "database": "erp", "user": "u"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	binding, _, err = s.SaveEntityBinding(ctx, store.EntityBinding{ConnectionProfileID: profile.ID, Entity: "customer", Status: domain.BindingActive, KeyFields: []string{"id"},
		Source: map[string]any{"table": "CustTable"}, CursorField: "upd", CursorType: domain.CursorTimestamp}, "")
	if err != nil {
		t.Fatal(err)
	}
	project, _, err = s.SaveProject(ctx, store.Project{Name: "sync", Type: domain.ProjectMigrate, SourceSystem: "custom_acme", Status: domain.ProjectActive,
		ConnectionProfileID: profile.ID, ScheduleCron: "*/15 * * * *"}, "")
	if err != nil {
		t.Fatal(err)
	}
	return binding, project
}

func inTenant(n int) context.Context {
	return syncer.SystemContext(context.Background(), testkit.TenantID(n))
}

func TestScheduler_TwoLoopsOnOneDueBindingStartOneRun(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	testdb.Fresh(t, admin)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	binding, _ := setup(t, s, inTenant(1))
	runner := &fakeRunner{hold: make(chan struct{})}
	a := &syncer.Scheduler{Store: s, Runner: runner}
	b := &syncer.Scheduler{Store: s, Runner: runner}
	var wg sync.WaitGroup
	for _, sc := range []*syncer.Scheduler{a, b} {
		wg.Add(1)
		go func() { defer wg.Done(); sc.Tick(context.Background()) }()
	}
	// Both loops see the candidate; only the one that wins the claim runs. Release it once the other has given up.
	deadline := time.Now().Add(5 * time.Second)
	for runner.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	close(runner.hold)
	wg.Wait()
	if runner.calls.Load() != 1 {
		t.Fatalf("runner called %d times, want 1", runner.calls.Load())
	}
	st, err := s.GetSync(inTenant(1), binding.ID)
	if err != nil || st.Watermark != "W1" || st.LastStatus != "succeeded" || st.Running {
		t.Fatalf("state = %+v %v", st, err)
	}
	// The next tick finds nothing due; the first run saw no watermark.
	a.Tick(context.Background())
	if runner.calls.Load() != 1 || runner.seen[0].Watermark != "" {
		t.Fatalf("calls=%d first watermark=%q", runner.calls.Load(), runner.seen[0].Watermark)
	}
}

func TestScheduler_NeverCrossesTenants(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	testdb.Fresh(t, admin)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	b1, _ := setup(t, s, inTenant(1))
	b2, _ := setup(t, s, inTenant(2))
	runner := &fakeRunner{}
	(&syncer.Scheduler{Store: s, Runner: runner}).Tick(context.Background())
	if runner.calls.Load() != 2 {
		t.Fatalf("calls = %d, want one per tenant", runner.calls.Load())
	}
	for n, b := range map[int]store.EntityBinding{1: b1, 2: b2} {
		if st, err := s.GetSync(inTenant(n), b.ID); err != nil || st.LastStatus != "succeeded" {
			t.Fatalf("tenant %d: %+v %v", n, st, err)
		}
	}
	if _, err := s.GetSync(inTenant(1), b2.ID); err == nil {
		t.Fatal("tenant 1 can see tenant 2's sync")
	}
}

func TestScheduler_FailedRunKeepsTheWatermarkAndBacksOff(t *testing.T) {
	m := store.NewMem()
	ctx := inTenant(1)
	binding, _ := setup(t, m, ctx)
	runner := &fakeRunner{}
	sc := &syncer.Scheduler{Store: m, Runner: runner}
	sc.Tick(ctx)
	if st, _ := m.GetSync(ctx, binding.ID); st.Watermark != "W1" {
		t.Fatalf("first run: %+v", st)
	}
	runner.res = func(store.SyncState) (syncer.Result, error) {
		return syncer.Result{}, errors.New("EXTRACT-FAILED: connection refused")
	}
	for i := 1; i <= 4; i++ {
		edit := store.SyncEdit{RunNow: true}
		cur, _ := m.GetSync(ctx, binding.ID)
		if _, err := m.UpdateSync(ctx, binding.ID, cur.Version, edit); err != nil {
			t.Fatal(err)
		}
		sc.Tick(ctx)
		st, _ := m.GetSync(ctx, binding.ID)
		wantDelay := domain.SyncBackoff(i)
		if st.Watermark != "W1" || st.ConsecutiveFailures != i || st.LastStatus != "failed" || st.NextDueAt == nil || st.NextDueAt.Before(time.Now().Add(wantDelay-time.Minute)) {
			t.Fatalf("failure %d: %+v (want next due >= %s ahead)", i, st, wantDelay)
		}
	}
	// A run reporting rows a target did not take is a failure too, whatever its own status says.
	runner.res = func(store.SyncState) (syncer.Result, error) {
		return syncer.Result{RunID: "r", Report: domain.Report{Status: domain.StatusPartial, Watermark: "W9", Transient: 2}}, nil
	}
	cur, _ := m.GetSync(ctx, binding.ID)
	_, _ = m.UpdateSync(ctx, binding.ID, cur.Version, store.SyncEdit{RunNow: true})
	sc.Tick(ctx)
	if st, _ := m.GetSync(ctx, binding.ID); st.Watermark != "W1" || st.ConsecutiveFailures != 5 {
		t.Fatalf("transient rejects must hold the watermark: %+v", st)
	}
	// Recovery resets the count and moves the watermark.
	runner.res = nil
	cur, _ = m.GetSync(ctx, binding.ID)
	_, _ = m.UpdateSync(ctx, binding.ID, cur.Version, store.SyncEdit{RunNow: true})
	sc.Tick(ctx)
	if st, _ := m.GetSync(ctx, binding.ID); st.ConsecutiveFailures != 0 || st.LastStatus != "succeeded" {
		t.Fatalf("recovery: %+v", st)
	}
}

func TestScheduler_DryRunsAndEmptyWatermarksDoNotMoveTheWatermark(t *testing.T) {
	m := store.NewMem()
	ctx := inTenant(1)
	binding, _ := setup(t, m, ctx)
	runner := &fakeRunner{res: func(store.SyncState) (syncer.Result, error) {
		return syncer.Result{RunID: "r", Report: domain.Report{Status: domain.StatusSucceeded, Watermark: ""}}, nil
	}}
	(&syncer.Scheduler{Store: m, Runner: runner}).Tick(ctx)
	if st, _ := m.GetSync(ctx, binding.ID); st.Watermark != "" || st.LastStatus != "succeeded" {
		t.Fatalf("a binding without a cursor keeps an empty watermark: %+v", st)
	}
}

func TestScheduler_ChangedScheduleMovesTheNextDueTime(t *testing.T) {
	m := store.NewMem()
	ctx := inTenant(1)
	binding, project := setup(t, m, ctx)
	fixed := time.Now()
	runner := &fakeRunner{}
	sc := &syncer.Scheduler{Store: m, Runner: runner, Now: func() time.Time { return fixed }}
	sc.Tick(ctx)
	first, _ := domain.CronNext("*/15 * * * *", fixed)
	if st, _ := m.GetSync(ctx, binding.ID); st.NextDueAt == nil || !st.NextDueAt.Equal(first) {
		t.Fatalf("next due = %+v, want %s", st.NextDueAt, first)
	}
	// Moving the project's schedule re-seeds the binding to the new cron's next slot.
	want, _ := domain.CronNext("30 * * * *", fixed)
	if _, err := m.UpdateProject(ctx, project.ID, project.Version, func(p *store.Project) error { p.ScheduleCron = "30 * * * *"; return nil }); err != nil {
		t.Fatal(err)
	}
	sc.Tick(ctx)
	if st, _ := m.GetSync(ctx, binding.ID); st.ScheduleCron != "30 * * * *" || st.NextDueAt == nil || !st.NextDueAt.Equal(want) {
		t.Fatalf("after the schedule changed: %+v", st)
	}
}

// A long run holds one slot, not the loop: a binding that becomes due while it runs is started by a later tick.
func TestScheduler_ALongRunDoesNotHoldBackOtherBindings(t *testing.T) {
	m := store.NewMem()
	ctx := inTenant(1)
	slow, _ := setup(t, m, ctx)
	release := make(chan struct{})
	var started sync.Map
	runner := &fakeRunner{res: func(st store.SyncState) (syncer.Result, error) {
		started.Store(st.BindingID, true)
		if st.BindingID == slow.ID {
			<-release
		}
		return syncer.Result{RunID: ids.New(), Report: domain.Report{Status: domain.StatusSucceeded, Watermark: "W"}}, nil
	}}
	sc := &syncer.Scheduler{Store: m, Runner: runner, Interval: 10 * time.Millisecond, Concurrency: 2}
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sc.Run(runCtx); close(done) }()
	t.Cleanup(func() { stop(); <-done; close(release); sc.Wait() })
	waitFor(t, func() bool { _, ok := started.Load(slow.ID); return ok })

	profile, _, err := m.SaveConnectionProfile(ctx, store.ConnectionProfile{Name: "Beta", Kind: domain.ConnSQL, SourceSystem: "custom_beta", Status: domain.ConnActive,
		Config: map[string]any{"driver": "postgres", "host": "erp.beta.example", "port": float64(5432), "database": "erp", "user": "u"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	fast, _, err := m.SaveEntityBinding(ctx, store.EntityBinding{ConnectionProfileID: profile.ID, Entity: "vendor", Status: domain.BindingActive, KeyFields: []string{"id"},
		Source: map[string]any{"table": "VendTable"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.SaveProject(ctx, store.Project{Name: "beta sync", Type: domain.ProjectMigrate, SourceSystem: "custom_beta", Status: domain.ProjectActive,
		ConnectionProfileID: profile.ID, ScheduleCron: "*/15 * * * *"}, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, ok := started.Load(fast.ID); return ok })
	if st, _ := m.GetSync(ctx, slow.ID); !st.Running {
		t.Fatalf("the slow run should still hold its claim: %+v", st)
	}
	if n := runner.calls.Load(); n != 2 {
		t.Fatalf("runner called %d times, want one per binding (a running binding is not started twice)", n)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A key check whose full read cannot finish (the row cap, the run deadline) must not turn every later run into the
// same failing full read: the next runs are deltas again and the check is retried one period later.
func TestScheduler_AFailingKeyCheckFallsBackToDeltas(t *testing.T) {
	m := store.NewMem()
	ctx := inTenant(1)
	binding, _ := setup(t, m, ctx)
	runner := &fakeRunner{res: func(st store.SyncState) (syncer.Result, error) {
		if st.KeyCheckDue {
			return syncer.Result{RunID: "r", FullRead: true}, errors.New("customer has more than 2000000 rows to extract")
		}
		return syncer.Result{RunID: "r", Report: domain.Report{Status: domain.StatusSucceeded, Extracted: 1, Created: 1, Watermark: "W2"}}, nil
	}}
	sc := &syncer.Scheduler{Store: m, Runner: runner}
	sc.Tick(ctx) // never checked: the first run is the full read, and it fails
	st, _ := m.GetSync(ctx, binding.ID)
	if !runner.seen[0].KeyCheckDue || st.LastStatus != "failed" || st.KeyCheckDue || st.KeyCheckError == "" || st.LastKeyCheckAt != nil {
		t.Fatalf("after the failed full read: %+v", st)
	}
	cur, _ := m.GetSync(ctx, binding.ID)
	if _, err := m.UpdateSync(ctx, binding.ID, cur.Version, store.SyncEdit{RunNow: true}); err != nil {
		t.Fatal(err)
	}
	sc.Tick(ctx)
	if st, _ := m.GetSync(ctx, binding.ID); runner.seen[1].KeyCheckDue || st.LastStatus != "succeeded" || st.Watermark != "W2" || st.ConsecutiveFailures != 0 {
		t.Fatalf("the run after a failed key check must be a delta: due=%v %+v", runner.seen[1].KeyCheckDue, st)
	}
}
