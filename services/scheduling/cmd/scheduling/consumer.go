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
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/api"
)

const deadLetterSuffix = ".dlq"

// orderGroupEnv overrides the Kafka group of the order intake; KAFKA_GROUP
// stays the downtime consumer's.
const orderGroupEnv = "KAFKA_ORDER_GROUP"

func kafkaConfig(getenv func(string) string) (kafka.Config, error) {
	return kafka.ConfigFromEnv(getenv, api.DowntimeConsumerName, api.DowntimeConsumerName, []string{api.DowntimeRecordedTopic})
}

// orderKafkaConfig is the order intake's own group on passage.production.order,
// with the same brokers and credentials as the downtime consumer.
func orderKafkaConfig(getenv func(string) string) (kafka.Config, error) {
	return kafka.ConfigFromEnv(func(k string) string {
		if k == "KAFKA_GROUP" {
			return getenv(orderGroupEnv)
		}
		return getenv(k)
	}, api.OrderImportConsumerName, api.OrderImportConsumerName, []string{api.OrderScheduledTopic})
}

// orderIntakeSeeded reports whether the service principal table gives the
// order intake its principal with the order import duty. Until the policy
// bundle and platform/go/tenant seed it (a handoff of slice 18), the order
// intake does not start: it never borrows another consumer's principal.
func orderIntakeSeeded(table map[string][]string) bool {
	return slices.Contains(table[api.OrderImportConsumerName], api.DutyOrderImport)
}

type runningConsumer struct {
	name     string
	client   *kafka.Client
	consumer *eventing.Consumer
}

func newConsumer(cfg kafka.Config, inbox eventing.Inbox, name, deadLetterTopic string, handlers map[string]eventing.Handler) (*runningConsumer, error) {
	client, err := kafka.New(cfg)
	if err != nil {
		return nil, err
	}
	consumer, err := eventing.NewConsumer(client, client, inbox, eventing.Config{
		Name: name, Idempotent: true, Handlers: handlers, DeadLetterTopic: deadLetterTopic, Log: slog.Default(),
	})
	if err != nil {
		client.Close()
		return nil, err
	}
	return &runningConsumer{name: name, client: client, consumer: consumer}, nil
}

func runConsumer(ctx context.Context, getenv func(string) string, a *api.API, pool *pgxpool.Pool) error {
	cfg, err := kafkaConfig(getenv)
	if err != nil {
		return err
	}
	inbox := eventing.NewPgInbox(pool)
	downtime, err := newConsumer(cfg, inbox, api.DowntimeConsumerName, api.DowntimeRecordedTopic+deadLetterSuffix, a.DowntimeConsumerHandlers())
	if err != nil {
		return err
	}
	defer downtime.client.Close()
	running := []*runningConsumer{downtime}

	if orderIntakeSeeded(tenant.ServicePrincipalTable()) {
		orderCfg, err := orderKafkaConfig(getenv)
		if err != nil {
			return err
		}
		orders, err := newConsumer(orderCfg, inbox, api.OrderImportConsumerName, api.OrderScheduledTopic+deadLetterSuffix, a.OrderConsumerHandlers())
		if err != nil {
			return err
		}
		defer orders.client.Close()
		running = append(running, orders)
	} else {
		slog.WarnContext(ctx, "production order intake not started: its service principal is not seeded",
			"principal", tenant.ServiceActor(api.OrderImportConsumerName), "duty", api.DutyOrderImport, "topic", api.OrderScheduledTopic)
	}

	admin := http.NewServeMux()
	admin.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	admin.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		for _, c := range running {
			if err := c.client.Ping(r.Context()); err != nil {
				slog.ErrorContext(r.Context(), "kafka not reachable", "consumer", c.name, "err", err)
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
	errs := make(chan error, 1+len(running))
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	for _, c := range running {
		go func() {
			err := c.consumer.Run(ctx)
			if err != nil && !errors.Is(err, context.Canceled) {
				slog.ErrorContext(ctx, "consumer stopped", "consumer", c.name, "err", err)
			}
			errs <- err
		}()
	}
	return <-errs
}
