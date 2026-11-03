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
	if err := runConsumer(context.Background(), func(string) string { return "" }, nil, nil); err == nil || !strings.Contains(err.Error(), "not seeded") {
		t.Fatalf("consumer without a seeded principal = %v", err)
	}
	servicePrincipals = func() map[string][]string {
		return map[string][]string{api.PlanningConsumerName: {api.DutyPlannedIntake}}
	}
	if err := runConsumer(context.Background(), func(string) string { return "" }, nil, nil); err == nil || strings.Contains(err.Error(), "not seeded") || !strings.Contains(err.Error(), "KAFKA_BROKERS") {
		t.Fatalf("seeded consumer without broker settings = %v", err)
	}
}
