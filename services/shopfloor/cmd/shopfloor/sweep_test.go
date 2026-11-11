package main

import (
	"context"
	"strings"
	"testing"
)

func TestSweepConfig_ReadsEnvironment(t *testing.T) {
	tenant := "0195f0a0-0000-7000-8000-0000000000a1"
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cfg, err := sweepConfig(env(map[string]string{"SWEEP_TENANT_IDS": tenant, "SWEEP_ROLLUP_INTERVAL": "0", "SWEEP_ANDON_INTERVAL": "30s", "SWEEP_AUTO_CLOCK_AFTER_HOURS": "12"}))
	if err != nil || cfg.RollupEvery != 0 || cfg.AndonEvery.Seconds() != 30 || cfg.AutoClockAfterHour != 12 || cfg.RetryEvery.Minutes() != 1 {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
	for name, m := range map[string]map[string]string{
		"no tenants":   {},
		"bad tenant":   {"SWEEP_TENANT_IDS": "x"},
		"bad interval": {"SWEEP_TENANT_IDS": tenant, "SWEEP_RETRY_INTERVAL": "soon"},
		"too fast":     {"SWEEP_TENANT_IDS": tenant, "SWEEP_RETRY_INTERVAL": "1s"},
		"bad hours":    {"SWEEP_TENANT_IDS": tenant, "SWEEP_AUTO_CLOCK_AFTER_HOURS": "99"},
	} {
		if _, err := sweepConfig(env(m)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// "sweep" fails on its settings before it touches the database.
	if err := run(context.Background(), func(string) string { return "" }, []string{"shopfloor", "sweep"}); err == nil || !strings.Contains(err.Error(), "no tenants") {
		t.Fatalf("sweep without tenants = %v", err)
	}
}

func TestSweepConfig_TenantRegistrySource(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	operator := "0195f0a0-0000-7000-8000-000000000001"
	key := "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=" // a 32-byte seed, base64
	ok := map[string]string{"SWEEP_TENANT_SOURCE": "identity", "IDENTITY_URL": "http://identity", "SWEEP_OPERATOR_TENANT_ID": operator,
		"PASSAGE_SERVICE_NAME": "shopfloor", "PASSAGE_SERVICE_SIGNING_KEY": key}
	cfg, err := sweepConfig(env(ok))
	if err != nil || cfg.Registry == nil || len(cfg.TenantIDs) != 0 {
		t.Fatalf("registry source: %+v, %v", cfg, err)
	}
	for name, drop := range map[string]string{"no identity url": "IDENTITY_URL", "no operator": "SWEEP_OPERATOR_TENANT_ID", "no signing key": "PASSAGE_SERVICE_SIGNING_KEY"} {
		m := map[string]string{}
		for k, v := range ok {
			if k != drop {
				m[k] = v
			}
		}
		if _, err := sweepConfig(env(m)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := sweepConfig(env(map[string]string{"SWEEP_TENANT_SOURCE": "directory", "SWEEP_TENANT_IDS": operator})); err == nil {
		t.Error("an unknown source was accepted")
	}
}
