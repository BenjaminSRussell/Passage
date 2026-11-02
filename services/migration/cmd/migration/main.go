package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/api"
	"github.com/BenjaminSRussell/passage/services/migration/internal/connector"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/egress"
	"github.com/BenjaminSRussell/passage/services/migration/internal/probe"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/internal/syncer"
	"github.com/BenjaminSRussell/passage/services/migration/internal/target"
	"github.com/BenjaminSRussell/passage/services/migration/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("migration stopped", "err", err)
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
	return slog.New(handler).With("service", "migration")
}

// connectionDeps reads the fieldcrypt keys (nil when unset) and the operator egress allowlist MIG_EGRESS_ALLOW. A bad
// value stops the service at start: a typo in the allowlist must not silently widen or narrow what it can reach.
func connectionDeps(getenv func(string) string) (*fieldcrypt.Keys, *probe.Prober, error) {
	keys, err := fieldcrypt.FromEnv(getenv)
	if err != nil {
		return nil, nil, fmt.Errorf("fieldcrypt keys: %w", err)
	}
	policy, err := egress.ParsePolicy(getenv("MIG_EGRESS_ALLOW"))
	if err != nil {
		return nil, nil, err
	}
	return keys, &probe.Prober{Guard: &egress.Guard{Policy: policy}, DropRoot: getenv("MIG_DROP_ROOT")}, nil
}

// targetConfig reads the owning services' base URLs (MIG_TARGET_PARTY_URL, MIG_TARGET_PRODUCT_URL,
// MIG_TARGET_RECEIVABLES_URL) and the service signing key the loader's calls are signed with. The key must belong to
// the service named "migration" (PASSAGE_SERVICE_NAME), the caller the service grants are written for. An address
// without a key stops the service: loading unsigned would be refused by every target.
func targetConfig(getenv func(string) string) (target.Config, error) {
	cfg := target.Config{PartyURL: getenv("MIG_TARGET_PARTY_URL"), ProductURL: getenv("MIG_TARGET_PRODUCT_URL"), ReceivablesURL: getenv("MIG_TARGET_RECEIVABLES_URL")}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return target.Config{}, err
	}
	for name, raw := range map[string]string{"MIG_TARGET_PARTY_URL": cfg.PartyURL, "MIG_TARGET_PRODUCT_URL": cfg.ProductURL, "MIG_TARGET_RECEIVABLES_URL": cfg.ReceivablesURL} {
		if raw == "" {
			continue
		}
		if u, err := url.Parse(raw); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return target.Config{}, fmt.Errorf("%s must be an http or https base address such as http://party:8080", name)
		}
	}
	anyURL := cfg.PartyURL != "" || cfg.ProductURL != "" || cfg.ReceivablesURL != ""
	switch {
	case signer == nil && anyURL:
		return target.Config{}, errors.New("MIG_TARGET_*_URL is set but no service signing key is (PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE)")
	case signer != nil && getenv(tenant.EnvServiceName) != target.Caller:
		return target.Config{}, fmt.Errorf("%s must be %q for the loader's service grants", tenant.EnvServiceName, target.Caller)
	}
	cfg.Signer = signer
	return cfg, nil
}

func run(ctx context.Context, getenv func(string) string) error {
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	keys, prober, err := connectionDeps(getenv)
	if err != nil {
		return err
	}
	targets, err := targetConfig(getenv)
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
	// Without fieldcrypt keys the secret and test routes answer 501 MIG-SECRETS-NOT-CONFIGURED.
	st := store.New(pool)
	a := api.New(st, api.NotConfigured).WithConnections(keys, prober).WithExtraction(connector.Env{Guard: prober.Guard, DropRoot: prober.DropRoot})
	if targets.Configured() {
		// With the owning services' addresses and a signing key, runs extract from the entity binding and load
		// through their APIs. Without them POST /migration-runs keeps answering 501 MIG-BACKEND-NOT-CONFIGURED.
		a = a.WithTargets(func(_ context.Context, run api.TargetRun) (domain.TargetLoader, domain.TargetReader, error) {
			return target.New(targets, target.Run{SourceSystem: run.SourceSystem, LegalEntityID: run.LegalEntityID, Xref: st.Xref()}), nil, nil
		})
	}
	router := httpx.NewRouter()
	a.Register(router)
	admin := http.NewServeMux()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Scheduled delta sync (integration track S4, decision B5) runs in this process on every replica; a due binding is
	// taken by one conditional UPDATE. It needs the same wiring live runs need, so it is off without it.
	sched := newScheduler(getenv, st, a, targets.Configured())
	if sched != nil {
		go sched.Run(ctx)
	}
	admin.Handle("/metrics", metrics(a.Metrics(), sched))
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

// newScheduler builds the delta-sync scheduler, or nil when it is off: MIG_SYNC_ENABLED=false, or no target wiring
// (a run could not load anything). MIG_SYNC_INTERVAL (a Go duration, default 30s) is how often a replica looks for
// due bindings.
func newScheduler(getenv func(string) string, st *store.Postgres, a *api.API, wired bool) *syncer.Scheduler {
	if !wired || getenv("MIG_SYNC_ENABLED") == "false" {
		slog.Info("delta sync scheduler is off", "wired", wired)
		return nil
	}
	interval := syncer.DefaultInterval
	if v := getenv("MIG_SYNC_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Second {
			slog.Warn("MIG_SYNC_INTERVAL is not a duration of at least 1s; using the default", "value", v)
		} else {
			interval = d
		}
	}
	return &syncer.Scheduler{Store: st, Runner: a.SyncRunner(), Interval: interval}
}

// metrics serves the API counters and, when the scheduler runs, its own.
func metrics(base http.Handler, s *syncer.Scheduler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base.ServeHTTP(w, r)
		if s == nil {
			return
		}
		claimed, ok, failed, scanErrs := s.Stats()
		for _, m := range []struct {
			name, help string
			value      int64
		}{
			{"migration_sync_claims_total", "Due bindings this replica claimed.", claimed},
			{"migration_sync_runs_succeeded_total", "Delta sync runs that ended succeeded or partial.", ok},
			{"migration_sync_runs_failed_total", "Delta sync runs that failed (watermark kept).", failed},
			{"migration_sync_scan_errors_total", "Scheduler scans that failed.", scanErrs},
		} {
			fmt.Fprintf(w, "# HELP %[1]s %[2]s\n# TYPE %[1]s counter\n%[1]s %[3]d\n", m.name, m.help, m.value)
		}
	})
}
