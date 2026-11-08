package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/config"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

const sweepLibraryCommand = "sweep-library"

// librarySweeper is what the job runs per tenant; *store.Postgres is the real one.
type librarySweeper interface {
	SweepLibrary(ctx context.Context, now time.Time) ([]store.Sweep, error)
	SweepRuns(ctx context.Context, now time.Time, retentionDays int) ([]store.RunSweep, error)
}

// sweepLibrary is `finreport sweep-library TENANT_ID...` (FR-15.8, PRD §2.15.9
// `library-retention-sweep`), run by the chart's CronJob weekly: for each tenant
// it deletes the report library files past their retention date, entity by
// entity, except for entities under legal hold (the pass records what the hold
// kept). The same pass deletes the ad-hoc statement runs older than
// REPORTING_RETENTION_ADHOC_DAYS (default 90), with the same hold rule.
// In-process system actor, like lag-check and fire-schedules: no route,
// no service call, no authz seed.
func sweepLibrary(ctx context.Context, getenv func(string) string, args []string) error {
	tenants, err := tenantArgs(sweepLibraryCommand, args)
	if err != nil {
		return err
	}
	url := getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}
	pool, err := dbx.Connect(ctx, url)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	cfg, err := config.FromEnv(getenv)
	if err != nil {
		return err
	}
	return sweepTenants(ctx, store.New(pool), time.Now, cfg.RetentionAdhocDays, tenants)
}

// sweepTenants sweeps every tenant even when one fails, and fails the job when
// any did, so a broken pass shows as a failed CronJob run.
func sweepTenants(ctx context.Context, s librarySweeper, now func() time.Time, adhocDays int, tenants []string) error {
	failed := 0
	for _, tid := range tenants {
		tctx := tenant.With(ctx, tenant.Principal{TenantID: tid, UserID: store.SweepActor, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
		passes, err := s.SweepLibrary(tctx, now())
		if err != nil {
			slog.ErrorContext(ctx, "library sweep failed", "tenantId", tid, "err", err)
			failed++
			continue
		}
		deleted, held := 0, 0
		for _, p := range passes {
			deleted += p.DeletedCount
			held += p.HeldCount
		}
		slog.InfoContext(ctx, "library swept", "tenantId", tid, "entities", len(passes), "deleted", deleted, "heldByLegalHold", held)
		runPasses, err := s.SweepRuns(tctx, now(), adhocDays)
		if err != nil {
			slog.ErrorContext(ctx, "ad-hoc run sweep failed", "tenantId", tid, "err", err)
			failed++
			continue
		}
		runs, heldRuns := 0, 0
		for _, p := range runPasses {
			runs += p.DeletedRuns
			heldRuns += p.HeldRuns
		}
		slog.InfoContext(ctx, "ad-hoc runs swept", "tenantId", tid, "entities", len(runPasses), "deletedRuns", runs, "heldByLegalHold", heldRuns, "retentionDays", adhocDays)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d tenant library sweeps failed", failed, len(tenants))
	}
	return nil
}
