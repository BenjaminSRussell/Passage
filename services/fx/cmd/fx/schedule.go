package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fx/internal/importer"
	"github.com/BenjaminSRussell/passage/services/fx/internal/provider"
	"github.com/BenjaminSRussell/passage/services/fx/internal/store"
)

const (
	defaultInterval        = time.Hour
	defaultProviderTimeout = 10 * time.Second
	defaultECBURL          = "https://www.ecb.europa.eu/stats/eurofxref/eurofxref-daily.xml"
	defaultOXRURL          = "https://openexchangerates.org/api"
	importUser             = "system:fx-import"
)

type importSchedule struct {
	interval time.Duration
	once     func(context.Context)
}

func (s *importSchedule) loop(ctx context.Context) {
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

func parseTimeout(getenv func(string) string) (time.Duration, error) {
	raw := getenv("FX_PROVIDER_TIMEOUT_S")
	if raw == "" {
		return defaultProviderTimeout, nil
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return 0, fmt.Errorf("FX_PROVIDER_TIMEOUT_S must be a positive integer, got %q", raw)
	}
	return time.Duration(seconds) * time.Second, nil
}

func parseInterval(getenv func(string) string) (time.Duration, error) {
	raw := getenv("FX_IMPORT_INTERVAL")
	if raw == "" {
		return defaultInterval, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < time.Minute {
		return 0, fmt.Errorf("FX_IMPORT_INTERVAL must be a duration of at least 1m, got %q", raw)
	}
	return d, nil
}

func buildProvider(getenv func(string) string) (provider.Provider, error) {
	timeout, err := parseTimeout(getenv)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: timeout}
	switch name := addr(getenv, "FX_IMPORT_PROVIDER", "ecb"); name {
	case "ecb":
		return provider.NewECB(client, addr(getenv, "FX_ECB_URL", defaultECBURL)), nil
	case "openexchangerates":
		return provider.NewOXR(client, addr(getenv, "FX_OXR_URL", defaultOXRURL), getenv("FX_OXR_APP_ID")), nil
	default:
		return nil, fmt.Errorf("FX_IMPORT_PROVIDER must be ecb or openexchangerates, got %q", name)
	}
}

func newImportSchedule(ctx context.Context, getenv func(string) string, st store.Store, metrics *importer.Metrics) (*importSchedule, error) {
	tenantID := getenv("FX_IMPORT_TENANT_ID")
	if tenantID == "" {
		slog.Info("fx provider import disabled; FX_IMPORT_TENANT_ID is not set")
		return nil, nil
	}
	if !ids.Valid(tenantID) {
		return nil, errors.New("FX_IMPORT_TENANT_ID must be a UUID")
	}
	rateType := getenv("FX_IMPORT_RATE_TYPE")
	if rateType == "" {
		return nil, errors.New("FX_IMPORT_RATE_TYPE is required when FX_IMPORT_TENANT_ID is set")
	}
	p, err := buildProvider(getenv)
	if err != nil {
		return nil, err
	}
	if oxr, ok := p.(*provider.OXR); ok && !oxr.Enabled() {
		return nil, errors.New("FX_OXR_APP_ID is required when FX_IMPORT_PROVIDER is openexchangerates")
	}
	interval, err := parseInterval(getenv)
	if err != nil {
		return nil, err
	}
	scope := func(parent context.Context) context.Context {
		return tenant.With(parent, tenant.Principal{TenantID: tenantID, UserID: importUser})
	}
	if _, err := st.GetRateType(scope(ctx), rateType); err != nil {
		return nil, fmt.Errorf("FX_IMPORT_RATE_TYPE %q: %w", rateType, err)
	}
	last, err := st.LastSuccess(scope(ctx), p.Name(), rateType)
	if err != nil {
		slog.Warn("fx last import lookup failed; metric stays unset until the next success", "provider", p.Name(), "err", err)
	}
	metrics.Seed(scope(ctx), p.Name(), rateType, last)
	imp := importer.New(p, st, importer.DefaultConfig, metrics)
	return &importSchedule{interval: interval, once: func(ctx context.Context) {
		res, err := imp.Run(scope(ctx), rateType, time.Now().UTC())
		if err != nil {
			slog.Error("fx import failed", "provider", p.Name(), "rateType", rateType, "runId", res.RunID, "err", err)
			return
		}
		slog.Info("fx import finished", "provider", p.Name(), "rateType", rateType, "runId", res.RunID,
			"imported", res.Imported, "unchanged", res.Unchanged, "degraded", res.Degraded, "alreadyDone", res.AlreadyDone)
	}}, nil
}
