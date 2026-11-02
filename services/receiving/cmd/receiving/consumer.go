package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/eventing/kafka"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
)

const (
	defaultConsumerGroup = "receiving-ers"
	erasureConsumerGroup = "receiving-erasure"
	deadLetterSuffix     = ".dlq"
)

func kafkaConfig(getenv func(string) string) (kafka.Config, error) {
	return kafka.ConfigFromEnv(getenv, defaultConsumerGroup, "receiving-ers", []string{api.ReceiptPostedTopic})
}

func erasureKafkaConfig(getenv func(string) string) (kafka.Config, error) {
	cfg, err := kafka.ConfigFromEnv(getenv, erasureConsumerGroup, erasureConsumerGroup, []string{api.ErasureRequestedTopic})
	cfg.Group = erasureConsumerGroup
	return cfg, err
}

type runningConsumer struct {
	consumer *eventing.Consumer
	client   *kafka.Client
	name     string
}

func startConsumer(cfg kafka.Config, pool *pgxpool.Pool, name, topic string, handlers map[string]eventing.Handler) (runningConsumer, error) {
	client, err := kafka.New(cfg)
	if err != nil {
		return runningConsumer{}, err
	}
	consumer, err := eventing.NewConsumer(client, client, eventing.NewPgInbox(pool), eventing.Config{
		Name: name, Idempotent: true, Handlers: handlers, DeadLetterTopic: topic + deadLetterSuffix, Log: slog.Default(),
	})
	if err != nil {
		client.Close()
		return runningConsumer{}, err
	}
	return runningConsumer{consumer: consumer, client: client, name: name}, nil
}

func runConsumer(ctx context.Context, getenv func(string) string, a *api.API, pool *pgxpool.Pool) error {
	ersCfg, err := kafkaConfig(getenv)
	if err != nil {
		return err
	}
	erasureCfg, err := erasureKafkaConfig(getenv)
	if err != nil {
		return err
	}
	ers, err := startConsumer(ersCfg, pool, api.ErsConsumerName, api.ReceiptPostedTopic, a.ErsConsumerHandlers())
	if err != nil {
		return err
	}
	defer ers.client.Close()
	erasure, err := startConsumer(erasureCfg, pool, api.ErasureConsumerName, api.ErasureRequestedTopic, a.ErasureConsumerHandlers())
	if err != nil {
		return err
	}
	defer erasure.client.Close()
	running := []runningConsumer{ers, erasure}

	admin := http.NewServeMux()
	admin.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	admin.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		for _, c := range running {
			if err := c.client.Ping(r.Context()); err != nil {
				slog.ErrorContext(r.Context(), "kafka not reachable", "err", err, "consumer", c.name)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	admin.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		a.Metrics().ServeHTTP(w, r)
		for _, c := range running {
			c.consumer.Metrics().Write(w, c.name)
		}
	})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, len(running)+1)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	for _, c := range running {
		go func() { errs <- c.consumer.Run(ctx) }()
	}
	return <-errs
}
