package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

const (
	captureJobActor           = "system:fieldservice-capture-retention"
	defaultCaptureJobInterval = time.Hour
	maxRetentionDays          = 3650
)

// captureRetainer is the store side of the job.
type captureRetainer interface {
	CaptureLegalEntities(ctx context.Context) ([]string, error)
	OffloadCaptures(ctx context.Context, limit int) (int, error)
	PurgeCaptures(ctx context.Context, now time.Time, retention time.Duration, limit int) (store.CaptureRetentionResult, error)
}

// captureJob moves captures still held in Postgres to the object store (only when one is configured) and deletes the bytes of
// the captures of finished work orders once they are older than the retention (#45). It is off until
// FIELDSERVICE_CAPTURE_JOB_TENANT_IDS names tenants; FIELDSERVICE_CAPTURE_RETENTION_DAYS (0 or unset: never purge) sets the
// retention and FIELDSERVICE_CAPTURE_JOB_INTERVAL (default 1h) or _AT ("HH:MM" UTC) when it runs. Replicas may all run it: both
// steps are idempotent.
type captureJob struct {
	sched     schedule
	tenants   []string
	retention time.Duration
	store     captureRetainer
	now       func() time.Time
}

func newCaptureJob(getenv func(string) string, s captureRetainer) (*captureJob, error) {
	tenants, err := parseTenants("FIELDSERVICE_CAPTURE_JOB_TENANT_IDS", getenv("FIELDSERVICE_CAPTURE_JOB_TENANT_IDS"))
	if err != nil {
		return nil, err
	}
	if len(tenants) == 0 {
		slog.Info("fieldservice capture retention job disabled; FIELDSERVICE_CAPTURE_JOB_TENANT_IDS is not set")
		return nil, nil
	}
	var retention time.Duration
	if v := getenv("FIELDSERVICE_CAPTURE_RETENTION_DAYS"); v != "" {
		days, err := strconv.Atoi(v)
		if err != nil || days < 0 || days > maxRetentionDays {
			return nil, fmt.Errorf("FIELDSERVICE_CAPTURE_RETENTION_DAYS must be a whole number of days from 0 (never purge) to %d, got %q", maxRetentionDays, v)
		}
		retention = time.Duration(days) * 24 * time.Hour
	}
	sched, err := parseSchedule(getenv, "FIELDSERVICE_CAPTURE_JOB", defaultCaptureJobInterval)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("capture retention job needs a store")
	}
	return &captureJob{sched: sched, tenants: tenants, retention: retention, store: s, now: time.Now}, nil
}

func (j *captureJob) loop(ctx context.Context) { j.sched.run(ctx, j.once) }

func (j *captureJob) once(ctx context.Context) {
	forEachEntity(ctx, captureJobActor, j.tenants, j.store.CaptureLegalEntities, func(ctx context.Context, tenantID, le string) error {
		offloaded, offErr := j.store.OffloadCaptures(ctx, store.MaxCaptureBatch)
		res, purgeErr := j.store.PurgeCaptures(ctx, j.now().UTC(), j.retention, store.MaxCaptureBatch)
		if offloaded > 0 || res.Purged > 0 || res.Failed > 0 {
			slog.Info("fieldservice capture retention finished", "tenantId", tenantID, "legalEntityId", le, "offloaded", offloaded, "purged", res.Purged, "failed", res.Failed)
		}
		return errors.Join(offErr, purgeErr)
	})
}
