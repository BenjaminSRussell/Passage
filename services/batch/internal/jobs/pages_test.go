package jobs_test

import (
	"context"
	"errors"
	"testing"

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
	run, err := s.StartRun(ctx, code, 0, testkit.TenantID(8))
	if err != nil {
		t.Fatal(err)
	}
	return run
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

	if _, err := runner.Run(ctx, jobs.HandlerProductGroupChange, run.ID, nil); err == nil {
		t.Fatal("third page must fail")
	}
	got, err := runs.GetRun(ctx, run.ID)
	if err != nil || got.Status != domain.RunError || got.RowsDone != 6 {
		t.Fatalf("after failure %+v, %v", got, err)
	}

	product.failReleased = nil
	product.groupCalls = nil
	summary, err := runner.Run(ctx, jobs.HandlerProductGroupChange, run.ID, nil)
	if err != nil || summary.Pages != 1 || summary.Items != 1 || len(product.groupCalls) != 1 {
		t.Fatalf("resume %+v, %v, calls=%v", summary, err, product.groupCalls)
	}
	got, err = runs.GetRun(ctx, run.ID)
	if err != nil || got.Status != domain.RunEnded || got.RowsDone != 7 {
		t.Fatalf("after resume %+v, %v", got, err)
	}

	product.groupCalls = nil
	if summary, err := runner.Run(ctx, jobs.HandlerProductGroupChange, run.ID, nil); err != nil || summary.Pages != 0 || len(product.groupCalls) != 0 {
		t.Fatalf("finished run reprocessed: %+v, %v", summary, err)
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
	product := &fakeProduct{variantsLeft: 1200}
	runner := jobs.ProductJobs{Client: &crashAfter{fakeProduct: product, okCalls: 2}, Pages: jobs.NewPgPageStore(app), MaxAttempts: 1}

	if _, err := runner.Run(ctx, jobs.HandlerProductExplode, run.ID, explodeParams()); err == nil {
		t.Fatal("third page must fail")
	}
	got, _ := runs.GetRun(ctx, run.ID)
	if got.RowsDone != 1000 || got.Status != domain.RunError {
		t.Fatalf("after crash %+v", got)
	}
	runner.Client = product
	if summary, err := runner.Run(ctx, jobs.HandlerProductExplode, run.ID, explodeParams()); err != nil || summary.Succeeded != 200 {
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

	if err := pages.Record(ctx, run.ID, jobs.Page{No: 0, Cursor: "", NextCursor: "x", Items: 3, Succeeded: 3, Status: jobs.PageOK}); err != nil {
		t.Fatal(err)
	}
	if err := pages.Record(ctx, run.ID, jobs.Page{No: 0, NextCursor: "other", Items: 9, Status: jobs.PageFailed, Error: "late retry"}); err != nil {
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
	if err := pages.Record(other, run.ID, jobs.Page{No: 0, Status: jobs.PageOK}); !errors.Is(err, jobs.ErrRunNotFound) {
		t.Fatalf("tenant 2 recording on tenant 1 run = %v", err)
	}
	if err := pages.Finish(other, run.ID, domain.RunEnded, ""); !errors.Is(err, jobs.ErrRunNotFound) {
		t.Fatalf("tenant 2 finishing tenant 1 run = %v", err)
	}
	if got, _ := store.New(app).GetRun(ctx, run.ID); got.Status != domain.RunExecuting {
		t.Fatalf("foreign finish changed the run: %+v", got)
	}
	if _, err := pages.Resume(context.Background(), run.ID); err == nil {
		t.Fatal("a context without a principal must be refused")
	}
}
