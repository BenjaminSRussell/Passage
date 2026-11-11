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

	"github.com/BenjaminSRussell/passage/services/edi/internal/api"
	"github.com/BenjaminSRussell/passage/services/edi/internal/autosend"
	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/sources"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

// consumerSeeded reports whether the service principal table gives the consumer its principal with the one outbound duty; the
// consumer never borrows another principal.
func consumerSeeded(table map[string][]string) bool {
	return slices.Contains(table[autosend.ConsumerName], autosend.Duty)
}

func consumerConfig(h map[string]eventing.Handler) eventing.Config {
	return eventing.Config{Name: autosend.ConsumerName, Idempotent: true, Handlers: h, DeadLetterTopic: autosend.DeadLetterTopic, Log: slog.Default()}
}

// consume runs the outbound consumer (#35, 35-1): it sends the 856, DESADV, 810 or INVOIC of fulfillment's asn.issued and billing's
// invoice.posted to the partners that asked for them, and serves only its admin port.
func consume(ctx context.Context, getenv func(string) string) error {
	if !consumerSeeded(tenant.ServicePrincipalTable()) {
		return errors.New("outbound consumer not started: service principal " + tenant.ServiceActor(autosend.ConsumerName) + " is not seeded")
	}
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return err
	}
	fulfillmentURL, billingURL, salesURL := getenv("EDI_FULFILLMENT_URL"), getenv("EDI_BILLING_URL"), getenv("EDI_SALES_URL")
	if signer == nil || fulfillmentURL == "" || billingURL == "" || salesURL == "" {
		return errors.New("outbound consumer needs PASSAGE_SERVICE_SIGNING_KEY and EDI_FULFILLMENT_URL, EDI_BILLING_URL and EDI_SALES_URL")
	}
	kcfg, err := kafka.ConfigFromEnv(getenv, autosend.ConsumerName, autosend.ConsumerName, []string{autosend.FulfillmentTopic, autosend.BillingTopic})
	if err != nil {
		return err
	}
	pool, err := dbx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	st := store.New(pool, 30)
	a := api.New(st)
	a.Sender, a.EdifactSender = api.DefaultSender, api.DefaultEdifactSender
	if q, id := getenv("EDI_ISA_QUALIFIER"), getenv("EDI_ISA_ID"); q != "" || id != "" {
		a.Sender = domain.Party{Qualifier: q, ID: id}
	}
	if id, q := getenv("EDI_EDIFACT_ID"), getenv("EDI_EDIFACT_QUALIFIER"); id != "" || q != "" {
		a.EdifactSender = domain.EdifactParty{ID: id, Qualifier: q}
	}
	a.Sources = sources.New(signer, fulfillmentURL, billingURL, salesURL, nil)
	client, err := kafka.New(kcfg)
	if err != nil {
		return err
	}
	defer client.Close()
	c, err := eventing.NewConsumer(client, client, eventing.NewPgInbox(pool), consumerConfig(autosend.Handlers(st, a)))
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
	admin.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) { c.Metrics().Write(w, autosend.ConsumerName) })

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	adminAddr := getenv("ADMIN_ADDR")
	if adminAddr == "" {
		adminAddr = ":9090"
	}
	go func() { errs <- httpx.Serve(ctx, adminAddr, admin) }()
	go func() {
		err := c.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "consumer stopped", "consumer", autosend.ConsumerName, "err", err)
		}
		errs <- err
	}()
	return <-errs
}
