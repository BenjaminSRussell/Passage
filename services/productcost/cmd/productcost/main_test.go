package main

import (
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestConfig_DefaultsAndOverrides(t *testing.T) {
	cfg, err := config(env(nil))
	if err != nil || cfg.MaxLevels != 20 || cfg.Workers != 4 {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	cfg, err = config(env(map[string]string{"COSTING_MAX_LEVELS": "12", "COSTING_ROLL_WORKERS": "8"}))
	if err != nil || cfg.MaxLevels != 12 || cfg.Workers != 8 {
		t.Fatalf("overrides: %+v %v", cfg, err)
	}
}

func TestLedgerIdempotencyTTLDays_DefaultsToFourHundredAndRejectsLess(t *testing.T) {
	if got, err := ledgerIdempotencyTTLDays(env(nil)); err != nil || got != 400 {
		t.Fatalf("default: %d %v", got, err)
	}
	if got, err := ledgerIdempotencyTTLDays(env(map[string]string{"PRODUCTCOST_LEDGER_IDEMPOTENCY_TTL_DAYS": "500"})); err != nil || got != 500 {
		t.Fatalf("500: %d %v", got, err)
	}
	for _, bad := range []string{"399", "x", "-1", "3651", "9999999999"} {
		if _, err := ledgerIdempotencyTTLDays(env(map[string]string{"PRODUCTCOST_LEDGER_IDEMPOTENCY_TTL_DAYS": bad})); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestConfig_RejectsOutOfRange(t *testing.T) {
	for _, bad := range []map[string]string{
		{"COSTING_MAX_LEVELS": "41"},
		{"COSTING_MAX_LEVELS": "x"},
		{"COSTING_ROLL_WORKERS": "0"},
	} {
		if _, err := config(env(bad)); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
