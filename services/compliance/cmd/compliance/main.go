package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/api"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/store"
	"github.com/BenjaminSRussell/passage/services/compliance/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("compliance stopped", "err", err)
		os.Exit(1)
	}
}

func newLogger(w io.Writer) *slog.Logger {
	renamed := map[string]string{slog.TimeKey: "timestamp", slog.MessageKey: "message"}
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if name, ok := renamed[a.Key]; ok {
			a.Key = name
		}
		return a
	}})
	return slog.New(handler).With("service", "compliance")
}

func run(ctx context.Context, getenv func(string) string) error {
	if err := tenant.ConfigureServiceVerifier(getenv); err != nil {
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
	if getenv("MIGRATE_ON_START") == "true" {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	if err := tenant.ShareReplayCache(ctx, pool); err != nil {
		return err
	}
	cfg, err := config(getenv)
	if err != nil {
		return err
	}
	if cfg.Keys, err = fieldcrypt.FromEnv(getenv); err != nil {
		return fmt.Errorf("field encryption keys: %w", err)
	}
	a := api.New(store.New(pool), cfg)
	router := httpx.NewRouter()
	a.Register(router)
	admin := http.NewServeMux()
	admin.Handle("/metrics", a.Metrics())

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "HTTP_ADDR", defaultHTTPAddr), router) }()
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	return <-errs
}

// config reads the tenant policies from the environment (PRD §9.48.13):
// sod.block_high (default true), sod.max_mitigation_days (365, 1..730),
// audit.case_archive_days (365, 30..3650), breakglass.max_minutes (60, 15..240),
// breakglass.self_activate (false), breakglass.review_days (3, 1..10),
// screening.threshold (0.88, 0.70..0.99) and trade.license_check (BLOCK, WARN or OFF).
func config(getenv func(string) string) (api.Config, error) {
	cfg := api.Config{BlockHigh: true, MaxMitigationDays: 365, CaseArchiveDays: domain.DefaultCaseArchiveDays,
		BreakGlassMaxMinutes: domain.BreakGlassDefaultMinutes, BreakGlassReviewDays: domain.BreakGlassReviewDays}
	var err error
	if cfg.BlockHigh, err = boolEnv(getenv, "SOD_BLOCK_HIGH", true); err != nil {
		return cfg, err
	}
	if cfg.BreakGlassSelfActivate, err = boolEnv(getenv, "BREAKGLASS_SELF_ACTIVATE", false); err != nil {
		return cfg, err
	}
	cfg.ScreeningThreshold = domain.DefaultScreeningThreshold
	if v := getenv("SCREENING_THRESHOLD"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < domain.MinScreeningThreshold || f > domain.MaxScreeningThreshold {
			return cfg, fmt.Errorf("SCREENING_THRESHOLD must be %.2f..%.2f, got %q", domain.MinScreeningThreshold, domain.MaxScreeningThreshold, v)
		}
		cfg.ScreeningThreshold = f
	}
	switch v := getenv("TRADE_LICENSE_CHECK"); v {
	case "":
		cfg.LicenseCheck = domain.LicenseCheckBlock
	case domain.LicenseCheckBlock, domain.LicenseCheckWarn, domain.LicenseCheckOff:
		cfg.LicenseCheck = v
	default:
		return cfg, fmt.Errorf("TRADE_LICENSE_CHECK must be BLOCK, WARN or OFF, got %q", v)
	}
	for _, p := range []struct {
		key      string
		min, max int
		dst      *int
	}{
		{"SOD_MAX_MITIGATION_DAYS", 1, 730, &cfg.MaxMitigationDays},
		{"AUDIT_CASE_ARCHIVE_DAYS", domain.MinCaseArchiveDays, domain.MaxCaseArchiveDays, &cfg.CaseArchiveDays},
		{"BREAKGLASS_MAX_MINUTES", domain.BreakGlassMinMinutes, domain.BreakGlassHardMaxMinutes, &cfg.BreakGlassMaxMinutes},
		{"BREAKGLASS_REVIEW_DAYS", domain.BreakGlassMinReviewDays, domain.BreakGlassMaxReviewDays, &cfg.BreakGlassReviewDays},
	} {
		v := getenv(p.key)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < p.min || n > p.max {
			return cfg, fmt.Errorf("%s must be %d..%d, got %q", p.key, p.min, p.max, v)
		}
		*p.dst = n
	}
	return cfg, nil
}

func boolEnv(getenv func(string) string, key string, fallback bool) (bool, error) {
	switch v := getenv(key); v {
	case "":
		return fallback, nil
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return fallback, fmt.Errorf("%s must be true or false, got %q", key, v)
	}
}

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
