package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/eventing/kafka"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/payables/internal/iccharge"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
)

// icChargeSeeded reports whether the service principal table gives the consumer its principal with the one flag duty.
func icChargeSeeded(table map[string][]string) bool {
	return slices.Contains(table[iccharge.ConsumerName], iccharge.Duty)
}

// consume runs the intercompany charge consumer (#40): it takes the borrower's vendor invoice of every chargeinvoice.requested into
// the vendor-invoice intake and serves only its admin port.
func consume(ctx context.Context, getenv func(string) string) error {
	if !icChargeSeeded(tenant.ServicePrincipalTable()) {
		return errors.New("ic charge consumer not started: service principal " + tenant.ServiceActor(iccharge.ConsumerName) + " is not seeded")
	}
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	kcfg, err := kafka.ConfigFromEnv(getenv, iccharge.ConsumerName, iccharge.ConsumerName, []string{iccharge.Topic})
	if err != nil {
		return err
	}
	pool, err := dbx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	client, err := kafka.New(kcfg)
	if err != nil {
		return err
	}
	defer client.Close()
	c, err := eventing.NewConsumer(client, client, eventing.NewPgInbox(pool), eventing.Config{Name: iccharge.ConsumerName, Idempotent: true,
		Handlers: iccharge.Handlers(store.New(pool)), DeadLetterTopic: iccharge.DeadLetterTopic, Log: slog.Default()})
	if err != nil {
		return err
	}
	admin := http.NewServeMux()
	admin.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	admin.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := client.Ping(r.Context()); err != nil {
			slog.ErrorContext(r.Context(), "kafka not reachable", "err", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	admin.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) { c.Metrics().Write(w, iccharge.ConsumerName) })

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	adminAddr := getenv("ADMIN_ADDR")
	if adminAddr == "" {
		adminAddr = ":9090"
	}
	go func() { errs <- httpx.Serve(ctx, adminAddr, admin) }()
	go func() {
		err := c.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "consumer stopped", "consumer", iccharge.ConsumerName, "err", err)
		}
		errs <- err
	}()
	return <-errs
}
