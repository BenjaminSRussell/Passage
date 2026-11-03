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
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/api"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

const (
	deadLetterSuffix = ".dlq"
	defaultAdminAddr = ":9090"
)

func kafkaConfig(getenv func(string) string) (kafka.Config, error) {
	return kafka.ConfigFromEnv(getenv, api.OrderConsumerName, api.OrderConsumerName, []string{api.OrderConfirmedTopic, api.OrderHoldTopic})
}

// orderSeeded reports whether the service principal table gives the order consumer its principal
// with the sync duty. Until the policy bundle and platform/go/tenant seed it (a handoff, see
// NOT-IMPLEMENTED.md), the consumer does not start: it never borrows another consumer's principal.
func orderSeeded(table map[string][]string) bool {
	return slices.Contains(table[api.OrderConsumerName], api.DutyOrderSync)
}

// runConsumer follows sales' order.confirmed.v1 and orderhold events and serves the consumer's admin port.
func runConsumer(ctx context.Context, getenv func(string) string, s *store.Postgres, pool *pgxpool.Pool) error {
	if !orderSeeded(tenant.ServicePrincipalTable()) {
		return errors.New("order consumer not started: service principal " + tenant.ServiceActor(api.OrderConsumerName) +
			" with duty " + api.DutyOrderSync + " is not seeded")
	}
	cfg, err := kafkaConfig(getenv)
	if err != nil {
		return err
	}
	client, err := kafka.New(cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	consumer, err := eventing.NewConsumer(client, client, eventing.NewPgInbox(pool), eventing.Config{
		Name: api.OrderConsumerName, Handlers: api.OrderConsumerHandlers(s),
		DeadLetterTopic: api.OrderConfirmedTopic + deadLetterSuffix, Log: slog.Default(),
	})
	if err != nil {
		return err
	}
	addr := getenv("ADMIN_ADDR")
	if addr == "" {
		addr = defaultAdminAddr
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
		consumer.Metrics().Write(w, api.OrderConsumerName)
	})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr, admin) }()
	go func() {
		err := consumer.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "consumer stopped", "consumer", api.OrderConsumerName, "err", err)
		}
		errs <- err
	}()
	return <-errs
}
