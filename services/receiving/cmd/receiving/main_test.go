package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/ledger"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/periods"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/purchasing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"missing database url", map[string]string{}, "DATABASE_URL is required"},
		{"unparseable database url", map[string]string{"DATABASE_URL": "://bad", "PASSAGE_ALLOW_FAKES": "1"}, "connect database"},
		{"real adapters required without the fakes flag", map[string]string{"DATABASE_URL": "://bad"}, "LEDGER_URL is required"},
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

func TestAddrFallsBackToDefault(t *testing.T) {
	none := func(string) string { return "" }
	if got := addr(none, "HTTP_ADDR", defaultHTTPAddr); got != ":8080" {
		t.Fatalf("got %s", got)
	}
	set := func(string) string { return ":9999" }
	if got := addr(set, "HTTP_ADDR", defaultHTTPAddr); got != ":9999" {
		t.Fatalf("got %s", got)
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

func TestNotImplementedListsEveryDeliverable(t *testing.T) {
	doc, err := os.ReadFile("../../NOT-IMPLEMENTED.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Deliverable 2", "Deliverable 3", "Deliverable 4", "Deliverable 5", "Deliverable 6", "Deliverable 7", "Deliverable 8"} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("NOT-IMPLEMENTED.md has no row for %s", want)
		}
	}
}

func withAccounts(env map[string]string) map[string]string {
	env["POSTING_LEDGER_ID"] = "0a1b2c3d-0000-4000-8000-000000000001"
	env["POSTING_ACCOUNT_INVENTORY"], env["POSTING_ACCOUNT_GRIR"], env["POSTING_ACCOUNT_PPV"], env["POSTING_ACCOUNT_VENDOR_CONTROL"] = "1400", "2100", "5200", "2000"
	return env
}

func TestBuildProfile(t *testing.T) {
	profile, err := buildProfile(func(k string) string { return withAccounts(map[string]string{})[k] }, false)
	if err != nil || profile.Inventory != "1400" || profile.GRIR != "2100" || profile.PPV != "5200" || profile.VendorControl != "2000" {
		t.Fatalf("profile %+v err %v", profile, err)
	}
	if profile.LedgerID != "0a1b2c3d-0000-4000-8000-000000000001" {
		t.Fatalf("ledger id %q", profile.LedgerID)
	}
	notUUID := withAccounts(map[string]string{})
	notUUID["POSTING_LEDGER_ID"] = "ledger-1"
	if _, err := buildProfile(func(k string) string { return notUUID[k] }, false); err == nil || !strings.Contains(err.Error(), "POSTING_LEDGER_ID must be a UUID") {
		t.Fatalf("got %v, want the ledger id rejected", err)
	}
	partial := map[string]string{"POSTING_LEDGER_ID": "0a1b2c3d-0000-4000-8000-000000000001", "POSTING_ACCOUNT_INVENTORY": "1400"}
	if _, err := buildProfile(func(k string) string { return partial[k] }, false); err == nil || !strings.Contains(err.Error(), "POSTING_ACCOUNT_GRIR is required") {
		t.Fatalf("got %v, want the GR/IR account reported missing", err)
	}
	fake, err := buildProfile(func(string) string { return "" }, true)
	if err != nil || fake.GRIR == "" || fake.VendorControl == "" {
		t.Fatalf("fake profile %+v err %v", fake, err)
	}
}

func TestBuildPorts(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"both urls and accounts", withAccounts(map[string]string{"LEDGER_URL": "http://ledger", "CLOSE_URL": "http://close"}), ""},
		{"both urls without accounts", map[string]string{"LEDGER_URL": "http://ledger", "CLOSE_URL": "http://close"}, "POSTING_LEDGER_ID is required"},
		{"fakes allowed with both urls empty", map[string]string{"PASSAGE_ALLOW_FAKES": "1"}, ""},
		{"empty urls without the flag", map[string]string{}, "LEDGER_URL is required"},
		{"flag must be exactly 1", map[string]string{"PASSAGE_ALLOW_FAKES": "true"}, "LEDGER_URL is required"},
		{"ledger set but close empty", map[string]string{"LEDGER_URL": "http://ledger"}, "CLOSE_URL is required"},
		{"close set but ledger empty", map[string]string{"CLOSE_URL": "http://close"}, "LEDGER_URL is required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ports, err := buildPorts(func(k string) string { return tc.env[k] })
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %v, want error containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || ports.Ledger == nil || ports.Periods == nil {
				t.Fatalf("ports %+v err %v", ports, err)
			}
		})
	}
}

func TestBuildPortsUsesRealAdaptersWhenConfigured(t *testing.T) {
	ports, err := buildPorts(func(k string) string {
		return map[string]string{"LEDGER_URL": "http://l", "CLOSE_URL": "http://c", "PURCHASING_URL": "http://p", "PASSAGE_ALLOW_FAKES": "1"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, real := ports.Ledger.(*ledger.Client); !real {
		t.Fatalf("ledger is %T despite LEDGER_URL", ports.Ledger)
	}
	if _, real := ports.Periods.(*periods.Client); !real {
		t.Fatalf("periods is %T despite CLOSE_URL", ports.Periods)
	}
	if _, real := ports.PurchaseOrders.(*purchasing.Client); !real {
		t.Fatalf("purchase orders is %T despite PURCHASING_URL", ports.PurchaseOrders)
	}
}

func TestSmallPoolConfigCapsConnectionsForURLAndKeywordDSNs(t *testing.T) {
	for name, dsn := range map[string]string{
		"url":     "postgres://passage_app:pw@db:5432/passage_receiving?sslmode=disable",
		"keyword": "host=db port=5432 user=passage_app password=pw dbname=passage_receiving sslmode=disable",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := smallPoolConfig(dsn)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.MaxConns != pendingPoolConns || cfg.ConnConfig.Host != "db" || cfg.ConnConfig.User != "passage_app" || cfg.ConnConfig.Database != "passage_receiving" {
				t.Fatalf("max %d host %s user %s db %s", cfg.MaxConns, cfg.ConnConfig.Host, cfg.ConnConfig.User, cfg.ConnConfig.Database)
			}
		})
	}
	if _, err := smallPoolConfig("://bad"); err == nil {
		t.Fatal("an unparseable DSN must fail")
	}
}
