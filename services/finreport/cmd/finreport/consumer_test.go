package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/consumer"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestKafkaConfigDefaults(t *testing.T) {
	cfg, err := kafkaConfig(env(map[string]string{"KAFKA_BROKERS": "a:9093, b:9093"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Brokers) != 2 || cfg.Group != "reporting-balances" || len(cfg.Topics) != 1 || cfg.Topics[0] != "passage.ledger.journal" {
		t.Fatalf("config = %+v", cfg)
	}
	cfg, err = kafkaConfig(env(map[string]string{"KAFKA_BROKERS": "a:9093", "KAFKA_GROUP": "other"}))
	if err != nil || cfg.Group != "other" {
		t.Fatalf("group override: %+v, %v", cfg, err)
	}
	if _, err := kafkaConfig(env(nil)); err == nil {
		t.Fatal("no brokers accepted")
	}
	if _, err := kafkaConfig(env(map[string]string{"KAFKA_BROKERS": "a:9093", "KAFKA_USERNAME": "u"})); err == nil {
		t.Fatal("user without password accepted")
	}
}

func TestLedgerSourceNeedsURLAndKey(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "signing-key")
	// base64 of 32 zero bytes: a valid Ed25519 seed.
	if err := os.WriteFile(key, []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ledgerSource(env(map[string]string{"LEDGER_URL": "http://ledger:8080"})); err == nil {
		t.Error("no signing key accepted")
	}
	if _, err := ledgerSource(env(map[string]string{"PASSAGE_SERVICE_NAME": "finreport", "PASSAGE_SERVICE_SIGNING_KEY_FILE": key})); err == nil {
		t.Error("no LEDGER_URL accepted")
	}
	if _, err := ledgerSource(env(map[string]string{"LEDGER_URL": "http://ledger:8080", "PASSAGE_SERVICE_NAME": "finreport", "PASSAGE_SERVICE_SIGNING_KEY_FILE": key})); err != nil {
		t.Errorf("complete config refused: %v", err)
	}
}

type recorder struct{ entries []domain.PostedEntry }

func (r *recorder) ApplyEntry(_ context.Context, e domain.PostedEntry) (int, error) {
	r.entries = append(r.entries, e)
	return len(e.Lines), nil
}

type noLines struct{}

func (noLines) EntryLines(context.Context, string, string, string) ([]domain.SourceLine, error) {
	return nil, nil
}

func TestHandlersRouteJournalPostedToTheConsumer(t *testing.T) {
	h := consumerHandlers(&recorder{}, noLines{})
	if len(h) != 1 || h[consumer.JournalPostedType] == nil {
		t.Fatalf("handlers = %v", h)
	}
	// Without the consumer's service principal the handler refuses, permanently.
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: "11111111-1111-4111-8111-111111111111", UserID: "someone"})
	err := h[consumer.JournalPostedType].Handle(ctx, nil, events.Event{Type: consumer.JournalPostedType})
	if err == nil || !strings.Contains(err.Error(), "service principal") {
		t.Fatalf("err = %v", err)
	}
}

// seedPrincipals replaces the service principal table for one test.
func seedPrincipals(t *testing.T, table map[string][]string) {
	t.Helper()
	prev := servicePrincipals
	servicePrincipals = func() map[string][]string { return table }
	t.Cleanup(func() { servicePrincipals = prev })
}

func TestRunConsumerWaitsForItsServicePrincipal(t *testing.T) {
	for name, table := range map[string]map[string][]string{
		"absent":       {"production-planning": {"production.planned.intake"}},
		"wrong duties": {consumer.Name: {"ledger.balance.read"}},
	} {
		seedPrincipals(t, table)
		err := runConsumer(context.Background(), env(map[string]string{"KAFKA_BROKERS": "a:9093"}), nil)
		if err == nil || !strings.Contains(err.Error(), "service:finreport-ledger") || !strings.Contains(err.Error(), "not seeded") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestMain_RunConsumerRequiresKafka(t *testing.T) {
	seedPrincipals(t, map[string][]string{consumer.Name: {"ledger.entry.lookup"}})
	if err := runConsumer(context.Background(), env(nil), nil); err == nil || !strings.Contains(err.Error(), "KAFKA_BROKERS") {
		t.Fatalf("err = %v", err)
	}
}

// The ledger outbox routes journal.posted by its aggregate type to the topic
// events.Topic derives from the event type; the consumer must follow that one.
func TestConsumerFollowsTheJournalPostedTopic(t *testing.T) {
	if got := events.Topic(consumer.JournalPostedType); got != ledgerJournalTopic {
		t.Fatalf("journal.posted lands on %q, the consumer follows %q", got, ledgerJournalTopic)
	}
}
