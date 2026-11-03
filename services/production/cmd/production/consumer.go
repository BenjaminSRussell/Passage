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
	"github.com/BenjaminSRussell/passage/services/production/internal/api"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

const deadLetterSuffix = ".dlq"

func kafkaConfig(getenv func(string) string) (kafka.Config, error) {
	return kafka.ConfigFromEnv(getenv, api.PlanningConsumerName, api.PlanningConsumerName, []string{api.PlannedOrderFirmedTopic})
}

// planningSeeded reports whether the service principal table gives the production consumer its
// principal with the intake duty. Until the policy bundle and platform/go/tenant seed it (a handoff,
// see NOT-IMPLEMENTED.md), the consumer does not start: it never borrows another consumer's principal.
func planningSeeded(table map[string][]string) bool {
	return slices.Contains(table[api.PlanningConsumerName], api.DutyPlannedIntake)
}

// servicePrincipals is the service principal table the start gate reads; tests replace it so they do
// not depend on what platform/go/tenant has seeded.
var servicePrincipals = tenant.ServicePrincipalTable

// runConsumer follows planning's plannedorder.firmed.v1 and serves the consumer's admin port.
func runConsumer(ctx context.Context, getenv func(string) string, s *store.Postgres, pool *pgxpool.Pool) error {
	if !planningSeeded(servicePrincipals()) {
		return errors.New("planning consumer not started: service principal " + tenant.ServiceActor(api.PlanningConsumerName) +
			" with duty " + api.DutyPlannedIntake + " is not seeded")
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
		Name: api.PlanningConsumerName, Handlers: api.PlanningConsumerHandlers(s),
		DeadLetterTopic: api.PlannedOrderFirmedTopic + deadLetterSuffix, Log: slog.Default(),
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
		consumer.Metrics().Write(w, api.PlanningConsumerName)
	})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	go func() {
		err := consumer.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "consumer stopped", "consumer", api.PlanningConsumerName, "err", err)
		}
		errs <- err
	}()
	return <-errs
}
