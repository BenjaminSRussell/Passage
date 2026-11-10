// Command scheduling runs the Passage scheduling service.
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
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/api"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/optimizer"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
	"github.com/BenjaminSRussell/passage/services/scheduling/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv, os.Args); err != nil {
		slog.Error("scheduling stopped", "err", err)
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
	return slog.New(handler).With("service", "scheduling")
}

func run(ctx context.Context, getenv func(string) string, args []string) error {
	consume := len(args) > 1 && args[1] == "consume"
	if len(args) > 1 && !consume {
		return fmt.Errorf("unknown command %q (use no argument to serve or \"consume\" to run the downtime.recorded consumer)", args[1])
	}
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	cfg, err := loadConfig(getenv)
	if err != nil {
		return err
	}
	pool, err := dbx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	if getenv("MIGRATE_ON_START") == "true" && !consume {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	a := api.New(store.New(pool).WithBottleneck(cfg.BottleneckPct, cfg.BottleneckWeek), cfg)
	// SCHED_OPT_URL is scheduler-opt (CP-SAT). Unset, cp-sat campaigns keep
	// the nearest-neighbour sequence and report SCHED-0006 UNAVAILABLE.
	if c := optimizer.NewClient(getenv("SCHED_OPT_URL")); c != nil {
		a.WithSequencer(c)
	}
	if consume {
		return runConsumer(ctx, getenv, a, pool)
	}
	router := httpx.NewRouter()
	a.Register(router)
	admin := http.NewServeMux()
	admin.Handle("/metrics", httpx.WithMetrics(a.Metrics()))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
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

// loadConfig reads and range-checks the scheduling settings (PRD 4.23.10).
func loadConfig(getenv func(string) string) (api.Config, error) {
	cfg := api.DefaultConfig
	for _, setting := range []struct {
		name     string
		min, max int
		target   *int
	}{
		{"SCHED_HORIZON_DAYS", 30, 730, &cfg.HorizonDays},
		{"SCHED_BOTTLENECK_PCT", 50, 200, &cfg.BottleneckPct},
		{"SCHED_BOTTLENECK_WEEKS", 1, 12, &cfg.BottleneckWeek},
	} {
		raw := getenv(setting.name)
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < setting.min || n > setting.max {
			return cfg, fmt.Errorf("%s must be an integer from %d to %d, got %q", setting.name, setting.min, setting.max, raw)
		}
		*setting.target = n
	}
	return cfg, nil
}
