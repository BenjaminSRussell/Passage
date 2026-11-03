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
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/api"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

const deadLetterSuffix = ".dlq"

func kafkaConfig(getenv func(string) string) (kafka.Config, error) {
	return kafka.ConfigFromEnv(getenv, api.ProductionConsumerName, api.ProductionConsumerName, []string{api.OperationStatusTopic})
}

// productionSeeded reports whether the service principal table gives the production consumer its
// principal with the sync duty. Until the policy bundle and platform/go/tenant seed it (a handoff,
// see NOT-IMPLEMENTED.md), the consumer does not start: it never borrows another consumer's principal.
func productionSeeded(table map[string][]string) bool {
	return slices.Contains(table[api.ProductionConsumerName], api.DutyProductionSync)
}

// runConsumer follows production's operation.statuschanged.v1 and serves the consumer's admin port.
func runConsumer(ctx context.Context, getenv func(string) string, s *store.Postgres, pool *pgxpool.Pool) error {
	if !productionSeeded(tenant.ServicePrincipalTable()) {
		return errors.New("production consumer not started: service principal " + tenant.ServiceActor(api.ProductionConsumerName) +
			" with duty " + api.DutyProductionSync + " is not seeded")
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
		Name: api.ProductionConsumerName, Handlers: api.ProductionConsumerHandlers(s),
		DeadLetterTopic: api.OperationStatusTopic + deadLetterSuffix, Log: slog.Default(),
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
		consumer.Metrics().Write(w, api.ProductionConsumerName)
	})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	go func() {
		err := consumer.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "consumer stopped", "consumer", api.ProductionConsumerName, "err", err)
		}
		errs <- err
	}()
	return <-errs
}
