package jobs_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/batch/internal/domain"
	"github.com/BenjaminSRussell/passage/services/batch/internal/jobs"
	"github.com/BenjaminSRussell/passage/services/batch/internal/store"
	"github.com/BenjaminSRussell/passage/services/batch/migrations"
)

var dispatchHandlers = []string{jobs.HandlerProductExplode, jobs.HandlerProductGroupChange}

func runnableIDs(t *testing.T, src *jobs.PgRunSource) []string {
	t.Helper()
	found, err := src.Runnable(context.Background(), dispatchHandlers, 5, time.Minute, 100)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(found))
	for i, r := range found {
		out[i] = r.RunID
	}
	sort.Strings(out)
	return out
}

func TestRunnableFindsOnlyExecutableUnclaimedRunsAcrossTenants(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	runs := store.New(app)
	src := jobs.NewPgRunSource(app)

	fresh := startProductRun(t, runs, scoped(1), "fresh", jobs.HandlerProductGroupChange)
	otherTenant := startProductRun(t, runs, scoped(2), "fresh", jobs.HandlerProductGroupChange)
	explode := startProductRun(t, runs, scoped(1), "explode", jobs.HandlerProductExplode)
	crashed := startProductRun(t, runs, scoped(1), "crashed", jobs.HandlerProductGroupChange)
	setRun(t, admin, crashed.ID, "claimed_until = now() - interval '1 minute', attempts = 2")
	retryable := startProductRun(t, runs, scoped(1), "retryable", jobs.HandlerProductGroupChange)
	setRun(t, admin, retryable.ID, "status = 'error', ended_at = now() - interval '2 minutes', attempts = 1")

	leased := startProductRun(t, runs, scoped(1), "leased", jobs.HandlerProductGroupChange)
	setRun(t, admin, leased.ID, "claimed_until = now() + interval '5 minutes'")
	cooling := startProductRun(t, runs, scoped(1), "cooling", jobs.HandlerProductGroupChange)
	setRun(t, admin, cooling.ID, "status = 'error', ended_at = now()")
	exhausted := startProductRun(t, runs, scoped(1), "exhausted", jobs.HandlerProductGroupChange)
	setRun(t, admin, exhausted.ID, "status = 'error', ended_at = now() - interval '1 hour', attempts = 5")
	crashLooping := startProductRun(t, runs, scoped(1), "crash-looping", jobs.HandlerProductGroupChange)
	setRun(t, admin, crashLooping.ID, "attempts = 5")
	legacy := startProductRun(t, runs, scoped(1), "legacy", jobs.HandlerProductGroupChange)
	setRun(t, admin, legacy.ID, "legal_entity_id = null")
	for _, status := range []string{domain.RunCanceled, domain.RunCanceling, domain.RunHold, domain.RunEnded, domain.RunWaiting, domain.RunScheduled} {
		frozen := startProductRun(t, runs, scoped(1), "frozen-"+status, jobs.HandlerProductGroupChange)
		setRun(t, admin, frozen.ID, "status = $2", status)
	}
	if _, err := runs.SaveJob(scoped(1), store.Job{Code: "synthetic", Handler: "synthetic.rows"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.StartRun(scoped(1), "synthetic", store.RunInput{Rows: 10, TriggeredBy: testkit.TenantID(8)}); err != nil {
		t.Fatal(err)
	}

	want := []string{fresh.ID, otherTenant.ID, explode.ID, crashed.ID, retryable.ID}
	sort.Strings(want)
	got := runnableIDs(t, src)
	if len(got) != len(want) {
		t.Fatalf("runnable = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("runnable = %v, want %v", got, want)
		}
	}

	found, err := src.Runnable(context.Background(), dispatchHandlers, 5, time.Minute, 2)
	if err != nil || len(found) != 2 {
		t.Fatalf("limit not applied: %d, %v", len(found), err)
	}
	for _, r := range found {
		if r.RunID == fresh.ID && (!reflect.DeepEqual(r.Duties, []string{"product.released.read", "product.jobs.write"}) || r.TenantID != testkit.TenantID(1) || r.LegalEntityID != testkit.TenantID(9) || r.UserID != testkit.TenantID(8) || r.Handler != jobs.HandlerProductGroupChange) {
			t.Fatalf("runnable row = %+v", r)
		}
	}
	if only, _ := src.Runnable(context.Background(), []string{jobs.HandlerProductExplode}, 5, time.Minute, 100); len(only) != 1 || only[0].RunID != explode.ID {
		t.Fatalf("handler filter = %+v", only)
	}
}

type contextRecorder struct {
	inner jobs.RunExecutor
	mu    sync.Mutex
	seen  map[string]tenant.Principal
}

func (c *contextRecorder) Run(ctx context.Context, handler, runID string) (jobs.Summary, error) {
	p, _ := tenant.FromContext(ctx)
	c.mu.Lock()
	c.seen[runID] = p
	c.mu.Unlock()
	return c.inner.Run(ctx, handler, runID)
}

func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDispatcherRunsEveryRunnableRunAsTheCreatorsServicePrincipal(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	runs := store.New(app)
	group := startProductRun(t, runs, scoped(1), "group", jobs.HandlerProductGroupChange)
	explode := startProductRun(t, runs, scoped(2), "explode", jobs.HandlerProductExplode)
	product := &syncProduct{fakeProduct: &fakeProduct{released: releasedIDs(4), variantsLeft: 700}}
	recorder := &contextRecorder{
		inner: jobs.ProductJobs{Client: product, Pages: jobs.NewPgPageStore(app), PageSize: 3, MaxAttempts: 1},
		seen:  map[string]tenant.Principal{},
	}
	d := &jobs.Dispatcher{Source: jobs.NewPgRunSource(app), Executor: recorder, Interval: 10 * time.Millisecond, Concurrency: 2}

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { d.Run(ctx); close(stopped) }()
	waitFor(t, "both runs to end", func() bool {
		return runStatus(t, runs, scoped(1), group.ID).Status == domain.RunEnded && runStatus(t, runs, scoped(2), explode.ID).Status == domain.RunEnded
	})
	cancel()
	<-stopped

	if got := recorder.seen[explode.ID]; len(got.Duties) != 1 || !got.HasDuty("product.variants.explode") {
		t.Fatalf("explode run principal holds %v; want only its stored duty", got.Duties)
	}
	if got := recorder.seen[group.ID]; got.TenantID != testkit.TenantID(1) || got.LegalEntityID != testkit.TenantID(9) || got.UserID != testkit.TenantID(8) {
		t.Fatalf("group principal = %+v", got)
	}
	if got := recorder.seen[explode.ID]; got.TenantID != testkit.TenantID(2) {
		t.Fatalf("explode principal = %+v", got)
	}
	if got := runStatus(t, runs, scoped(1), group.ID); got.RowsDone != 4 {
		t.Fatalf("group run = %+v", got)
	}
	if got := runStatus(t, runs, scoped(2), explode.ID); got.RowsDone != 700 {
		t.Fatalf("explode run = %+v", got)
	}
}

type syncProduct struct {
	*fakeProduct
	mu    sync.Mutex
	delay time.Duration
}

func (s *syncProduct) ReleasedPage(ctx context.Context, after string, limit int) (jobs.ReleasedPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fakeProduct.ReleasedPage(ctx, after, limit)
}

func (s *syncProduct) GroupChange(ctx context.Context, ids []string) ([]jobs.ItemResult, error) {
	time.Sleep(s.delay)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fakeProduct.GroupChange(ctx, ids)
}

func (s *syncProduct) ExplodePage(ctx context.Context, master string, selected map[string][]string, limit int) (jobs.ExplodePage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fakeProduct.ExplodePage(ctx, master, selected, limit)
}

type perRunProduct struct {
	mu    sync.Mutex
	delay time.Duration
	calls map[string]int
}

func (p *perRunProduct) executor(pages jobs.PageStore) jobs.RunExecutor {
	return executorFunc(func(ctx context.Context, handler, runID string) (jobs.Summary, error) {
		fake := &syncProduct{fakeProduct: &fakeProduct{released: releasedIDs(4)}, delay: p.delay}
		summary, err := jobs.ProductJobs{Client: fake, Pages: pages, PageSize: 3, MaxAttempts: 1}.Run(ctx, handler, runID)
		p.mu.Lock()
		p.calls[runID] += len(fake.groupCalls)
		p.mu.Unlock()
		return summary, err
	})
}

type executorFunc func(ctx context.Context, handler, runID string) (jobs.Summary, error)

func (f executorFunc) Run(ctx context.Context, handler, runID string) (jobs.Summary, error) {
	return f(ctx, handler, runID)
}

func TestTwoDispatchersNeverRunTheSameRunTwice(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	runs := store.New(app)
	const total = 10
	var ids []string
	for i := range total {
		ids = append(ids, startProductRun(t, runs, scoped(1), "shared-"+string(rune('a'+i)), jobs.HandlerProductGroupChange).ID)
	}
	shared := &perRunProduct{delay: 40 * time.Millisecond, calls: map[string]int{}}
	pages := jobs.NewPgPageStore(app)
	barrier := &sync.WaitGroup{}
	barrier.Add(2)
	replicas := []*jobs.Dispatcher{
		{Source: &barrierSource{PgRunSource: jobs.NewPgRunSource(app), barrier: barrier}, Executor: shared.executor(pages), Interval: 5 * time.Millisecond, Concurrency: 4},
		{Source: &barrierSource{PgRunSource: jobs.NewPgRunSource(app), barrier: barrier}, Executor: shared.executor(pages), Interval: 5 * time.Millisecond, Concurrency: 4},
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, d := range replicas {
		wg.Add(1)
		go func() { defer wg.Done(); d.Run(ctx) }()
	}
	waitFor(t, "all runs to end", func() bool {
		for _, id := range ids {
			if runStatus(t, runs, scoped(1), id).Status != domain.RunEnded {
				return false
			}
		}
		return true
	})
	cancel()
	wg.Wait()

	if refused := replicas[0].Refused() + replicas[1].Refused(); refused == 0 {
		t.Error("both replicas saw the same runs at once, so one of them must have been refused a claim")
	}
	shared.mu.Lock()
	defer shared.mu.Unlock()
	for _, id := range ids {
		if got := runStatus(t, runs, scoped(1), id); got.RowsDone != 4 {
			t.Errorf("run %s rows_done = %d", id, got.RowsDone)
		}
		if shared.calls[id] != 2 {
			t.Errorf("run %s made %d group-change calls, want 2 (one worker, two pages)", id, shared.calls[id])
		}
	}
}

type barrierSource struct {
	*jobs.PgRunSource
	barrier *sync.WaitGroup
	once    sync.Once
}

func (b *barrierSource) Runnable(ctx context.Context, handlers []string, maxAttempts int, retryAfter time.Duration, limit int) ([]jobs.Runnable, error) {
	found, err := b.PgRunSource.Runnable(ctx, handlers, maxAttempts, retryAfter, limit)
	b.once.Do(func() { b.barrier.Done(); b.barrier.Wait() })
	return found, err
}

type blockUntilCanceled struct {
	*fakeProduct
	started chan struct{}
	once    *sync.Once
}

func (b blockUntilCanceled) ReleasedPage(ctx context.Context, _ string, _ int) (jobs.ReleasedPage, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return jobs.ReleasedPage{}, ctx.Err()
}

func TestDispatcherShutdownWaitsForTheRunToBeFinalized(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	runs := store.New(app)
	run := startProductRun(t, runs, scoped(1), "shutdown", jobs.HandlerProductGroupChange)
	started := make(chan struct{})
	blocking := blockUntilCanceled{fakeProduct: &fakeProduct{}, started: started, once: &sync.Once{}}
	d := &jobs.Dispatcher{
		Source:   jobs.NewPgRunSource(app),
		Executor: jobs.ProductJobs{Client: blocking, Pages: jobs.NewPgPageStore(app), MaxAttempts: 1},
		Interval: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { d.Run(ctx); close(stopped) }()
	<-started
	cancel()
	select {
	case <-stopped:
	case <-time.After(20 * time.Second):
		t.Fatal("dispatcher did not stop")
	}
	var status, errMsg string
	if err := admin.QueryRow(context.Background(), `select status, coalesce(error, '') from batch_run where id = $1`, run.ID).Scan(&status, &errMsg); err != nil {
		t.Fatal(err)
	}
	if status != domain.RunError || errMsg == "" {
		t.Fatalf("run left as %q (%q) after shutdown; it must be finalized for retry", status, errMsg)
	}
	var claimed *time.Time
	if err := admin.QueryRow(context.Background(), `select claimed_until from batch_run where id = $1`, run.ID).Scan(&claimed); err != nil || claimed != nil {
		t.Fatalf("lease not released: %v, %v", claimed, err)
	}
}

func TestConcurrentClaimsAdmitExactlyOneWorker(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	run := startProductRun(t, store.New(app), scoped(1), "race", jobs.HandlerProductGroupChange)
	pages := jobs.NewPgPageStore(app)
	const workers = 12
	start := make(chan struct{})
	claims := make(chan jobs.Claim, workers)
	failures := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claim, err := pages.Claim(scoped(1), run.ID, jobs.HandlerProductGroupChange)
			if err != nil {
				failures <- err
				return
			}
			claims <- claim
		}()
	}
	close(start)
	wg.Wait()
	close(claims)
	close(failures)

	var winners []jobs.Claim
	for c := range claims {
		winners = append(winners, c)
	}
	if len(winners) != 1 || winners[0].Token == "" {
		t.Fatalf("%d workers claimed the run, want exactly 1", len(winners))
	}
	for err := range failures {
		if !errors.Is(err, jobs.ErrRunNotActive) {
			t.Errorf("loser err = %v", err)
		}
	}
	var attempts int
	if err := admin.QueryRow(context.Background(), `select attempts from batch_run where id = $1`, run.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("attempts = %d, %v; only the winning claim counts", attempts, err)
	}
}

func TestExhaustedRunsBecomeFailedAndTheScanSeesPastRowSecurity(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	runs := store.New(app)
	src := jobs.NewPgRunSource(app)
	if ok, err := src.CanScan(context.Background()); err != nil || !ok {
		t.Fatalf("CanScan = %v, %v", ok, err)
	}
	stuck := startProductRun(t, runs, scoped(1), "stuck", jobs.HandlerProductGroupChange)
	setRun(t, admin, stuck.ID, "attempts = 5, status = 'error', ended_at = now()")
	busy := startProductRun(t, runs, scoped(1), "busy", jobs.HandlerProductGroupChange)
	setRun(t, admin, busy.ID, "attempts = 5, claimed_until = now() + interval '5 minutes'")
	fine := startProductRun(t, runs, scoped(1), "fine", jobs.HandlerProductGroupChange)

	failed, err := src.FailExhausted(context.Background(), dispatchHandlers, 5)
	if err != nil || len(failed) != 1 || failed[0].RunID != stuck.ID {
		t.Fatalf("failed = %+v, %v", failed, err)
	}
	if got := runStatus(t, runs, scoped(1), stuck.ID); got.Status != domain.RunFailed {
		t.Fatalf("stuck run = %+v", got)
	}
	if got := runStatus(t, runs, scoped(1), busy.ID); got.Status != domain.RunExecuting {
		t.Fatalf("a run another worker holds must not be failed: %+v", got)
	}
	if ids := runnableIDs(t, src); len(ids) != 1 || ids[0] != fine.ID {
		t.Fatalf("runnable after failing = %v", ids)
	}

	d := &jobs.Dispatcher{Source: src, Executor: executorFunc(func(context.Context, string, string) (jobs.Summary, error) { return jobs.Summary{}, nil }), Interval: time.Hour, MaxAttempts: 5}
	setRun(t, admin, fine.ID, "attempts = 5")
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { d.Run(ctx); close(stopped) }()
	waitFor(t, "the dispatcher to fail the exhausted run", func() bool {
		return runStatus(t, runs, scoped(1), fine.ID).Status == domain.RunFailed
	})
	cancel()
	<-stopped
}
