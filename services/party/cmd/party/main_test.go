package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}
func get(t *testing.T, url string) (int, string) {
	t.Helper()
	var lastErr error
	for i := 0; i < 50; i++ {
		resp, err := http.Get(url)
		if err == nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(body)
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("GET %s: %v", url, lastErr)
	return 0, ""
}

func TestRun_RejectsMissingOrUnparseableDatabaseURL(t *testing.T) {
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

func TestRunServesProbesAndMetricsThenStopsOnCancel(t *testing.T) {
	databaseURL := os.Getenv("PASSAGE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("PASSAGE_TEST_DATABASE_URL not set; skipping database-backed serve test")
	}
	httpAddr, adminAddr := freeAddr(t), freeAddr(t)
	env := map[string]string{"HTTP_ADDR": httpAddr, "ADMIN_ADDR": adminAddr, "DATABASE_URL": databaseURL}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, func(k string) string { return env[k] }) }()

	if status, _ := get(t, "http://"+httpAddr+"/healthz"); status != http.StatusOK {
		t.Fatalf("healthz %d", status)
	}
	if status, _ := get(t, "http://"+httpAddr+"/readyz"); status != http.StatusOK {
		t.Fatalf("readyz %d", status)
	}
	if status, body := get(t, "http://"+adminAddr+"/metrics"); status != http.StatusOK || !bytes.Contains([]byte(body), []byte("party_tax_validations_total")) {
		t.Fatalf("metrics %d %s", status, body)
	}
	if status, body := get(t, "http://"+httpAddr+"/v1/party/tax-registrations:validate"); status != http.StatusUnauthorized && status != http.StatusMethodNotAllowed {
		t.Fatalf("validate GET %d %s", status, body)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("run did not stop after cancel")
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

func TestTaxCheckerUsesVIESOnlyWhenConfigured(t *testing.T) {
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	if got := taxChecker(getenv); fmt.Sprintf("%T", got) != "vies.Unavailable" {
		t.Fatalf("empty PARTY_VIES_URL gave %T", got)
	}
	env["PARTY_VIES_URL"] = "http://vies.invalid"
	if got := taxChecker(getenv); fmt.Sprintf("%T", got) != "*vies.Client" {
		t.Fatalf("configured PARTY_VIES_URL gave %T", got)
	}
}
