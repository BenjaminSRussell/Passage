package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

func runEnv(t *testing.T, extra map[string]string) func(string) string {
	t.Helper()
	url := os.Getenv("PASSAGE_TEST_APP_DATABASE_URL")
	if url == "" {
		t.Skip("PASSAGE_TEST_APP_DATABASE_URL is not set")
	}
	seed := make([]byte, ed25519.SeedSize)
	_, _ = rand.Read(seed)
	m := map[string]string{
		"DATABASE_URL": url, "HTTP_ADDR": "127.0.0.1:0", "ADMIN_ADDR": "127.0.0.1:0", "PASSAGE_SERVICE_NAME": "billing",
		"PASSAGE_SERVICE_SIGNING_KEY": base64.StdEncoding.EncodeToString(seed),
	}
	for k, v := range extra {
		m[k] = v
	}
	return func(k string) string { return m[k] }
}

func profilesJSON(t *testing.T, tenantID string) string {
	t.Helper()
	b, err := json.Marshal([]map[string]any{{
		"tenantId": tenantID, "legalEntityId": ids.New(), "ledgerId": ids.New(), "receivableAccount": "1200", "taxAccount": "2300",
		"chargeAccount": "4800", "deferredRevenueAccounts": []string{"4100"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRun_AutoBookEnvStartsTheWorkerAgainstTheConfiguredLedger(t *testing.T) {
	var asked atomic.Int64
	ledger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer ledger.Close()
	tenantID := ids.New()
	getenv := runEnv(t, map[string]string{
		"LEDGER_URL": ledger.URL, "REVREC_URL": "http://revrec.invalid", "BILLING_GL_PROFILES": profilesJSON(t, tenantID),
		"BILLING_GL_AUTOBOOK_TENANT_IDS": tenantID, "BILLING_GL_AUTOBOOK_INTERVAL": "1m",
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, getenv) }()
	deadline := time.After(10 * time.Second)
	for asked.Load() == 0 {
		select {
		case err := <-done:
			t.Fatalf("run stopped before the worker reached the ledger: %v", err)
		case <-deadline:
			t.Fatal("the auto-book worker never called the ledger")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("run after cancel: %v", err)
	}
}

func TestRun_AutoBookEnvWithoutALedgerFailsStartup(t *testing.T) {
	getenv := runEnv(t, map[string]string{"BILLING_GL_AUTOBOOK_TENANT_IDS": ids.New()})
	err := run(context.Background(), getenv)
	if err == nil || !strings.Contains(err.Error(), "BILLING_GL_AUTOBOOK_TENANT_IDS needs the ledger configured") {
		t.Fatalf("err = %v, want the auto-book-needs-a-ledger error", err)
	}
}
