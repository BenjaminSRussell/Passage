package main

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/api"
)

// The credit-note consumer starts only when the service principal table gives it the reverse duty, and the platform table
// does; it subscribes to billing's invoice topic under its own group.
func TestCreditNotesSeeded(t *testing.T) {
	if creditNotesSeeded(map[string][]string{}) || creditNotesSeeded(map[string][]string{api.CreditNoteConsumerName: {"other.duty"}}) {
		t.Fatal("unseeded consumer reported seeded")
	}
	if !creditNotesSeeded(tenant.ServicePrincipalTable()) {
		t.Fatal("platform table does not seed service:" + api.CreditNoteConsumerName)
	}
	cfg, err := creditNoteKafkaConfig(func(k string) string { return map[string]string{"KAFKA_BROKERS": "kafka:9092"}[k] })
	if err != nil || cfg.Group != api.CreditNoteConsumerName || len(cfg.Topics) != 1 || cfg.Topics[0] != api.BillingInvoiceTopic {
		t.Fatalf("kafka config %+v %v", cfg, err)
	}
	if _, err := creditNoteKafkaConfig(func(string) string { return "" }); err == nil {
		t.Fatal("kafka config without KAFKA_BROKERS accepted")
	}
}
