package main

import (
	"context"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/purchasing/internal/api"
)

// The consumer starts only when the service principal table gives it the apply duty.
func TestInboundSeeded(t *testing.T) {
	if inboundSeeded(map[string][]string{}) || inboundSeeded(map[string][]string{api.InboundConsumerName: {"other.duty"}}) {
		t.Fatal("unseeded consumer reported seeded")
	}
	if !inboundSeeded(map[string][]string{api.InboundConsumerName: {api.DutyInboundApply}}) {
		t.Fatal("seeded consumer reported unseeded")
	}
}

// Until the principal is seeded (a handoff in NOT-IMPLEMENTED.md) the consumer refuses to start and says why;
// once it is, the gate lets it through to its database settings. The table is supplied, so the test does not
// depend on the seed state of platform/go/tenant.
func TestRunConsume_StopsUntilSeeded(t *testing.T) {
	saved := servicePrincipals
	t.Cleanup(func() { servicePrincipals = saved })

	servicePrincipals = func() map[string][]string { return map[string][]string{} }
	err := runConsume(context.Background(), func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "service:purchasing-inbound") || !strings.Contains(err.Error(), "not seeded") {
		t.Fatalf("runConsume without the principal = %v", err)
	}
	servicePrincipals = func() map[string][]string {
		return map[string][]string{api.InboundConsumerName: {api.DutyInboundApply}}
	}
	err = runConsume(context.Background(), func(string) string { return "" })
	if err == nil || strings.Contains(err.Error(), "not seeded") || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("seeded consumer without a database = %v", err)
	}
}

func TestKafkaConfig_FollowsFulfillmentAndPlanning(t *testing.T) {
	env := map[string]string{"KAFKA_BROKERS": "k1:9092"}
	cfg, err := kafkaConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Group != api.InboundConsumerName || len(cfg.Topics) != 2 || cfg.Topics[0] != "passage.fulfillment.dropship" || cfg.Topics[1] != "passage.planning.plan" {
		t.Fatalf("config = %+v", cfg)
	}
}
