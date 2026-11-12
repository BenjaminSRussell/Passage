// Command finreport serves the financial reporting API (wave 15).
//
// Configuration is read from the environment (12-factor):
//
//	DATABASE_URL       required, Postgres URL for the passage_reporting database
//	MIGRATE_ON_START   "true" applies migrations at startup (dev only; not in consume mode)
//	LEDGER_URL, PASSAGE_SERVICE_*   balance verification when serving, and the consumer; KAFKA_* consume mode only
//	HTTP_ADDR          listen address, default :8080
//	REPORTING_*        tunables, validated at startup (see internal/config)
//
// `finreport lag-check TENANT_ID...` runs the lag alert once (the chart's CronJob).
// `finreport fire-schedules TENANT_ID...` fires the distribution schedules that are due (the chart's CronJob).
// `finreport sweep-library TENANT_ID...` deletes report library files past retention unless a legal hold keeps them (the chart's CronJob).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/api"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/config"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/ledgerbalances"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/verify"
	"github.com/BenjaminSRussell/passage/services/finreport/migrations"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "finreport"))
	var err error
	switch {
	case len(os.Args) > 1 && os.Args[1] == "consume":
		err = runWith(ctx, os.Getenv, true)
	case len(os.Args) > 1 && os.Args[1] == lagCheckCommand:
		err = lagCheck(ctx, os.Getenv, os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == fireSchedulesCommand:
		err = fireSchedules(ctx, os.Getenv, os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == sweepLibraryCommand:
		err = sweepLibrary(ctx, os.Getenv, os.Args[2:])
	case len(os.Args) > 1:
		err = fmt.Errorf("unknown command %q (use no argument to serve, \"consume\" to run the ledger journal consumer, \"lag-check TENANT_ID...\" to raise lag.exceeded, \"fire-schedules TENANT_ID...\" to fire due distribution schedules or \"sweep-library TENANT_ID...\" to delete report library files past retention)", os.Args[1])
	default:
		err = run(ctx, os.Getenv)
	}
	if err != nil {
		slog.Error("finreport stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string) error { return runWith(ctx, getenv, false) }

// runWith serves the API, or with consume runs the Kafka consumer of the
// ledger journal (the same image, args: [consume]).
func runWith(ctx context.Context, getenv func(string) string, consume bool) error {
	// Reporting tunables are validated before any connection is made, so an
	// out-of-range REPORTING_* value stops the pod at startup (PRD §2.15.13).
	cfg, err := config.FromEnv(getenv)
	if err != nil {
		return err
	}
	slog.Info("reporting configuration", "config", cfg)
	url := getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}
	pool, err := dbx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()
	if getenv("MIGRATE_ON_START") == "true" && !consume {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return err
		}
	}
	if consume {
		return runConsumer(ctx, getenv, pool)
	}
	addr := getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	st := store.New(pool)
	return httpx.ServeWithMetrics(ctx, addr, newHandler(st, cfg, withVerifier(verifier(getenv, st))))
}

// verifier builds the balance verification service when the ledger can be read
// (LEDGER_URL and a service signing key); without them the verification routes
// answer 503 and the rest of the API is unaffected.
func verifier(getenv func(string) string, st *store.Postgres) api.Verifier {
	if getenv("LEDGER_URL") == "" {
		slog.Info("balance verification disabled: LEDGER_URL is not set")
		return nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err == nil {
		var c *ledgerbalances.Client
		if c, err = ledgerbalances.New(getenv("LEDGER_URL"), signer, nil); err == nil {
			return &verify.Service{Store: st, Ledger: c}
		}
	}
	slog.Warn("balance verification disabled", "error", err)
	return nil
}

func withVerifier(v api.Verifier) func(*api.API) {
	return func(a *api.API) {
		if v != nil {
			a.WithVerifier(v)
		}
	}
}

// newHandler mounts the reporting API on the platform router, which installs
// the tenant middleware and the health endpoints, exactly as before slice 19.
// The one public route, the share link download (FR-15.8), never reaches that
// router: every path under api.SharedPathPrefix goes to the API's public
// handler, which serves that download alone (no other path answers there) and
// keeps the token, which is in the path, out of the access log.
func newHandler(st api.Store, cfg config.Reporting, opts ...func(*api.API)) http.Handler {
	router := httpx.NewRouter()
	a := api.New(st).WithConfig(cfg)
	for _, o := range opts {
		o(a)
	}
	a.Register(router)
	public := httpx.FenceMiddleware(a.PublicHandler(httpx.ParseTrustedProxies(os.Getenv(httpx.TrustedProxiesEnv))))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, api.SharedPathPrefix) {
			public.ServeHTTP(w, r)
			return
		}
		router.ServeHTTP(w, r)
	})
}
