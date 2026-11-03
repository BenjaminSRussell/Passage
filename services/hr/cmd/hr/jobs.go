package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/hr/internal/compliance"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const jobTimeout = 30 * time.Minute

type tenantJob struct {
	command string
	actor   string
	run     func(ctx context.Context, s *store.Postgres, tenantID string, getenv func(string) string) (slog.Attr, error)
}

var (
	accrueJob = tenantJob{command: "accrue", actor: "system:hr-accrue", run: func(ctx context.Context, s *store.Postgres, tenantID string, _ func(string) string) (slog.Attr, error) {
		res, err := s.RunScheduledAccrual(ctx)
		return slog.Any("result", res), err
	}}
	carryOverExpiryJob = tenantJob{command: "carryover-expiry", actor: "system:hr-carryover-expiry", run: func(ctx context.Context, s *store.Postgres, tenantID string, _ func(string) string) (slog.Attr, error) {
		res, err := s.RunScheduledCarryOverExpiry(ctx)
		return slog.Any("result", res), err
	}}
	retentionPurgeJob = tenantJob{command: "retention-purge", actor: "system:hr-retention-purge", run: func(ctx context.Context, s *store.Postgres, tenantID string, getenv func(string) string) (slog.Attr, error) {
		holds, err := legalHoldsFromEnv(getenv)
		if err != nil {
			return slog.Attr{}, err
		}
		res, err := s.PurgeCandidates(ctx, holds)
		return slog.Group("result", slog.Int("erased", res.Erased), slog.Int("held", res.HeldCount)), err
	}}
	tenantJobs = []tenantJob{accrueJob, carryOverExpiryJob, retentionPurgeJob}
)

func legalHoldsFromEnv(getenv func(string) string) (store.CandidateHolds, error) {
	if getenv("HR_RETENTION_PURGE_ENABLED") == "false" {
		return nil, errors.New("the retention purge is switched off (HR_RETENTION_PURGE_ENABLED=false)")
	}
	url := getenv("COMPLIANCE_URL")
	if url == "" {
		return nil, errors.New("COMPLIANCE_URL is required: the purge checks legal holds before erasing")
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	return compliance.New(url).WithSigner(signer), nil
}

func jobByCommand(command string) (tenantJob, bool) {
	for _, j := range tenantJobs {
		if j.command == command {
			return j, true
		}
	}
	return tenantJob{}, false
}

func parseTenants(command string, args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("usage: hr %s TENANT_ID [TENANT_ID ...]", command)
	}
	tenants := make([]string, len(args))
	for i, arg := range args {
		id, ok := ids.Canonical(arg)
		if !ok || id == ids.Nil {
			return nil, fmt.Errorf("tenant id %q is not a UUID", arg)
		}
		tenants[i] = id
	}
	return tenants, nil
}

func runTenantJob(ctx context.Context, getenv func(string) string, job tenantJob, args []string) error {
	tenants, err := parseTenants(job.command, args)
	if err != nil {
		return err
	}
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	pool, err := dbx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	s := store.New(pool, nil, defaultMaxDepth)
	var failed []error
	for _, id := range tenants {
		runCtx, cancel := context.WithTimeout(tenant.With(ctx, tenant.Principal{
			TenantID: id, UserID: job.actor, Duties: map[string]struct{}{tenant.DutyAllScope: {}},
		}), jobTimeout)
		result, err := job.run(runCtx, s, id, getenv)
		cancel()
		if err != nil {
			slog.Error("hr job failed", "job", job.command, "tenantId", id, "err", err)
			failed = append(failed, fmt.Errorf("tenant %s: %w", id, err))
			continue
		}
		slog.Info("hr job finished", "job", job.command, "tenantId", id, result)
	}
	return errors.Join(failed...)
}
