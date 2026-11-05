package main

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/api"
)

// The postings consumer starts only when the service principal table gives it
// the transaction-apply duty, and the platform table does.
func TestPostingsSeeded(t *testing.T) {
	if postingsSeeded(map[string][]string{}) || postingsSeeded(map[string][]string{api.PostingsConsumerName: {"other.duty"}}) {
		t.Fatal("unseeded consumer reported seeded")
	}
	if !postingsSeeded(tenant.ServicePrincipalTable()) {
		t.Fatal("platform table does not seed service:" + api.PostingsConsumerName)
	}
	cfg, err := postingsKafkaConfig(func(k string) string { return map[string]string{"KAFKA_BROKERS": "kafka:9092"}[k] })
	if err != nil || cfg.Group != api.PostingsConsumerName || len(cfg.Topics) != 2 || cfg.Topics[0] != api.ReceiptTopic || cfg.Topics[1] != api.OrderProgressTopic {
		t.Fatalf("kafka config %+v %v", cfg, err)
	}
}
