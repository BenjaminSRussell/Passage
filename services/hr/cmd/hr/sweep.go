package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const (
	sweepActor           = "system:hr-action-sweep"
	defaultSweepInterval = 15 * time.Minute
	minSweepInterval     = time.Minute
	sweepTimeout         = 5 * time.Minute
)

type actionSweep struct {
	interval time.Duration
	tenants  []string
	apply    func(context.Context) (store.ApplyResult, error)
}

func newActionSweep(getenv func(string) string, apply func(context.Context) (store.ApplyResult, error)) (*actionSweep, error) {
	raw := getenv("HR_ACTION_SWEEP_TENANT_IDS")
	if strings.TrimSpace(raw) == "" {
		slog.Info("hr due-action sweep disabled; HR_ACTION_SWEEP_TENANT_IDS is not set")
		return nil, nil
	}
	var tenants []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		id, ok := ids.Canonical(strings.TrimSpace(part))
		if !ok || id == ids.Nil {
			return nil, fmt.Errorf("HR_ACTION_SWEEP_TENANT_IDS entry %q is not a UUID", part)
		}
		if !seen[id] {
			seen[id] = true
			tenants = append(tenants, id)
		}
	}
	interval := defaultSweepInterval
	if v := getenv("HR_ACTION_SWEEP_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < minSweepInterval {
			return nil, errors.New("HR_ACTION_SWEEP_INTERVAL must be a duration of at least 1m, got " + v)
		}
		interval = d
	}
	return &actionSweep{interval: interval, tenants: tenants, apply: apply}, nil
}

func (s *actionSweep) loop(ctx context.Context) {
	s.once(ctx)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.once(ctx)
		}
	}
}

func (s *actionSweep) once(ctx context.Context) {
	for _, tenantID := range s.tenants {
		runCtx, cancel := context.WithTimeout(tenant.With(ctx, tenant.Principal{
			TenantID: tenantID, UserID: sweepActor, Duties: map[string]struct{}{tenant.DutyAllScope: {}},
		}), sweepTimeout)
		res, err := s.apply(runCtx)
		cancel()
		if err != nil {
			slog.Error("hr due-action sweep failed", "tenantId", tenantID, "err", err)
			continue
		}
		slog.Info("hr due-action sweep finished", "tenantId", tenantID, "applied", res.Applied, "failed", res.Failed)
	}
}
