package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/eventing/kafka"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/consumer"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/ledgerlines"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

const (
	// consumerGroup is the Kafka group (PRD job reporting-balance-consumer);
	// the inbox and the service principal are consumer.Name.
	consumerGroup = "reporting-balances"
	// ledgerJournalTopic is the ledger's journal channel (ledger asyncapi).
	ledgerJournalTopic = "passage.ledger.journal"
	deadLetterSuffix   = ".dlq"
	defaultAdminAddr   = ":9090"
)

func kafkaConfig(getenv func(string) string) (kafka.Config, error) {
	return kafka.ConfigFromEnv(getenv, consumerGroup, "finreport-consumer", []string{ledgerJournalTopic})
}

// consumerHandlers maps the event types this service folds into the read model.
// The platform consumer runs the handler with the service principal in the
// context; the read model writes in its own transaction, so tx is not used.
func consumerHandlers(ap consumer.Applier, src consumer.LineSource) map[string]eventing.Handler {
	return map[string]eventing.Handler{
		consumer.JournalPostedType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			return consumer.HandleJournalPosted(ctx, ap, src, evt)
		}),
	}
}

// ledgerSource builds the ledger lines client from LEDGER_URL and the service
// signing key (PASSAGE_SERVICE_NAME=finreport, PASSAGE_SERVICE_SIGNING_KEY_FILE).
func ledgerSource(getenv func(string) string) (*ledgerlines.Client, error) {
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	return ledgerlines.New(getenv("LEDGER_URL"), signer, nil)
}

// servicePrincipals is the service principal table the start gate reads; tests
// replace it so they do not depend on what platform/go/tenant has seeded.
var servicePrincipals = tenant.ServicePrincipalTable

// ledgerSeeded reports whether the service principal table gives the consumer
// its principal with the ledger lookup duty. Until platform/go/tenant and the
// policy bundle seed it (handoff, NOT-IMPLEMENTED.md) the consumer does not
// start: it never borrows another consumer's principal.
func ledgerSeeded(table map[string][]string) bool {
	return slices.Contains(table[consumer.Name], ledgerlines.Duty)
}

// runConsumer is the "consume" command: it folds journal.posted events into the
// read model and serves /healthz, /readyz and /metrics on ADMIN_ADDR.
func runConsumer(ctx context.Context, getenv func(string) string, pool *pgxpool.Pool) error {
	if !ledgerSeeded(servicePrincipals()) {
		return errors.New("ledger journal consumer not started: service principal " + tenant.ServiceActor(consumer.Name) +
			" with duty " + ledgerlines.Duty + " is not seeded")
	}
	cfg, err := kafkaConfig(getenv)
	if err != nil {
		return err
	}
	src, err := ledgerSource(getenv)
	if err != nil {
		return err
	}
	client, err := kafka.New(cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	c, err := eventing.NewConsumer(client, client, eventing.NewPgInbox(pool), eventing.Config{
		Name: consumer.Name, Idempotent: true, Handlers: consumerHandlers(store.New(pool), src),
		DeadLetterTopic: ledgerJournalTopic + deadLetterSuffix, Log: slog.Default(),
	})
	if err != nil {
		return fmt.Errorf("start consumer: %w", err)
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
	admin.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) { c.Metrics().Write(w, consumer.Name) })

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	adminAddr := getenv("ADMIN_ADDR")
	if adminAddr == "" {
		adminAddr = defaultAdminAddr
	}
	go func() { errs <- httpx.Serve(ctx, adminAddr, admin) }()
	go func() {
		err := c.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "consumer stopped", "consumer", consumer.Name, "err", err)
		}
		errs <- err
	}()
	err = <-errs
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
