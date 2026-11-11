package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/api"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
	"github.com/BenjaminSRussell/passage/services/engineering/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
	devEnvironment   = "dev"
	auditRelayEvery  = 5 * time.Second
)

var errApproverNotConfigured = errors.New("no ECO approval workflow is configured")

type failClosedApprover struct{}

func (failClosedApprover) Decide(context.Context, string) (domain.Decision, error) {
	return domain.Decision{}, errApproverNotConfigured
}

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("engineering stopped", "err", err)
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
	return slog.New(handler).With("service", "engineering")
}

func run(ctx context.Context, getenv func(string) string) error {
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	dev := getenv("PASSAGE_ENV") == devEnvironment
	auditURL := getenv("AUDIT_URL")
	if auditURL == "" && !dev {
		return errors.New("AUDIT_URL is required unless PASSAGE_ENV=dev: signatures must reach the audit service")
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
	slog.WarnContext(ctx, "fail-closed ECO approver bound, :release returns 503 until a workflow approver is wired", "err", errApproverNotConfigured)
	st := store.New(pool)
	a := api.New(st, failClosedApprover{}, time.Now)
	var auditor api.Auditor = api.NopAuditor{}
	if auditURL != "" {
		signer, err := tenant.ServiceSignerFromEnv(getenv)
		if err != nil {
			return fmt.Errorf("audit signer: %w", err)
		}
		if signer == nil {
			return errors.New("audit signer: PASSAGE_SERVICE_SIGNING_KEY is required when AUDIT_URL is set")
		}
		auditor = api.NewHTTPAuditor(auditURL, signer)
	}
	if dev {
		slog.WarnContext(ctx, "development re-authentication bound: document signatures accept the acting user without a step-up proof")
		a.WithReauth(api.DevReauth{})
	} else {
		slog.WarnContext(ctx, "fail-closed re-authentication bound, document signing returns 503 until a step-up verifier is wired")
	}
	router := httpx.NewRouter()
	a.Register(router)
	admin := http.NewServeMux()
	admin.Handle("/metrics", httpx.WithMetrics(a.Metrics()))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go api.NewAuditRelay(st, auditor).Run(ctx, auditRelayEvery)
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
