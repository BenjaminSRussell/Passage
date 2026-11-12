package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/fxclient"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/orgclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/ledgerrecon"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/posting"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/store"
)

const (
	reconcileCommand = "reconcile"
	reconcileActor   = "system:intercompany-reconcile"
)

// reconciler is what the API and the scheduled job share: the ledger client, the poster that moves past closed periods and the
// reconciler that reads the pairs.
type reconciler struct {
	ledger     *ledgerclient.Client
	poster     *posting.Poster
	reconciler *ledgerrecon.Reconciler
}

// buildReconciler wires the ledger, the rate tables and the fiscal calendar from the environment. Without LEDGER_URL the result has
// no ledger and the posting and ledger reconciliation routes stay off.
func buildReconciler(getenv func(string) string, st *store.Postgres) (reconciler, error) {
	ledger, err := ledgerclient.FromEnv(getenv, "intercompany")
	if err != nil {
		return reconciler{}, fmt.Errorf("ledger client: %w", err)
	}
	if ledger == nil {
		return reconciler{}, nil
	}
	poster := posting.New(ledger, st)
	calendars, err := orgclient.FromEnv(getenv)
	if err != nil {
		return reconciler{}, fmt.Errorf("org client: %w", err)
	}
	if calendars != nil {
		years := orgclient.NewYears(calendars)
		poster.WithNextPeriod(func(ctx context.Context, legalEntityID string, date time.Time) (time.Time, error) {
			next, err := years.NextPeriodStart(ctx, legalEntityID, date)
			if errors.Is(err, orgclient.ErrNoCalendar) {
				return time.Date(date.Year(), date.Month()+1, 1, 0, 0, 0, 0, time.UTC), nil
			}
			return next, err
		})
	}
	var rates fxclient.Rater
	fx, err := fxclient.FromEnv(getenv)
	if err != nil {
		return reconciler{}, fmt.Errorf("fx client: %w", err)
	}
	if fx != nil {
		rates = fx
	}
	return reconciler{ledger: ledger, poster: poster, reconciler: ledgerrecon.New(st, st, st, ledger, poster, rates, getenv("FX_RATE_TYPE"))}, nil
}

// reconcilePeriods are the periods the schedule reads: the one just ended and the one running, so a difference that is only timing
// is seen again once the counterpart posts.
func reconcilePeriods(now time.Time) []string {
	first := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	return []string{first.AddDate(0, -1, 0).Format("2006-01"), first.Format("2006-01")}
}

// reconcileJob is `intercompany reconcile TENANT_ID [TENANT_ID ...]` (FR-40.6): for each tenant that has set a reconciliation
// currency, it reads every pair of the stored matrix from the ledgers for the last and the current period and stores the runs. A
// run that flags items publishes reconciliation.flagged.v1 like any other; the job fails only when a tenant could not be read.
func reconcileJob(ctx context.Context, getenv func(string) string, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: intercompany reconcile TENANT_ID [TENANT_ID ...]")
	}
	tenants := make([]string, len(args))
	for i, arg := range args {
		id, ok := ids.Canonical(arg)
		if !ok || id == ids.Nil {
			return fmt.Errorf("tenant id %q is not a UUID", arg)
		}
		tenants[i] = id
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
	st := store.New(pool)
	rec, err := buildReconciler(getenv, st)
	if err != nil {
		return err
	}
	if rec.reconciler == nil {
		return errors.New("LEDGER_URL is required")
	}
	return reconcileTenants(ctx, st, rec.reconciler, tenants, time.Now())
}

type settingsReader interface {
	GetSettings(ctx context.Context) (domain.Settings, error)
}

type runner interface {
	Run(ctx context.Context, period, currency string, tolerance decimal.Decimal, key string) (domain.ReconRun, bool, error)
}

func reconcileTenants(ctx context.Context, st settingsReader, rec runner, tenants []string, now time.Time) error {
	failed := 0
	for _, tenantID := range tenants {
		tctx := tenant.With(ctx, tenant.Principal{TenantID: tenantID, UserID: reconcileActor, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
		settings, err := st.GetSettings(tctx)
		if err != nil {
			slog.ErrorContext(ctx, "intercompany reconcile cannot read the settings", "tenantId", tenantID, "err", err)
			failed++
			continue
		}
		if settings.ReconCurrency == "" {
			slog.InfoContext(ctx, "intercompany reconcile skipped: the tenant has no reconciliation currency", "tenantId", tenantID)
			continue
		}
		for _, period := range reconcilePeriods(now) {
			key := fmt.Sprintf("scheduled-recon:%s:%s", period, now.UTC().Format(time.DateOnly))
			run, replayed, err := rec.Run(tctx, period, settings.ReconCurrency, settings.FXTolerancePct, key)
			if err != nil {
				slog.ErrorContext(ctx, "intercompany reconciliation failed", "tenantId", tenantID, "period", period, "err", err)
				failed++
				continue
			}
			slog.InfoContext(ctx, "intercompany reconciled from the ledgers", "tenantId", tenantID, "period", period, "run", run.ID, "replayed", replayed,
				"matched", run.Matched, "timing", run.Timing, "mismatch", run.Mismatch)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d reconciliations could not be run", failed)
	}
	return nil
}
