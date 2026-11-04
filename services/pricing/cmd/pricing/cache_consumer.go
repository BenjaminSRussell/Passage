package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"slices"
	"time"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/eventing/kafka"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const deadLetterTopic = "passage.pricing.cache.dlq"

// cacheConsumerBackoff bounds how long a replica waits before it reconnects its cache consumer
// after a poll or connection failure.
const (
	cacheConsumerMinBackoff = 5 * time.Second
	cacheConsumerMaxBackoff = 2 * time.Minute
)

// servicePrincipals is the service principal table the start gate reads; tests replace it so
// they do not depend on what platform/go/tenant has seeded.
var servicePrincipals = tenant.ServicePrincipalTable

// cacheConsumerSeeded reports whether the service principal table gives the cache consumer its
// principal with the flag duty pricing.cache.invalidate. Until platform/go/tenant and the policy
// bundle seed it (a handoff, see NOT-IMPLEMENTED.md) the consumer does not start and the replica
// keeps its TTL-bounded staleness: it never borrows another consumer's principal.
func cacheConsumerSeeded(table map[string][]string) bool {
	return slices.Contains(table[store.CacheConsumerName], store.DutyCacheInvalidate)
}

// cacheConsumerGroup is the Kafka group of this replica, pricing-cache-<pod>. Every replica needs
// every event, so each reads under a group of its own; KAFKA_GROUP is deliberately not honoured,
// since one shared group would hand each event to a single replica. A pod with no name gets a
// random suffix.
func cacheConsumerGroup(getenv func(string) string) string {
	host := getenv("HOSTNAME")
	if host == "" {
		host, _ = os.Hostname()
	}
	if host == "" {
		host = ids.New()
	}
	return store.CacheConsumerName + "-" + host
}

// runCacheConsumer follows campaign.killed, campaign.restored, parameters.changed and
// tenantparameter.changed so this replica's caches follow the other replicas' changes within the
// event lag instead of the snapshot lifetime (FR-26.14, FR-26.10). It runs when KAFKA_BROKERS
// is set and returns when ctx ends. A failure is logged and never stops the replica: the
// consumer reconnects with backoff, and the TTL bounds staleness meanwhile.
func runCacheConsumer(ctx context.Context, getenv func(string) string, cache *store.Cache) {
	if getenv("KAFKA_BROKERS") == "" {
		return
	}
	if !cacheConsumerSeeded(servicePrincipals()) {
		slog.Warn("cache invalidation consumer not started: service principal " + tenant.ServiceActor(store.CacheConsumerName) +
			" with duty " + store.DutyCacheInvalidate + " is not seeded")
		return
	}
	cfg, err := cacheConsumerConfig(getenv)
	if err != nil {
		slog.Error("cache invalidation consumer not started", "err", err)
		return
	}
	handlers := cache.InvalidationHandlers(time.Now())
	backoff := cacheConsumerMinBackoff
	for {
		err := consumeOnce(ctx, cfg, handlers)
		if ctx.Err() != nil {
			return
		}
		slog.Error("cache invalidation consumer stopped; reconnecting", "err", err, "group", cfg.Group, "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, cacheConsumerMaxBackoff)
	}
}

// cacheConsumerConfig is the Kafka configuration of the replica's cache consumer: the platform's
// KAFKA_* settings under the replica's own group, whatever KAFKA_GROUP says.
func cacheConsumerConfig(getenv func(string) string) (kafka.Config, error) {
	env := func(k string) string {
		if k == "KAFKA_GROUP" {
			return ""
		}
		return getenv(k)
	}
	return kafka.ConfigFromEnv(env, cacheConsumerGroup(getenv), store.CacheConsumerName, store.CacheTopics)
}

func consumeOnce(ctx context.Context, cfg kafka.Config, handlers map[string]eventing.Handler) error {
	client, err := kafka.New(cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	c, err := eventing.NewConsumer(client, client, store.MemoryInbox{}, eventing.Config{
		Name: store.CacheConsumerName, Idempotent: true, Handlers: handlers,
		DeadLetterTopic: deadLetterTopic, Log: slog.Default(),
	})
	if err != nil {
		return err
	}
	if err := c.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
