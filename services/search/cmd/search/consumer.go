package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/eventing/kafka"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/search/internal/index"
	"github.com/BenjaminSRussell/passage/services/search/internal/store"
)

const deadLetterTopic = "passage.search.index.dlq"

const indexDuty = "search.index.write"

func kafkaConfig(getenv func(string) string) (kafka.Config, error) {
	return kafka.ConfigFromEnv(getenv, index.ConsumerName, index.ConsumerName,
		[]string{index.PartyTopic, index.ProductTopic, index.PurchaseOrderTopic})
}

func seeded(table map[string][]string) bool {
	return slices.Contains(table[index.ConsumerName], indexDuty)
}

// consume keeps the search index current from the party, product and purchase
// order events and serves the consumer's admin port.
func consume(ctx context.Context, getenv func(string) string) error {
	if !seeded(tenant.ServicePrincipalTable()) {
		return errors.New("index consumer not started: service principal " + tenant.ServiceActor(index.ConsumerName) + " is not seeded")
	}
	cfg, err := kafkaConfig(getenv)
	if err != nil {
		return err
	}
	url := getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}
	pool, err := dbx.Connect(ctx, url)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()
	return runConsumer(ctx, getenv, cfg, pool)
}

func runConsumer(ctx context.Context, getenv func(string) string, cfg kafka.Config, pool *pgxpool.Pool) error {
	client, err := kafka.New(cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	consumer, err := eventing.NewConsumer(client, client, eventing.NewPgInbox(pool), eventing.Config{
		Name: index.ConsumerName, Idempotent: true, Handlers: index.Handlers(store.New(pool)),
		DeadLetterTopic: deadLetterTopic, Log: slog.Default(),
	})
	if err != nil {
		return err
	}
	admin := http.NewServeMux()
	admin.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	admin.HandleFunc("/readyz", ready(client.Ping))
	admin.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		consumer.Metrics().Write(w, index.ConsumerName)
	})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	go func() {
		err := consumer.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "consumer stopped", "consumer", index.ConsumerName, "err", err)
		}
		errs <- err
	}()
	return <-errs
}
