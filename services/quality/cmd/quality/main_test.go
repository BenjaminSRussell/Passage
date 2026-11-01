package main

import (
	"strings"
	"testing"
)

const testSigningKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestNewStore_InventoryNeedsASignerAndIsOptional(t *testing.T) {
	if _, err := newStore(nil, env(nil)); err != nil {
		t.Fatalf("without INVENTORY_URL the service runs without holds: %v", err)
	}
	if _, err := newStore(nil, env(map[string]string{"INVENTORY_URL": "http://inventory:8080", "PASSAGE_SERVICE_SIGNING_KEY": testSigningKey})); err == nil || !strings.Contains(err.Error(), "QUALITY_HOLD_TENANTS") {
		t.Fatalf("INVENTORY_URL without tenants must stop the service: %v", err)
	}
	if _, err := newStore(nil, env(map[string]string{"INVENTORY_URL": "http://inventory:8080", "QUALITY_HOLD_TENANTS": "t1"})); err == nil || !strings.Contains(err.Error(), "PASSAGE_SERVICE_SIGNING_KEY") {
		t.Fatalf("INVENTORY_URL without a signing key must stop the service: %v", err)
	}
	if _, err := newStore(nil, env(map[string]string{"INVENTORY_URL": "http://inventory:8080", "QUALITY_HOLD_TENANTS": "t1", "PASSAGE_SERVICE_SIGNING_KEY": "not-a-seed"})); err == nil {
		t.Fatal("a malformed signing key must stop the service")
	}
	linked := env(map[string]string{"INVENTORY_URL": "http://inventory:8080", "QUALITY_HOLD_TENANTS": "t1", "PASSAGE_SERVICE_NAME": "quality", "PASSAGE_SERVICE_SIGNING_KEY": testSigningKey})
	if _, err := newStore(nil, linked); err != nil {
		t.Fatalf("URL and key: %v", err)
	}
}
