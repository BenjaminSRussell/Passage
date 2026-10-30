package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/batch/internal/domain"
	"github.com/BenjaminSRussell/passage/services/batch/internal/jobs"
	"github.com/BenjaminSRussell/passage/services/batch/internal/store"
	"github.com/BenjaminSRussell/passage/services/batch/migrations"
)

func scoped(tenantN int) context.Context {
	return tenant.With(context.Background(), tenant.Principal{
		TenantID: testkit.TenantID(tenantN), LegalEntityID: testkit.TenantID(9), UserID: testkit.TenantID(8),
		Duties: map[string]struct{}{"batch.write": {}},
	})
}

func startProductRun(t *testing.T, s *store.Postgres, ctx context.Context, code, handler string) store.Run {
	t.Helper()
	if _, err := s.SaveJob(ctx, store.Job{Code: code, Handler: handler}); err != nil {
		t.Fatal(err)
	}
	var params json.RawMessage
	if handler == jobs.HandlerProductExplode {
		params = explodeParams()
	}
	run, err := s.StartRun(ctx, code, 0, testkit.TenantID(8), params)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func setRun(t *testing.T, admin *pgxpool.Pool, runID, set string, args ...any) {
	t.Helper()
	if _, err := admin.Exec(context.Background(), "update batch_run set "+set+" where id = $1", append([]any{runID}, args...)...); err != nil {
		t.Fatal(err)
	}
}

func runStatus(t *testing.T, runs *store.Postgres, ctx context.Context, runID string) store.Run {
	t.Helper()
	got, err := runs.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestProductJobsTrackRunProgressInPostgres(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := scoped(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	runs := store.New(app)
	pages := jobs.NewPgPageStore(app)

	run := startProductRun(t, runs, ctx, "product-group-change", jobs.HandlerProductGroupChange)
	ids := releasedIDs(7)
	product := &fakeProduct{released: ids, onHand: map[string]bool{ids[0]: true}, failReleased: map[string]int{ids[5]: 99}}
	runner := jobs.ProductJobs{Client: product, Pages: pages, PageSize: 3, MaxAttempts: 1}

	if _, err := runner.Run(ctx, jobs.HandlerProductGroupChange, run.ID); err == nil {
		t.Fatal("third page must fail")
	}
	got, err := runs.GetRun(ctx, run.ID)
	if err != nil || got.Status != domain.RunError || got.RowsDone != 6 {
		t.Fatalf("after failure %+v, %v", got, err)
	}

	product.failReleased = nil
	product.groupCalls = nil
	summary, err := runner.Run(ctx, jobs.HandlerProductGroupChange, run.ID)
	if err != nil || summary.Pages != 1 || summary.Items != 1 || len(product.groupCalls) != 1 {
		t.Fatalf("resume %+v, %v, calls=%v", summary, err, product.groupCalls)
	}
	got, err = runs.GetRun(ctx, run.ID)
	if err != nil || got.Status != domain.RunEnded || got.RowsDone != 7 {
		t.Fatalf("after resume %+v, %v", got, err)
	}

	product.groupCalls = nil
	if summary, err := runner.Run(ctx, jobs.HandlerProductGroupChange, run.ID); !errors.Is(err, jobs.ErrRunNotActive) || summary.Pages != 0 || len(product.groupCalls) != 0 {
		t.Fatalf("finished run reprocessed: %+v, %v", summary, err)
	}
	if got = runStatus(t, runs, ctx, run.ID); got.Status != domain.RunEnded || got.RowsDone != 7 {
		t.Fatalf("a refused rerun changed the run: %+v", got)
	}
}

func TestProductExplodeJobCheckpointsPagesInPostgres(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := scoped(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	runs := store.New(app)
	run := startProductRun(t, runs, ctx, "product-explode", jobs.HandlerProductExplode)
	setRun(t, admin, run.ID, "params = $2::jsonb", string(explodeParams()))
	product := &fakeProduct{variantsLeft: 1200}
	runner := jobs.ProductJobs{Client: &crashAfter{fakeProduct: product, okCalls: 2}, Pages: jobs.NewPgPageStore(app), MaxAttempts: 1}

	if _, err := runner.Run(ctx, jobs.HandlerProductExplode, run.ID); err == nil {
		t.Fatal("third page must fail")
	}
	got, _ := runs.GetRun(ctx, run.ID)
	if got.RowsDone != 1000 || got.Status != domain.RunError {
		t.Fatalf("after crash %+v", got)
	}
	runner.Client = product
	if summary, err := runner.Run(ctx, jobs.HandlerProductExplode, run.ID); err != nil || summary.Succeeded != 200 {
		t.Fatalf("resume %+v, %v", summary, err)
	}
	if got, _ = runs.GetRun(ctx, run.ID); got.RowsDone != 1200 || got.Status != domain.RunEnded {
		t.Fatalf("after resume %+v", got)
	}
}

func TestPgPageStoreIsTenantScopedAndKeepsCompletedPages(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := scoped(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	run := startProductRun(t, store.New(app), ctx, "scoped", jobs.HandlerProductGroupChange)
	pages := jobs.NewPgPageStore(app)
	claim, err := pages.Claim(ctx, run.ID, jobs.HandlerProductGroupChange)
	if err != nil {
		t.Fatal(err)
	}

	if err := pages.Record(ctx, run.ID, claim.Token, jobs.Page{No: 0, Cursor: "", NextCursor: "x", Items: 3, Succeeded: 3, Status: jobs.PageOK}); err != nil {
		t.Fatal(err)
	}
	if err := pages.Record(ctx, run.ID, claim.Token, jobs.Page{No: 0, NextCursor: "other", Items: 9, Status: jobs.PageFailed, Error: "late retry"}); err != nil {
		t.Fatal(err)
	}
	resume, err := pages.Resume(ctx, run.ID)
	if err != nil || resume.NextPage != 1 || resume.Cursor != "x" || resume.Done {
		t.Fatalf("completed page was overwritten: %+v, %v", resume, err)
	}

	other := scoped(2)
	if foreign, err := pages.Resume(other, run.ID); err != nil || foreign != (jobs.Resume{}) {
		t.Fatalf("tenant 2 saw tenant 1 pages: %+v, %v", foreign, err)
	}
	if err := pages.Record(other, run.ID, claim.Token, jobs.Page{No: 0, Status: jobs.PageOK}); !errors.Is(err, jobs.ErrRunNotFound) {
		t.Fatalf("tenant 2 recording on tenant 1 run = %v", err)
	}
	if err := pages.Finish(other, run.ID, claim.Token, domain.RunEnded, ""); !errors.Is(err, jobs.ErrRunNotFound) {
		t.Fatalf("tenant 2 finishing tenant 1 run = %v", err)
	}
	if got, _ := store.New(app).GetRun(ctx, run.ID); got.Status != domain.RunExecuting {
		t.Fatalf("foreign finish changed the run: %+v", got)
	}
	if _, err := pages.Resume(context.Background(), run.ID); err == nil {
		t.Fatal("a context without a principal must be refused")
	}
}

type gate struct {
	*fakeProduct
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gate) ReleasedPage(ctx context.Context, after string, limit int) (jobs.ReleasedPage, error) {
	g.once.Do(func() { close(g.started) })
	<-g.release
	return g.fakeProduct.ReleasedPage(ctx, after, limit)
}

func TestSecondWorkerOnALeasedRunIsRefusedAndTheFirstFinishesCleanly(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := scoped(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	runs := store.New(app)
	run := startProductRun(t, runs, ctx, "contended", jobs.HandlerProductGroupChange)
	slow := &gate{fakeProduct: &fakeProduct{released: releasedIDs(4)}, started: make(chan struct{}), release: make(chan struct{})}
	pages := jobs.NewPgPageStore(app)

	done := make(chan error, 1)
	go func() {
		_, err := jobs.ProductJobs{Client: slow, Pages: pages, PageSize: 3, MaxAttempts: 1}.Run(ctx, jobs.HandlerProductGroupChange, run.ID)
		done <- err
	}()
	<-slow.started

	rival := &fakeProduct{released: releasedIDs(4)}
	if _, err := (jobs.ProductJobs{Client: rival, Pages: pages, PageSize: 3, MaxAttempts: 1}).Run(ctx, jobs.HandlerProductGroupChange, run.ID); !errors.Is(err, jobs.ErrRunNotActive) {
		t.Fatalf("second worker err = %v", err)
	}
	if len(rival.groupCalls) != 0 || runStatus(t, runs, ctx, run.ID).Status != domain.RunExecuting {
		t.Fatalf("rival touched the run: calls=%v", rival.groupCalls)
	}

	close(slow.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := runStatus(t, runs, ctx, run.ID); got.Status != domain.RunEnded || got.RowsDone != 4 {
		t.Fatalf("first worker run = %+v", got)
	}

	setRun(t, admin, run.ID, "status = 'error', claimed_until = now() - interval '1 minute'")
	if _, err := (jobs.ProductJobs{Client: &fakeProduct{released: releasedIDs(4)}, Pages: pages, PageSize: 3, MaxAttempts: 1}).Run(ctx, jobs.HandlerProductGroupChange, run.ID); err != nil {
		t.Fatalf("an expired lease must be claimable: %v", err)
	}
}

func TestRunsThatAreNotExecutableAreNeverStartedOrOverwritten(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := scoped(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	runs := store.New(app)
	pages := jobs.NewPgPageStore(app)
	for _, status := range []string{domain.RunCanceled, domain.RunCanceling, domain.RunHold, domain.RunEnded, domain.RunWaiting} {
		run := startProductRun(t, runs, ctx, "frozen-"+status, jobs.HandlerProductGroupChange)
		setRun(t, admin, run.ID, "status = $2", status)
		product := &fakeProduct{released: releasedIDs(2)}
		if _, err := (jobs.ProductJobs{Client: product, Pages: pages, MaxAttempts: 1}).Run(ctx, jobs.HandlerProductGroupChange, run.ID); !errors.Is(err, jobs.ErrRunNotActive) {
			t.Fatalf("%s: err = %v", status, err)
		}
		if err := pages.Finish(ctx, run.ID, testkit.TenantID(99), domain.RunEnded, ""); !errors.Is(err, jobs.ErrLostLease) {
			t.Fatalf("%s: finish err = %v", status, err)
		}
		if got := runStatus(t, runs, ctx, run.ID); got.Status != status || len(product.groupCalls) != 0 {
			t.Fatalf("%s: run = %+v calls=%v", status, got, product.groupCalls)
		}
	}

	wrong := startProductRun(t, runs, ctx, "wrong-handler", jobs.HandlerProductExplode)
	if _, err := (jobs.ProductJobs{Client: &fakeProduct{}, Pages: pages, MaxAttempts: 1}).Run(ctx, jobs.HandlerProductGroupChange, wrong.ID); !errors.Is(err, jobs.ErrRunNotActive) {
		t.Fatalf("a run of another job's handler must be refused: %v", err)
	}
}

func TestCancelingARunMidwayStopsTheWorkerWithoutOverwritingTheCancel(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := scoped(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	runs := store.New(app)
	run := startProductRun(t, runs, ctx, "cancel-mid", jobs.HandlerProductGroupChange)
	product := &fakeProduct{released: releasedIDs(9)}
	canceling := cancelAfterFirstGroup{fakeProduct: product, cancel: func() { setRun(t, admin, run.ID, "status = 'canceling'") }}

	_, err := jobs.ProductJobs{Client: canceling, Pages: jobs.NewPgPageStore(app), PageSize: 3, MaxAttempts: 1}.Run(ctx, jobs.HandlerProductGroupChange, run.ID)
	if !errors.Is(err, jobs.ErrRunNotActive) || len(product.groupCalls) != 1 {
		t.Fatalf("err=%v calls=%d", err, len(product.groupCalls))
	}
	if got := runStatus(t, runs, ctx, run.ID); got.Status != domain.RunCanceling {
		t.Fatalf("cancel overwritten: %+v", got)
	}
}

type cancelAfterFirstGroup struct {
	*fakeProduct
	cancel func()
}

func (c cancelAfterFirstGroup) GroupChange(ctx context.Context, ids []string) ([]jobs.ItemResult, error) {
	out, err := c.fakeProduct.GroupChange(ctx, ids)
	c.cancel()
	return out, err
}

func TestStalledWorkerWithALostLeaseCannotWrite(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := scoped(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	runs := store.New(app)
	pages := jobs.NewPgPageStore(app)
	run := startProductRun(t, runs, ctx, "lost-lease", jobs.HandlerProductGroupChange)

	a, err := pages.Claim(ctx, run.ID, jobs.HandlerProductGroupChange)
	if err != nil {
		t.Fatal(err)
	}
	setRun(t, admin, run.ID, "claimed_until = now() - interval '1 minute'")
	if _, err := (jobs.ProductJobs{Client: &fakeProduct{released: releasedIDs(4)}, Pages: pages, PageSize: 3, MaxAttempts: 1}).Run(ctx, jobs.HandlerProductGroupChange, run.ID); err != nil {
		t.Fatalf("worker B: %v", err)
	}
	want := runStatus(t, runs, ctx, run.ID)
	if want.Status != domain.RunEnded || want.RowsDone != 4 {
		t.Fatalf("worker B run = %+v", want)
	}

	if err := pages.Record(ctx, run.ID, a.Token, jobs.Page{No: 9, Items: 50, Succeeded: 50, Status: jobs.PageOK}); !errors.Is(err, jobs.ErrLostLease) {
		t.Fatalf("stalled Record = %v", err)
	}
	if err := pages.Finish(ctx, run.ID, a.Token, domain.RunError, "stalled"); !errors.Is(err, jobs.ErrLostLease) {
		t.Fatalf("stalled Finish = %v", err)
	}
	if got := runStatus(t, runs, ctx, run.ID); got != want {
		t.Fatalf("B's state changed: %+v want %+v", got, want)
	}
	if resume, _ := pages.Resume(ctx, run.ID); resume.NextPage != 2 {
		t.Fatalf("stalled page leaked: %+v", resume)
	}
}
