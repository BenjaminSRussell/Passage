package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/eventing/kafka"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/api"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// postingsDeadLetterTopic parks the receipt and delivery events the postings
// consumer could not book (both source topics share it; the x-origin-topic
// header names where a record came from).
const postingsDeadLetterTopic = "passage.inventory.postings.dlq"

func postingsKafkaConfig(getenv func(string) string) (kafka.Config, error) {
	return kafka.ConfigFromEnv(getenv, api.PostingsConsumerName, api.PostingsConsumerName, []string{api.ReceiptTopic, api.OrderProgressTopic})
}

// postingsSeeded reports whether the service principal table gives the postings
// consumer its principal with the transaction-apply duty; the consumer never
// borrows another consumer's principal.
func postingsSeeded(table map[string][]string) bool {
	return slices.Contains(table[api.PostingsConsumerName], "inventory.transaction.apply")
}

// runPostingsConsumer books receiving's receipt.posted and sales' DELIVERED
// orderprogress.recorded as inventory transactions (#480) and serves the
// consumer's admin port.
func runPostingsConsumer(ctx context.Context, getenv func(string) string, s *store.Postgres, pool *pgxpool.Pool, neg domain.NegativePolicy) error {
	if !postingsSeeded(tenant.ServicePrincipalTable()) {
		return errors.New("postings consumer not started: service principal " + tenant.ServiceActor(api.PostingsConsumerName) + " is not seeded")
	}
	cfg, err := postingsKafkaConfig(getenv)
	if err != nil {
		return err
	}
	client, err := kafka.New(cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	consumer, err := eventing.NewConsumer(client, client, eventing.NewPgInbox(pool), eventing.Config{
		Name: api.PostingsConsumerName, Idempotent: true, Handlers: api.PostingsConsumerHandlers(s, neg),
		DeadLetterTopic: postingsDeadLetterTopic, Log: slog.Default(),
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
		consumer.Metrics().Write(w, api.PostingsConsumerName)
	})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	go func() {
		err := consumer.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "consumer stopped", "consumer", api.PostingsConsumerName, "err", err)
		}
		errs <- err
	}()
	return <-errs
}
