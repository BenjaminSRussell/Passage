// Package jobs runs the print service's scheduled work. The daily job-log purge keeps
// the 30-day job log of FR-32.13 (§6.32.19) without an operator calling
// POST /print/v1/jobs:purge. It runs once per tenant listed in PRINT_PURGE_TENANT_IDS,
// because row-level security scopes every delete to one tenant.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

// PurgeHourUTC is when the daily purge fires (01:00 UTC, the daily slot of §6.32.16).
const PurgeHourUTC = 1

// Purger deletes the finished job log rows older than cutoff for the tenant in ctx.
// store.Store satisfies it.
type Purger interface {
	PurgeJobs(ctx context.Context, cutoff time.Time) (int64, error)
}

// NextRun returns the first instant strictly after now at which the job fires at
// hour:00 UTC. A call exactly at the hour schedules the following day.
func NextRun(now time.Time, hour int) time.Time {
	u := now.UTC()
	next := time.Date(u.Year(), u.Month(), u.Day(), hour, 0, 0, 0, time.UTC)
	if !next.After(u) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

// PurgeTenants removes job log rows older than retentionDays for each tenant and returns
// the total removed. A failing tenant does not stop the others; its error is returned
// with the rest, and the next run retries it (the purge is idempotent).
func PurgeTenants(ctx context.Context, p Purger, tenants []string, now time.Time, retentionDays int) (int64, error) {
	cutoff := domain.RetentionCutoff(now, retentionDays)
	var total int64
	var errs []error
	for _, id := range tenants {
		n, err := p.PurgeJobs(tenant.With(ctx, tenant.Principal{TenantID: id, UserID: "system:print-purge"}), cutoff)
		if err != nil {
			errs = append(errs, fmt.Errorf("purge tenant %s: %w", id, err))
			continue
		}
		total += n
	}
	return total, errors.Join(errs...)
}

// RunDaily runs PurgeTenants at every PurgeHourUTC until ctx is cancelled. onRun, when
// set, receives each run's outcome for logging and metrics.
func RunDaily(ctx context.Context, p Purger, tenants []string, retentionDays int, onRun func(deleted int64, err error)) {
	for {
		timer := time.NewTimer(time.Until(NextRun(time.Now(), PurgeHourUTC)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		n, err := PurgeTenants(ctx, p, tenants, time.Now(), retentionDays)
		if onRun != nil {
			onRun(n, err)
		}
	}
}
