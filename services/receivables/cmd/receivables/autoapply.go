package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

const (
	autoApplyActor           = "system:receivables-auto-apply"
	defaultAutoApplyInterval = 15 * time.Minute
)

// autoApplier is the store side the scheduled auto-apply drives.
type autoApplier interface {
	AutoApplyLegalEntities(ctx context.Context) ([]string, error)
	AutoApplyDue(ctx context.Context, limit int) (store.AutoApplyResult, error)
}

// autoApplyJob runs the matcher's AUTO_APPLY decision on the unapplied payments of each configured tenant on a schedule (30-4),
// so payments with no advice and items opened after a payment came in are settled without a clerk. It is off until
// RECEIVABLES_AUTO_APPLY_TENANT_IDS names tenants; RECEIVABLES_AUTO_APPLY_INTERVAL (default 15m) or RECEIVABLES_AUTO_APPLY_AT
// ("HH:MM" UTC) sets when. Several replicas may run it: each application is keyed by its payment, so none is applied twice.
type autoApplyJob struct {
	sched   schedule
	tenants []string
	store   autoApplier
}

func newAutoApplyJob(getenv func(string) string, s autoApplier) (*autoApplyJob, error) {
	tenants, err := parseTenants("RECEIVABLES_AUTO_APPLY_TENANT_IDS", getenv("RECEIVABLES_AUTO_APPLY_TENANT_IDS"))
	if err != nil {
		return nil, err
	}
	if len(tenants) == 0 {
		slog.Info("receivables auto-apply job disabled; RECEIVABLES_AUTO_APPLY_TENANT_IDS is not set")
		return nil, nil
	}
	sched, err := parseSchedule(getenv, "RECEIVABLES_AUTO_APPLY", defaultAutoApplyInterval)
	if err != nil {
		return nil, err
	}
	return &autoApplyJob{sched: sched, tenants: tenants, store: s}, nil
}

func (j *autoApplyJob) loop(ctx context.Context) { j.sched.run(ctx, j.once) }

func (j *autoApplyJob) once(ctx context.Context) {
	forEachEntity(ctx, autoApplyActor, j.tenants, j.store.AutoApplyLegalEntities, func(ctx context.Context, tenantID, le string) error {
		res, err := j.store.AutoApplyDue(ctx, store.MaxAutoApplyBatch)
		if err != nil {
			return err
		}
		if res.Applied > 0 || res.Failed > 0 {
			slog.Info("receivables auto-apply finished", "tenantId", tenantID, "legalEntityId", le, "applied", res.Applied, "suggested", res.Suggested, "onAccount", res.OnAccount, "failed", res.Failed)
		}
		return nil
	})
}
