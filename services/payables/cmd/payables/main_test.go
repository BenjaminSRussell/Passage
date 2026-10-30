package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"missing database url", map[string]string{}, "DATABASE_URL is required"},
		{"unparseable database url", map[string]string{"DATABASE_URL": "://bad"}, "connect database"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := run(context.Background(), func(k string) string { return tc.env[k] })
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoggerEmitsKernelLogFields(t *testing.T) {
	var out bytes.Buffer
	newLogger(&out).Info("started", "correlationId", "req-1")
	var line map[string]any
	if err := json.Unmarshal(out.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"timestamp", "level", "message", "service", "correlationId"} {
		if _, ok := line[key]; !ok {
			t.Errorf("log line missing %q: %s", key, out.String())
		}
	}
}

func TestLedgerBalancesWiring(t *testing.T) {
	if lb, err := ledgerBalances(func(string) string { return "" }); lb != nil || err != nil {
		t.Fatal("no LEDGER_URL must leave reconciliation without a ledger")
	}
	if lb, err := ledgerBalances(func(k string) string { return map[string]string{"LEDGER_URL": "http://ledger"}[k] }); lb == nil || err != nil {
		t.Fatal("LEDGER_URL must configure the ledger client")
	}
	if _, err := ledgerBalances(func(k string) string {
		return map[string]string{"LEDGER_URL": "http://ledger", "PASSAGE_SERVICE_SIGNING_KEY": "not-a-seed"}[k]
	}); err == nil {
		t.Fatal("a malformed service signing key must stop startup")
	}
}

func TestLedgerBalancesTimeoutOverride(t *testing.T) {
	if lb, _ := ledgerBalances(func(k string) string {
		return map[string]string{"LEDGER_URL": "http://ledger", "LEDGER_TIMEOUT": "750ms"}[k]
	}); lb == nil {
		t.Fatal("LEDGER_TIMEOUT must not disable the ledger client")
	}
}

func TestRateSourceWiring(t *testing.T) {
	if rateSource(func(string) string { return "" }) != nil {
		t.Fatal("no FX_URL must leave valuation without a rate source")
	}
	env := map[string]string{"FX_URL": "http://fx", "FX_RATE_TYPE": "closing", "FX_TIMEOUT": "750ms"}
	if rateSource(func(k string) string { return env[k] }) == nil {
		t.Fatal("FX_URL must configure the fx client")
	}
}
