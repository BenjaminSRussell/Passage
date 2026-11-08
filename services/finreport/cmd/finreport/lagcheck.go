package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/config"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

const (
	lagCheckCommand = "lag-check"
	// lagCheckActor runs the check in-process. It is not a service principal:
	// it calls no other service and no route, so it needs no authz seed (the
	// pattern of costacct cost-gl-reconcile and the hr jobs).
	lagCheckActor = "system:finreport-lag-check"
)

type lagChecker interface {
	CheckLag(ctx context.Context, maxLag, sustain int) (store.LagCheck, error)
}

// lagCheck is `finreport lag-check TENANT_ID...` (FR-15.1, PRD §2.15.5), run by
// the chart's CronJob every minute: for each tenant it opens and closes lag
// episodes and writes one dev.passage.reporting.lag.exceeded.v1 per episode
// once the lag has stayed above REPORTING_MAX_LAG_S for REPORTING_LAG_ALERT_S.
func lagCheck(ctx context.Context, getenv func(string) string, args []string) error {
	tenants, err := tenantArgs(lagCheckCommand, args)
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
	return checkTenants(ctx, store.New(pool), cfg, tenants)
}

func tenantArgs(command string, args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("usage: finreport %s TENANT_ID [TENANT_ID ...]", command)
	}
	out := make([]string, len(args))
	for i, arg := range args {
		id, ok := ids.Canonical(arg)
		if !ok || id == ids.Nil {
			return nil, fmt.Errorf("tenant id %q is not a UUID", arg)
		}
		out[i] = id
	}
	return out, nil
}

// checkTenants checks every tenant even when one fails, and fails the job when
// any did, so a broken check shows as a failed CronJob run.
func checkTenants(ctx context.Context, st lagChecker, cfg config.Reporting, tenants []string) error {
	failed := 0
	for _, tid := range tenants {
		tctx := tenant.With(ctx, tenant.Principal{TenantID: tid, UserID: lagCheckActor, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
		res, err := st.CheckLag(tctx, cfg.MaxLagSeconds, cfg.LagAlertSeconds)
		if err != nil {
			slog.ErrorContext(ctx, "lag check failed", "tenantId", tid, "err", err)
			failed++
			continue
		}
		for _, a := range res.Raised {
			slog.WarnContext(ctx, "reporting read model lag exceeded", "tenantId", tid, "ledgerId", a.LedgerID,
				"lagSeconds", a.LagSeconds, "hwmLedger", a.HWMLedger, "hwmReporting", a.HWMReporting)
		}
		slog.InfoContext(ctx, "lag check", "tenantId", tid, "checked", res.Checked, "exceeded", res.Exceeded, "raised", len(res.Raised))
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d tenant lag checks failed", failed, len(tenants))
	}
	return nil
}
