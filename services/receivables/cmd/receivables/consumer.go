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
	"github.com/BenjaminSRussell/passage/services/receivables/internal/api"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

// billingDeadLetterTopic parks the invoice.posted and creditnote.posted events the consumer could not apply (the x-origin-*
// headers name the source record, x-dlq-reason why; the plain reason is in the consumer's log).
const billingDeadLetterTopic = "passage.receivables.invoices.dlq"

func billingKafkaConfig(getenv func(string) string) (kafka.Config, error) {
	return kafka.ConfigFromEnv(getenv, api.BillingConsumerName, api.BillingConsumerName, []string{api.BillingInvoiceTopic})
}

// billingSeeded reports whether the service principal table gives the consumer its principal with the open-item duty; the
// consumer never borrows another consumer's principal.
func billingSeeded(table map[string][]string) bool {
	return slices.Contains(table[api.BillingConsumerName], "receivables.openitem.from-billing")
}

func billingConfig(s api.BillingStore) eventing.Config {
	return eventing.Config{
		Name: api.BillingConsumerName, Idempotent: true, Handlers: api.BillingConsumerHandlers(s),
		DeadLetterTopic: billingDeadLetterTopic, Log: slog.Default(),
	}
}

// consume runs the billing consumer (#30): it opens the receivables of billing's posted invoices and credit notes and serves
// only its admin port.
func consume(ctx context.Context, getenv func(string) string) error {
	if !billingSeeded(tenant.ServicePrincipalTable()) {
		return errors.New("billing consumer not started: service principal " + tenant.ServiceActor(api.BillingConsumerName) + " is not seeded")
	}
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	cfg, err := billingKafkaConfig(getenv)
	if err != nil {
		return err
	}
	pool, err := dbx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	client, err := kafka.New(cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	c, err := eventing.NewConsumer(client, client, eventing.NewPgInbox(pool), billingConfig(store.New(pool)))
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
	admin.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) { c.Metrics().Write(w, api.BillingConsumerName) })

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
			slog.ErrorContext(ctx, "consumer stopped", "consumer", api.BillingConsumerName, "err", err)
		}
		errs <- err
	}()
	return <-errs
}
