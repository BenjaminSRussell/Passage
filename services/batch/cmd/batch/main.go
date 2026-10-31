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
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/batch/internal/api"
	"github.com/BenjaminSRussell/passage/services/batch/internal/jobs"
	"github.com/BenjaminSRussell/passage/services/batch/internal/notifyout"
	"github.com/BenjaminSRussell/passage/services/batch/internal/store"
	"github.com/BenjaminSRussell/passage/services/batch/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"

	serviceName         = "batch"
	productPageAttempts = 3
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("batch stopped", "err", err)
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
	return slog.New(handler).With("service", "batch")
}

func run(ctx context.Context, getenv func(string) string) error {
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	notifyCfg, err := notifyout.Load(getenv)
	if err != nil {
		return fmt.Errorf("notify config: %w", err)
	}
	slog.Info("notify adapters configured", "adapters", notifyCfg.Enabled())
	dispatch, err := loadDispatchConfig(getenv)
	if err != nil {
		return err
	}
	distributeInterval, err := durationEnv(getenv, "NOTIFY_DISTRIBUTE_INTERVAL", notifyout.DefaultDistributeInterval)
	if err != nil {
		return err
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
	a := api.New(store.New(pool))
	router := httpx.NewRouter()
	a.Register(router)
	admin := http.NewServeMux()
	metrics := []http.Handler{a.Metrics()}

	ctx, cancel := context.WithCancel(ctx)
	var background sync.WaitGroup
	defer func() {
		cancel()
		background.Wait()
	}()
	if dispatch == nil {
		slog.Warn("run dispatcher disabled: PRODUCT_URL is empty")
	} else {
		d := dispatch.dispatcher(pool)
		metrics = append(metrics, d.Metrics())
		background.Go(func() { d.Run(ctx) })
	}
	if senders := notifyout.SendersFor(notifyCfg); len(senders) == 0 {
		slog.Warn("notify distributor disabled: no notify adapter is enabled")
	} else {
		n := &notifyout.Distributor{Pool: pool, Senders: senders, Interval: distributeInterval}
		metrics = append(metrics, n.Metrics())
		background.Go(func() { n.Run(ctx) })
	}
	admin.Handle("/metrics", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, h := range metrics {
			h.ServeHTTP(w, r)
		}
	}))
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

type dispatchConfig struct {
	signer      *tenant.ServiceSigner
	productURL  string
	interval    time.Duration
	retryAfter  time.Duration
	concurrency int
	maxAttempts int
}

func loadDispatchConfig(getenv func(string) string) (*dispatchConfig, error) {
	productURL := getenv("PRODUCT_URL")
	if productURL == "" {
		return nil, nil
	}
	if getenv(tenant.EnvServiceName) != serviceName {
		return nil, fmt.Errorf("PRODUCT_URL requires %s=%s", tenant.EnvServiceName, serviceName)
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, fmt.Errorf("PRODUCT_URL requires %s or %s", tenant.EnvServiceSigningKey, tenant.EnvServiceSigningFile)
	}
	cfg := &dispatchConfig{signer: signer, productURL: productURL}
	if cfg.interval, err = durationEnv(getenv, "DISPATCH_INTERVAL", jobs.DefaultDispatchInterval); err != nil {
		return nil, err
	}
	if cfg.retryAfter, err = durationEnv(getenv, "DISPATCH_RETRY_AFTER", jobs.DefaultRetryAfter); err != nil {
		return nil, err
	}
	if cfg.concurrency, err = intEnv(getenv, "DISPATCH_CONCURRENCY", jobs.DefaultDispatchConcurrency); err != nil {
		return nil, err
	}
	if cfg.maxAttempts, err = intEnv(getenv, "DISPATCH_MAX_RUN_ATTEMPTS", jobs.DefaultRunAttempts); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *dispatchConfig) dispatcher(pool *pgxpool.Pool) *jobs.Dispatcher {
	return &jobs.Dispatcher{
		Source: jobs.NewPgRunSource(pool),
		Executor: jobs.ProductJobs{
			Client:      jobs.NewHTTPProductClient(c.productURL, c.signer),
			Pages:       jobs.NewPgPageStore(pool),
			MaxAttempts: productPageAttempts,
		},
		Interval:    c.interval,
		Concurrency: c.concurrency,
		MaxAttempts: c.maxAttempts,
		RetryAfter:  c.retryAfter,
	}
}

func durationEnv(getenv func(string) string, key string, fallback time.Duration) (time.Duration, error) {
	raw := getenv(key)
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", key)
	}
	return d, nil
}

func intEnv(getenv func(string) string, key string, fallback int) (int, error) {
	raw := getenv(key)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return n, nil
}
