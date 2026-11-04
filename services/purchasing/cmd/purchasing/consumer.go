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
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/api"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
	"github.com/BenjaminSRussell/passage/services/purchasing/migrations"
)

// deadLetterTopic takes the records the consumer cannot handle from either source topic; the origin
// topic, partition and offset travel in the x-origin-* headers, so one topic serves both.
const deadLetterTopic = "passage.procurement.inbound.dlq"

// inboundTopics are the topics the consumer follows: fulfillment's drop-ship events and planning's
// message decisions.
func inboundTopics() []string { return []string{api.DropShipTopic, api.MessageAppliedTopic} }

func kafkaConfig(getenv func(string) string) (kafka.Config, error) {
	return kafka.ConfigFromEnv(getenv, api.InboundConsumerName, api.InboundConsumerName, inboundTopics())
}

// servicePrincipals is the service principal table the start gate reads; tests replace it so they do not
// depend on the seed state of platform/go/tenant.
var servicePrincipals = tenant.ServicePrincipalTable

// inboundSeeded reports whether the service principal table gives the inbound consumer its principal
// with the apply duty. Until the policy bundle and platform/go/tenant seed it (a handoff, see
// NOT-IMPLEMENTED.md), the consumer does not start: it never borrows another consumer's principal.
func inboundSeeded(table map[string][]string) bool {
	return slices.Contains(table[api.InboundConsumerName], api.DutyInboundApply)
}

// runConsume follows fulfillment's dropship.closed.v1 and planning's message.applied.v1 and serves the
// consumer's admin port. It has no HTTP API and no service token verifier.
func runConsume(ctx context.Context, getenv func(string) string) error {
	if !inboundSeeded(servicePrincipals()) {
		return errors.New("inbound consumer not started: service principal " + tenant.ServiceActor(api.InboundConsumerName) +
			" with duty " + api.DutyInboundApply + " is not seeded")
	}
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	cfg, err := kafkaConfig(getenv)
	if err != nil {
		return err
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
	client, err := kafka.New(cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	consumer, err := eventing.NewConsumer(client, client, eventing.NewPgInbox(pool), eventing.Config{
		Name: api.InboundConsumerName, Handlers: api.InboundConsumerHandlers(store.New(pool)),
		DeadLetterTopic: deadLetterTopic, Log: slog.Default(),
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
		consumer.Metrics().Write(w, api.InboundConsumerName)
	})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	go func() {
		err := consumer.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "consumer stopped", "consumer", api.InboundConsumerName, "err", err)
		}
		errs <- err
	}()
	return <-errs
}
