package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/ops/internal/api"
	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ops/internal/store"
	"github.com/BenjaminSRussell/passage/services/ops/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("ops stopped", "err", err)
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
	return slog.New(handler).With("service", "ops")
}

func run(ctx context.Context, getenv func(string) string) error {
	// The ops service must answer in a fenced region: it runs the rejoin and serves /healthz/region.
	httpx.DisableFence()
	if err := tenant.ConfigureServiceVerifier(getenv); err != nil {
		return err
	}
	cfg := api.Config{Region: getenv("REGION_NAME")}
	operator, ok := ids.Canonical(getenv("OPS_OPERATOR_TENANT_ID"))
	if !ok || operator == ids.Nil {
		return errors.New("OPS_OPERATOR_TENANT_ID must be the platform operator's tenant id (a UUID): the ops routes serve that tenant only")
	}
	cfg.OperatorTenantID = operator
	if v := getenv("FAILOVER_MAX_LAG_S"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("FAILOVER_MAX_LAG_S must be a positive number of seconds, got %q", v)
		}
		cfg.MaxLagSeconds = n
	}
	for _, t := range []struct {
		env string
		dst *int
	}{{"RTO_TARGET_MIN", &cfg.RTOTargetMinutes}, {"RPO_TARGET_S", &cfg.RPOTargetSeconds}} {
		if v := getenv(t.env); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return fmt.Errorf("%s must be a positive number, got %q", t.env, v)
			}
			*t.dst = n
		}
	}
	if v := getenv("OPS_LICENSE_PUBLIC_KEY"); v != "" {
		key, err := base64.StdEncoding.DecodeString(v)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return errors.New("OPS_LICENSE_PUBLIC_KEY must be the base64 of a 32-byte Ed25519 public key")
		}
		cfg.LicensePublicKey = key
	}
	if err := part3Config(getenv, &cfg); err != nil {
		return err
	}
	checkEvery := time.Hour
	if v := getenv("OPS_CHECK_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Minute {
			return fmt.Errorf("OPS_CHECK_INTERVAL must be a duration of at least 1m, got %q", v)
		}
		checkEvery = d
	}
	seed, err := domain.ParseRegions(getenv("OPS_REGIONS"))
	if err != nil {
		return fmt.Errorf("OPS_REGIONS: %w", err)
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
	lagPolicy := store.LagPolicy{StaleAfter: 90 * time.Second, RequireReports: getenv("OPS_REQUIRE_LAG_REPORTS") == "true"}
	if v := getenv("OPS_LAG_STALE_S"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return fmt.Errorf("OPS_LAG_STALE_S must be a number of seconds (0 turns the check off), got %q", v)
		}
		lagPolicy.StaleAfter = time.Duration(n) * time.Second
	}
	st := store.New(pool).WithLagPolicy(lagPolicy).WithOperator(operator)
	if len(seed) > 0 {
		if err := st.Seed(ctx, seed); err != nil {
			return fmt.Errorf("seed regions: %w", err)
		}
	}
	a := api.New(st, cfg)
	router := httpx.NewRouterWithoutTenant()
	a.Register(router)
	admin := http.NewServeMux()
	admin.Handle("/metrics", httpx.WithMetrics(a.Metrics()))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The hourly licence check and, with PROMETHEUS_URL, the SLO check (slo.breached, license.changed).
	go a.RunChecks(ctx, checkEvery)
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "HTTP_ADDR", defaultHTTPAddr), router) }()
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	return <-errs
}

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}

// part3Config reads the SLO, cost and licence-check settings.
func part3Config(getenv func(string) string, cfg *api.Config) error {
	if u := getenv("PROMETHEUS_URL"); u != "" {
		parsed, err := url.Parse(u)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("PROMETHEUS_URL must be an http(s) URL, got %q", u)
		}
		cfg.SLOSource = api.PromSource{URL: u}
	}
	if v := getenv("SLO_AVAILABILITY_TARGET"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 || f >= 1 {
			return fmt.Errorf("SLO_AVAILABILITY_TARGET must be a fraction between 0 and 1 (0.999), got %q", v)
		}
		cfg.SLOTargets.Availability = f
	}
	if v := getenv("SLO_LATENCY_P95_MS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("SLO_LATENCY_P95_MS must be a positive number of milliseconds, got %q", v)
		}
		cfg.SLOTargets.LatencyP95 = time.Duration(n) * time.Millisecond
	}
	if v := getenv("SLO_LATENCY_P95_BY"); v != "" {
		var by map[string]int
		if err := json.Unmarshal([]byte(v), &by); err != nil {
			return fmt.Errorf("SLO_LATENCY_P95_BY must be a JSON object of service to milliseconds: %w", err)
		}
		cfg.SLOTargets.LatencyP95By = map[string]time.Duration{}
		for svc, ms := range by {
			if ms <= 0 {
				return fmt.Errorf("SLO_LATENCY_P95_BY: %s must be a positive number of milliseconds", svc)
			}
			cfg.SLOTargets.LatencyP95By[svc] = time.Duration(ms) * time.Millisecond
		}
	}
	if v := getenv("OPS_COST_RATES"); v != "" {
		rates, err := domain.ParseCostRates(v)
		if err != nil {
			return fmt.Errorf("OPS_COST_RATES: %w", err)
		}
		cfg.CostRates = &rates
	}
	return nil
}
