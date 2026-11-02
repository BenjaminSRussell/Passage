package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKafkaConfig(t *testing.T) {
	dir := t.TempDir()
	passwordFile := filepath.Join(dir, "password")
	if err := os.WriteFile(passwordFile, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	badCA := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(badCA, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	cfg, err := kafkaConfig(env(map[string]string{"KAFKA_BROKERS": "a:9093, b:9093", "KAFKA_USERNAME": "receiving-consumer", "KAFKA_PASSWORD_FILE": passwordFile}))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Brokers) != 2 || cfg.Password != "s3cret" || cfg.Group != "receiving-ers" || len(cfg.Topics) != 1 || cfg.Topics[0] != "passage.receiving.receipt" {
		t.Fatalf("config = %+v", cfg)
	}

	refusals := map[string]map[string]string{
		"no brokers":        {},
		"user without pass": {"KAFKA_BROKERS": "a:9093", "KAFKA_USERNAME": "u"},
		"missing pass file": {"KAFKA_BROKERS": "a:9093", "KAFKA_PASSWORD_FILE": filepath.Join(dir, "nope")},
		"bad ca":            {"KAFKA_BROKERS": "a:9093", "KAFKA_TLS_CA_FILE": badCA},
	}
	for name, m := range refusals {
		if _, err := kafkaConfig(env(m)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestErasureKafkaConfigKeepsItsOwnGroupAndTopic(t *testing.T) {
	cfg, err := erasureKafkaConfig(func(k string) string {
		return map[string]string{"KAFKA_BROKERS": "a:9093", "KAFKA_GROUP": "receiving-ers"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Group != "receiving-erasure" || len(cfg.Topics) != 1 || cfg.Topics[0] != "passage.privacy.erasure" {
		t.Fatalf("config = %+v", cfg)
	}
	if _, err := erasureKafkaConfig(func(string) string { return "" }); err == nil {
		t.Fatal("no brokers accepted")
	}
}

func TestRunRefusesUnknownSubcommand(t *testing.T) {
	err := run(context.Background(), func(string) string { return "" }, []string{"receiving", "migrate"})
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("err = %v", err)
	}
}
