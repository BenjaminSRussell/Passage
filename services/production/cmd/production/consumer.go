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

// consumerSpec is one Kafka consumer the production binary runs: its name (Kafka group and
// service principal), the duty that principal must hold, its topics and handlers.
type consumerSpec struct {
	name, duty      string
	topics          []string
	deadLetterTopic string
	handlers        func(*store.Postgres) map[string]eventing.Handler
}

// planningSpec follows planning's plannedorder.firmed.v1 into the planned order intake (FR-22.10).
var planningSpec = consumerSpec{
	name: api.PlanningConsumerName, duty: api.DutyPlannedIntake,
	topics: []string{api.PlannedOrderFirmedTopic}, deadLetterTopic: api.PlannedOrderFirmedTopic + deadLetterSuffix,
	handlers: func(s *store.Postgres) map[string]eventing.Handler { return api.PlanningConsumerHandlers(s) },
}

// shopfloorSpec follows the shop floor's registration.posted and .corrected into job cards (AC-25.1).
var shopfloorSpec = consumerSpec{
	name: api.ShopfloorConsumerName, duty: api.DutyShopfloorJobCard,
	topics: []string{api.RegistrationPostedTopic, api.RegistrationCorrectedTopic}, deadLetterTopic: api.RegistrationPostedTopic + deadLetterSuffix,
	handlers: func(s *store.Postgres) map[string]eventing.Handler { return api.ShopfloorConsumerHandlers(s) },
}

func kafkaConfig(getenv func(string) string, spec consumerSpec) (kafka.Config, error) {
	return kafka.ConfigFromEnv(getenv, spec.name, spec.name, spec.topics)
}

// seeded reports whether the service principal table gives the consumer its own principal with
// its duty. Until the policy bundle and platform/go/tenant seed it (see NOT-IMPLEMENTED.md), the
// consumer does not start: it never borrows another consumer's principal.
func seeded(table map[string][]string, spec consumerSpec) bool {
	return slices.Contains(table[spec.name], spec.duty)
}

func planningSeeded(table map[string][]string) bool { return seeded(table, planningSpec) }

// servicePrincipals is the service principal table the start gate reads; tests replace it so they do
// not depend on what platform/go/tenant has seeded.
var servicePrincipals = tenant.ServicePrincipalTable

// runConsumer runs one consumer and serves its admin port.
func runConsumer(ctx context.Context, getenv func(string) string, s *store.Postgres, pool *pgxpool.Pool, spec consumerSpec) error {
	if !seeded(servicePrincipals(), spec) {
		return errors.New(spec.name + " consumer not started: service principal " + tenant.ServiceActor(spec.name) +
			" with duty " + spec.duty + " is not seeded")
	}
	cfg, err := kafkaConfig(getenv, spec)
	if err != nil {
		return err
	}
	client, err := kafka.New(cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	consumer, err := eventing.NewConsumer(client, client, eventing.NewPgInbox(pool), eventing.Config{
		Name: spec.name, Handlers: spec.handlers(s),
		DeadLetterTopic: spec.deadLetterTopic, Log: slog.Default(),
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
		consumer.Metrics().Write(w, spec.name)
	})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	go func() {
		err := consumer.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "consumer stopped", "consumer", spec.name, "err", err)
		}
		errs <- err
	}()
	return <-errs
}
