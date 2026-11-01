package main

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/recon"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/store"
)

const reconUser = "system:revenue-reconciliation"

func newReconJob(getenv func(string) string, st *store.Postgres) (*recon.Job, error) {
	tenantID := getenv("ANALYTICS_RECON_TENANT_ID")
	if tenantID == "" {
		slog.Info("revenue reconciliation disabled; ANALYTICS_RECON_TENANT_ID is not set")
		return nil, nil
	}
	if !ids.Valid(tenantID) {
		return nil, fmt.Errorf("ANALYTICS_RECON_TENANT_ID must be a UUID")
	}
	for _, key := range []string{"CUBE_URL", "CUBE_API_TOKEN", "LEDGER_URL", "ANALYTICS_RECON_LEDGERS", "ANALYTICS_RECON_REVENUE_ACCOUNTS"} {
		if getenv(key) == "" {
			return nil, fmt.Errorf("%s is required when ANALYTICS_RECON_TENANT_ID is set", key)
		}
	}
	loc := time.UTC
	if name := getenv("ANALYTICS_RECON_TZ"); name != "" {
		var err error
		if loc, err = time.LoadLocation(name); err != nil {
			return nil, fmt.Errorf("ANALYTICS_RECON_TZ: %w", err)
		}
	}
	ledgers, err := parseLedgers(getenv("ANALYTICS_RECON_LEDGERS"))
	if err != nil {
		return nil, err
	}
	accountFrom, accountTo, ok := strings.Cut(getenv("ANALYTICS_RECON_REVENUE_ACCOUNTS"), "-")
	if !ok || accountFrom == "" || accountTo == "" {
		return nil, fmt.Errorf("ANALYTICS_RECON_REVENUE_ACCOUNTS must be FROM-TO, got %q", getenv("ANALYTICS_RECON_REVENUE_ACCOUNTS"))
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, fmt.Errorf("a service signing key is required for the revenue reconciliation to read the ledger")
	}
	return &recon.Job{
		Semantic:  recon.NewCubeReader(getenv("CUBE_URL"), getenv("CUBE_API_TOKEN"), nil),
		GL:        recon.NewLedgerReader(getenv("LEDGER_URL"), signer, nil, ledgers, accountFrom, accountTo),
		Recorder:  st,
		Principal: tenant.Principal{TenantID: tenantID, UserID: reconUser, Duties: map[string]struct{}{tenant.DutyAllScope: {}}},
		Location:  loc,
		Now:       time.Now,
	}, nil
}

func parseLedgers(raw string) ([]recon.LedgerRef, error) {
	var out []recon.LedgerRef
	for _, pair := range strings.Split(raw, ",") {
		entity, ledger, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || !ids.Valid(entity) || !ids.Valid(ledger) {
			return nil, fmt.Errorf("ANALYTICS_RECON_LEDGERS must be legalEntityId=ledgerId pairs, got %q", pair)
		}
		out = append(out, recon.LedgerRef{LegalEntityID: entity, LedgerID: ledger})
	}
	return out, nil
}
