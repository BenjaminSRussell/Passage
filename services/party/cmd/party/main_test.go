package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
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

func TestRunServesProbesAndMetricsThenStopsOnCancel(t *testing.T) {
	httpAddr, adminAddr := freeAddr(t), freeAddr(t)
	env := map[string]string{"HTTP_ADDR": httpAddr, "ADMIN_ADDR": adminAddr}
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

func TestNewCheckerDefaultsToUnverifiedAndOptsIntoFixture(t *testing.T) {
	for env, want := range map[string]domain.Status{"": domain.StatusUnverified, "1": domain.StatusValid} {
		checker, err := newChecker(func(string) string { return env })
		if err != nil {
			t.Fatal(err)
		}
		got, _ := checker.Check(context.Background(), "DE", "123456789")
		if got != want {
			t.Errorf("VIES_FIXTURE=%q got %s want %s", env, got, want)
		}
	}
}
