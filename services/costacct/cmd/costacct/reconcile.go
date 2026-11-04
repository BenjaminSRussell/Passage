package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/domain"
	"github.com/BenjaminSRussell/passage/services/costacct/internal/store"
)

const (
	reconcileCommand = "cost-gl-reconcile"
	reconcileActor   = "system:cost-gl-reconcile"
)

type reconciler interface {
	LegalEntities(ctx context.Context) ([]string, error)
	ReconcileToGL(ctx context.Context, gl store.GLBalances, in store.GLReconcileRequest) (domain.Reconciliation, error)
}

func parseLedgerIDs(raw string) (map[string]string, error) {
	out := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		rawLE, rawLedger, ok := strings.Cut(pair, "=")
		le, leOK := ids.Canonical(rawLE)
		ledger, ledgerOK := ids.Canonical(rawLedger)
		if !ok || !leOK || !ledgerOK {
			return nil, fmt.Errorf("COSTACCT_LEDGER_IDS entry %q must be <legalEntityId>=<ledgerId> with two UUIDs", pair)
		}
		out[le] = ledger
	}
	return out, nil
}

func reconcilePeriods(now time.Time) []string {
	first := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	return []string{first.AddDate(0, -1, 0).Format("2006-01"), first.Format("2006-01")}
}

// costGLReconcile is the nightly `costacct cost-gl-reconcile <tenant-id>...` (FR-38.11, AC-38.6): for each tenant, legal
// entity and open period it reads the ledger's balances and compares them with the cost ledger. A difference, an unmapped
// legal entity or a ledger failure fails the run, which is the alert.
func costGLReconcile(ctx context.Context, getenv func(string) string, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: costacct cost-gl-reconcile TENANT_ID [TENANT_ID ...]")
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
	gl, err := ledgerclient.FromEnv(getenv, "costacct")
	if err != nil {
		return err
	}
	if gl == nil {
		return errors.New("LEDGER_URL is required")
	}
	ledgerIDs, err := parseLedgerIDs(getenv("COSTACCT_LEDGER_IDS"))
	if err != nil {
		return err
	}
	pool, err := dbx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	return reconcileTenants(ctx, store.New(pool), gl, ledgerIDs, tenants, reconcilePeriods(time.Now()))
}

func reconcileTenants(ctx context.Context, st reconciler, gl store.GLBalances, ledgerIDs map[string]string, tenants, periods []string) error {
	failed := 0
	for _, tenantID := range tenants {
		tctx := tenant.With(ctx, tenant.Principal{TenantID: tenantID, UserID: reconcileActor, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
		entities, err := st.LegalEntities(tctx)
		if err != nil {
			slog.ErrorContext(ctx, "cost GL reconciliation cannot list legal entities", "tenantId", tenantID, "err", err)
			failed++
			continue
		}
		for _, le := range entities {
			ledgerID, ok := ledgerIDs[le]
			if !ok {
				slog.ErrorContext(ctx, "cost GL reconciliation has no ledger for the legal entity", "tenantId", tenantID, "legalEntityId", le)
				failed++
				continue
			}
			for _, period := range periods {
				if !reconcileOne(tctx, st, gl, tenantID, le, ledgerID, period) {
					failed++
				}
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d cost GL reconciliations failed or differ", failed)
	}
	return nil
}

func reconcileOne(ctx context.Context, st reconciler, gl store.GLBalances, tenantID, le, ledgerID, period string) bool {
	res, err := st.ReconcileToGL(ctx, gl, store.GLReconcileRequest{LegalEntityID: le, LedgerID: ledgerID, Period: period})
	if err != nil {
		slog.ErrorContext(ctx, "cost GL reconciliation failed", "tenantId", tenantID, "legalEntityId", le, "period", period, "err", err)
		return false
	}
	if res.Reconciled {
		slog.InfoContext(ctx, "cost ledger reconciles to the general ledger", "tenantId", tenantID, "legalEntityId", le, "period", period, "elements", len(res.Lines))
		return true
	}
	for _, l := range res.Lines {
		if !l.Difference.IsZero() {
			slog.ErrorContext(ctx, "cost ledger differs from the general ledger", "tenantId", tenantID, "legalEntityId", le, "period", period,
				"costElementId", l.ElementID, "journal", l.Journal.String(), "costLedger", l.CostLedger.String(), "difference", l.Difference.String())
		}
	}
	return false
}
