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
	"github.com/BenjaminSRussell/passage/services/finreport/internal/firing"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

const fireSchedulesCommand = "fire-schedules"

// scheduleFirer is what the job runs per tenant; firing.Runner is the real one.
type scheduleFirer interface {
	Run(ctx context.Context) (firing.Result, error)
}

// fireSchedules is `finreport fire-schedules TENANT_ID...` (FR-15.8, PRD §2.15.9
// `statement-schedules`), run by the chart's CronJob every few minutes: for
// each tenant it runs the schedules that came due since the last pass, stores
// the rendered files in the report library and writes distribution.sent.v1 per
// file (statement.failed.v1 for a firing that failed twice).
func fireSchedules(ctx context.Context, getenv func(string) string, args []string) error {
	tenants, err := tenantArgs(fireSchedulesCommand, args)
	if err != nil {
		return err
	}
	cfg, err := config.FromEnv(getenv)
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
	runner := firing.Runner{Store: store.New(pool), Now: time.Now, MaxLagSeconds: cfg.MaxLagSeconds, RetentionYears: cfg.RetentionClosedYears}
	return fireTenants(ctx, runner, tenants)
}

// fireTenants fires every tenant even when one fails, and fails the job when
// any did, so a broken pass shows as a failed CronJob run. A firing that
// failed is not a failed job: it is recorded and reported by its own event.
func fireTenants(ctx context.Context, f scheduleFirer, tenants []string) error {
	failed := 0
	for _, tid := range tenants {
		tctx := tenant.With(ctx, tenant.Principal{TenantID: tid, UserID: firing.Actor, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
		res, err := f.Run(tctx)
		if err != nil {
			slog.ErrorContext(ctx, "schedule firing failed", "tenantId", tid, "err", err)
			failed++
			continue
		}
		slog.InfoContext(ctx, "schedules fired", "tenantId", tid, "due", res.Due, "completed", res.Completed, "failed", res.Failed)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d tenant schedule passes failed", failed, len(tenants))
	}
	return nil
}
