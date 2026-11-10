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
	"github.com/BenjaminSRussell/passage/services/revrec/internal/api"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/store"
)

// creditNoteDeadLetterTopic parks the creditnote.booked events the consumer could not apply (the x-origin-* headers name the
// source record, x-dlq-reason why).
const creditNoteDeadLetterTopic = "passage.revrec.creditnotes.dlq"

func creditNoteKafkaConfig(getenv func(string) string) (kafka.Config, error) {
	return kafka.ConfigFromEnv(getenv, api.CreditNoteConsumerName, api.CreditNoteConsumerName, []string{api.BillingInvoiceTopic})
}

// creditNotesSeeded reports whether the service principal table gives the consumer its principal with the reverse duty; the
// consumer never borrows another consumer's principal.
func creditNotesSeeded(table map[string][]string) bool {
	return slices.Contains(table[api.CreditNoteConsumerName], "revrec.creditnote.reverse") &&
		slices.Contains(table[api.CreditNoteConsumerName], "revrec.contract.from-invoice")
}

// consume runs the credit-note consumer (BR-29.6, #29): it reverses the schedules of booked credit notes through the store and
// serves only its admin port. The reversal postings are booked by POST /v1/revrec/journal-postings, so the consumer needs no
// ledger.
func consume(ctx context.Context, getenv func(string) string) error {
	if !creditNotesSeeded(tenant.ServicePrincipalTable()) {
		return errors.New("credit-note consumer not started: service principal " + tenant.ServiceActor(api.CreditNoteConsumerName) + " is not seeded")
	}
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	cfg, err := creditNoteKafkaConfig(getenv)
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
	consumer, err := eventing.NewConsumer(client, client, eventing.NewPgInbox(pool), eventing.Config{
		Name: api.CreditNoteConsumerName, Idempotent: true, Handlers: consumerHandlers(store.New(pool)),
		DeadLetterTopic: creditNoteDeadLetterTopic, Log: slog.Default(),
	})
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
	admin.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		consumer.Metrics().Write(w, api.CreditNoteConsumerName)
	})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	go func() {
		err := consumer.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "consumer stopped", "consumer", api.CreditNoteConsumerName, "err", err)
		}
		errs <- err
	}()
	return <-errs
}

// consumerHandlers serves both billing events of the topic: creditnote.booked reverses schedules (BR-29.6) and invoice.posted
// opens the revenue contract of the invoiced order (29-4).
func consumerHandlers(s *store.Postgres) map[string]eventing.Handler {
	out := api.CreditNoteConsumerHandlers(s)
	for t, h := range api.InvoiceConsumerHandlers(s) {
		out[t] = h
	}
	return out
}
