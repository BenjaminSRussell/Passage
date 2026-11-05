package main

import (
	"context"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/production/internal/api"
)

// The consumer runs only as its own principal with the intake duty, never as another consumer's.
func TestPlanningSeeded_NeedsOwnPrincipalWithTheIntakeDuty(t *testing.T) {
	for name, table := range map[string]map[string][]string{
		"absent":         {"shopfloor-production": {"shopfloor.job.sync"}},
		"without duty":   {api.PlanningConsumerName: {"production.order.read"}},
		"duty elsewhere": {"shopfloor-production": {api.DutyPlannedIntake}},
	} {
		if planningSeeded(table) {
			t.Errorf("%s: seeded", name)
		}
	}
	if !planningSeeded(map[string][]string{api.PlanningConsumerName: {api.DutyPlannedIntake}}) {
		t.Error("seeded principal not recognised")
	}
}

// Until the principal is seeded the consumer refuses to start, after reading its database settings;
// once it is, the gate lets it through to the broker settings. The table is supplied, so the test does
// not depend on the seed state of platform/go/tenant.
func TestConsume_NeedsADatabaseAndASeededPrincipal(t *testing.T) {
	if err := consume(context.Background(), func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("consume without a database = %v", err)
	}
	saved := servicePrincipals
	t.Cleanup(func() { servicePrincipals = saved })

	servicePrincipals = func() map[string][]string { return map[string][]string{} }
	if err := runConsumer(context.Background(), func(string) string { return "" }, nil, nil, planningSpec); err == nil || !strings.Contains(err.Error(), "not seeded") {
		t.Fatalf("consumer without a seeded principal = %v", err)
	}
	servicePrincipals = func() map[string][]string {
		return map[string][]string{api.PlanningConsumerName: {api.DutyPlannedIntake}}
	}
	if err := runConsumer(context.Background(), func(string) string { return "" }, nil, nil, planningSpec); err == nil || strings.Contains(err.Error(), "not seeded") || !strings.Contains(err.Error(), "KAFKA_BROKERS") {
		t.Fatalf("seeded consumer without broker settings = %v", err)
	}
}

// The shop-floor consumer has its own principal and duty, and is seeded in the platform table.
func TestShopfloorConsumer_OwnSeededPrincipal(t *testing.T) {
	if !seeded(servicePrincipals(), shopfloorSpec) {
		t.Fatal("service:production-shopfloor is not seeded with production.jobcard.shopfloor")
	}
	if seeded(map[string][]string{api.PlanningConsumerName: {api.DutyShopfloorJobCard}}, shopfloorSpec) {
		t.Fatal("borrowed the planning principal")
	}
	if err := consumeShopfloor(context.Background(), func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("consume-shopfloor without a database = %v", err)
	}
	if err := runConsumer(context.Background(), func(string) string { return "" }, nil, nil, shopfloorSpec); err == nil || !strings.Contains(err.Error(), "KAFKA_BROKERS") {
		t.Fatalf("seeded shop-floor consumer without broker settings = %v", err)
	}
	cfg, err := kafkaConfig(func(k string) string {
		if k == "KAFKA_BROKERS" {
			return "b:9092"
		}
		return ""
	}, shopfloorSpec)
	if err != nil || cfg.Group != api.ShopfloorConsumerName || len(cfg.Topics) != 2 {
		t.Fatalf("kafka config %+v, %v", cfg, err)
	}
}
