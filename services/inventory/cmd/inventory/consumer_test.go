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
	if err != nil || cfg.Group != api.PostingsConsumerName || len(cfg.Topics) != 3 || cfg.Topics[0] != api.ReceiptTopic || cfg.Topics[1] != api.OrderProgressTopic || cfg.Topics[2] != api.PackingSlipTopic {
		t.Fatalf("kafka config %+v %v", cfg, err)
	}
}

// Plain lines are converted by the product service once PRODUCT_URL is set,
// which then needs the signing key; without it there is no converter.
func TestPostingsUnits(t *testing.T) {
	if c, err := postingsUnits(func(string) string { return "" }); c != nil || err != nil {
		t.Fatalf("no PRODUCT_URL: %v %v", c, err)
	}
	if _, err := postingsUnits(func(k string) string { return map[string]string{"PRODUCT_URL": "http://product"}[k] }); err == nil {
		t.Fatal("PRODUCT_URL without a signing key accepted")
	}
	env := map[string]string{"PRODUCT_URL": "http://product", "PASSAGE_SERVICE_NAME": "inventory",
		"PASSAGE_SERVICE_SIGNING_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
	if c, err := postingsUnits(func(k string) string { return env[k] }); c == nil || err != nil {
		t.Fatalf("configured: %v %v", c, err)
	}
}
