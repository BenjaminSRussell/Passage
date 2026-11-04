package main

import (
	"bytes"
	"context"
	"encoding/base64"
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
	if src, err := rateSource(func(string) string { return "" }); src != nil || err != nil {
		t.Fatal("no FX_URL must leave valuation without a rate source")
	}
	env := map[string]string{"FX_URL": "http://fx", "FX_RATE_TYPE": "closing", "FX_TIMEOUT": "750ms"}
	if src, err := rateSource(func(k string) string { return env[k] }); src == nil || err != nil {
		t.Fatal("FX_URL must configure the fx client")
	}
	env["PASSAGE_SERVICE_SIGNING_KEY"] = "not-a-seed"
	if _, err := rateSource(func(k string) string { return env[k] }); err == nil {
		t.Fatal("a malformed service signing key must stop startup")
	}
}

func TestLedgerPosterWiring(t *testing.T) {
	if lp, err := ledgerPoster(func(string) string { return "" }); lp != nil || err != nil {
		t.Fatal("no LEDGER_URL must leave payment runs without a ledger")
	}
	if lp, err := ledgerPoster(func(k string) string { return map[string]string{"LEDGER_URL": "http://ledger"}[k] }); lp != nil || err == nil {
		t.Fatalf("a ledger without a signing key must not become a poster: %v %v", lp, err)
	}
	seed := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32))
	env := map[string]string{"LEDGER_URL": "http://ledger", "PASSAGE_SERVICE_NAME": "payables", "PASSAGE_SERVICE_SIGNING_KEY": seed}
	if lp, err := ledgerPoster(func(k string) string { return env[k] }); lp == nil || err != nil {
		t.Fatalf("a signed ledger client is the poster: %v %v", lp, err)
	}
}

func TestVendorBanksAndWithholdingNeedASignedServiceClient(t *testing.T) {
	seed := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32))
	signed := func(url string) func(string) string {
		env := map[string]string{url: "http://svc", "PASSAGE_SERVICE_NAME": "payables", "PASSAGE_SERVICE_SIGNING_KEY": seed}
		return func(k string) string { return env[k] }
	}
	unsigned := func(url string) func(string) string {
		return func(k string) string { return map[string]string{url: "http://svc"}[k] }
	}
	if vb, err := vendorBanks(func(string) string { return "" }); vb != nil || err != nil {
		t.Fatal("no PARTY_URL must leave file generation without bank details")
	}
	if vb, err := vendorBanks(unsigned("PARTY_URL")); vb != nil || err == nil {
		t.Fatalf("bank details are never revealed with forwarded user headers: %v %v", vb, err)
	}
	if vb, err := vendorBanks(signed("PARTY_URL")); vb == nil || err != nil {
		t.Fatalf("a signed party client reveals bank details: %v %v", vb, err)
	}
	if wt, err := withholdingTax(func(string) string { return "" }); wt != nil || err != nil {
		t.Fatal("no TAX_URL must leave withholding disabled")
	}
	if wt, err := withholdingTax(unsigned("TAX_URL")); wt != nil || err == nil {
		t.Fatalf("an unsigned tax client must not be used: %v %v", wt, err)
	}
	if wt, err := withholdingTax(signed("TAX_URL")); wt == nil || err != nil {
		t.Fatalf("a signed tax client calculates withholding: %v %v", wt, err)
	}
}

func TestVendorGroupDimension(t *testing.T) {
	for in, want := range map[string]string{"": "VENDGRP", "-": "", "GROUP_X": "GROUP_X"} {
		got, err := vendorGroupDimension(func(string) string { return in })
		if err != nil || got != want {
			t.Fatalf("AP_VENDOR_GROUP_DIMENSION=%q gave %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := vendorGroupDimension(func(string) string { return "vend grp" }); err == nil {
		t.Fatal("a malformed attribute must stop the service")
	}
}
