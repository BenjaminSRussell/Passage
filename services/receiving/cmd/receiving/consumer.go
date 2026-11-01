package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/eventing/kafka"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
)

const (
	defaultConsumerGroup = "receiving-ers"
	deadLetterSuffix     = ".dlq"
)

func kafkaConfig(getenv func(string) string) (kafka.Config, error) {
	brokers := strings.FieldsFunc(getenv("KAFKA_BROKERS"), func(r rune) bool { return r == ',' || r == ' ' })
	if len(brokers) == 0 {
		return kafka.Config{}, errors.New("KAFKA_BROKERS is required")
	}
	cfg := kafka.Config{
		Brokers: brokers, Group: addrOr(getenv("KAFKA_GROUP"), defaultConsumerGroup), Topics: []string{api.ReceiptPostedTopic},
		ClientID: "receiving-ers", Username: getenv("KAFKA_USERNAME"), Password: getenv("KAFKA_PASSWORD"),
	}
	if file := getenv("KAFKA_PASSWORD_FILE"); file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return kafka.Config{}, fmt.Errorf("read KAFKA_PASSWORD_FILE: %w", err)
		}
		cfg.Password = strings.TrimSpace(string(raw))
	}
	if cfg.Username != "" && cfg.Password == "" {
		return kafka.Config{}, errors.New("KAFKA_USERNAME needs KAFKA_PASSWORD or KAFKA_PASSWORD_FILE")
	}
	if ca := getenv("KAFKA_TLS_CA_FILE"); ca != "" {
		pem, err := os.ReadFile(ca)
		if err != nil {
			return kafka.Config{}, fmt.Errorf("read KAFKA_TLS_CA_FILE: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return kafka.Config{}, errors.New("KAFKA_TLS_CA_FILE holds no certificate")
		}
		cfg.TLS = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	return cfg, nil
}

func addrOr(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func runConsumer(ctx context.Context, getenv func(string) string, a *api.API, pool *pgxpool.Pool) error {
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
		Name: api.ErsConsumerName, Handlers: a.ErsConsumerHandlers(), DeadLetterTopic: api.ReceiptPostedTopic + deadLetterSuffix, Log: slog.Default(),
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
	admin.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		a.Metrics().ServeHTTP(w, r)
		consumer.Metrics().Write(w, api.ErsConsumerName)
	})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	go func() { errs <- consumer.Run(ctx) }()
	return <-errs
}
