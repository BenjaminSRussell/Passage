package main

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

func TestCacheConsumerSeeded(t *testing.T) {
	if cacheConsumerSeeded(map[string][]string{"fulfillment-order": {"x"}}) {
		t.Error("seeded without the pricing-cache principal")
	}
	if cacheConsumerSeeded(map[string][]string{store.CacheConsumerName: {"pricing.view"}}) {
		t.Error("seeded with a pricing-cache principal that lacks pricing.cache.invalidate")
	}
	if !cacheConsumerSeeded(map[string][]string{store.CacheConsumerName: {store.DutyCacheInvalidate}}) {
		t.Error("not seeded with the pricing-cache principal")
	}
}

func TestCacheConsumerGroup_OnePerReplica(t *testing.T) {
	a := cacheConsumerGroup(func(string) string { return "pricing-0" })
	b := cacheConsumerGroup(func(string) string { return "pricing-1" })
	if a == b || a != "pricing-cache-pricing-0" {
		t.Errorf("groups = %q and %q; want one group per replica", a, b)
	}
	// KAFKA_GROUP, honoured by other consumers, would put every replica in one group.
	env := func(k string) string {
		return map[string]string{"HOSTNAME": "pricing-2", "KAFKA_GROUP": "shared", "KAFKA_BROKERS": "b:9092"}[k]
	}
	cfg, err := cacheConsumerConfig(env)
	if err != nil || cfg.Group != "pricing-cache-pricing-2" {
		t.Errorf("group with KAFKA_GROUP set = %q (%v); want the replica's own", cfg.Group, err)
	}
}

func TestRunCacheConsumer_NotStartedWithoutBrokersOrPrincipal(t *testing.T) {
	cache := store.NewCache(nil, time.Minute)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runCacheConsumer(context.Background(), func(string) string { return "" }, cache)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the consumer ran without KAFKA_BROKERS")
	}
	original := servicePrincipals
	servicePrincipals = func() map[string][]string { return map[string][]string{} }
	defer func() { servicePrincipals = original }()
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		runCacheConsumer(context.Background(), func(k string) string {
			if k == "KAFKA_BROKERS" {
				return "localhost:1"
			}
			return ""
		}, cache)
	}()
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("the consumer ran without its principal")
	}
}
