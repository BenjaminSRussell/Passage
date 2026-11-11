package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
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

func TestProviderTimeout(t *testing.T) {
	tests := []struct {
		raw  string
		want time.Duration
	}{
		{"", domain.DefaultProviderTimeout},
		{"abc", domain.DefaultProviderTimeout},
		{"-5", domain.DefaultProviderTimeout},
		{"0", domain.DefaultProviderTimeout},
		{"750", 750 * time.Millisecond},
	}
	for _, tc := range tests {
		if got := providerTimeout(func(string) string { return tc.raw }); got != tc.want {
			t.Errorf("TAX_PROVIDER_TIMEOUT_MS=%q gave %s, want %s", tc.raw, got, tc.want)
		}
	}
}

func TestProvidersFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got, err := providersFromEnv(env(nil)); err != nil || len(got) != 0 {
		t.Fatalf("no provider configured: %v %v", got, err)
	}
	got, err := providersFromEnv(env(map[string]string{
		"AVALARA_URL": "https://sandbox-rest.avatax.com", "AVALARA_ACCOUNT_ID": "a", "AVALARA_LICENSE_KEY": "k", "AVALARA_COMPANY_CODE": "C",
		"VERTEX_URL": "https://vertex.example", "VERTEX_TOKEN_URL": "https://vertex.example/token", "VERTEX_CLIENT_ID": "i", "VERTEX_CLIENT_SECRET": "s",
		"VERTEX_USERNAME": "u", "VERTEX_PASSWORD": "p", "VERTEX_COMPANY": "C"}))
	if err != nil || got["avalara"] == nil || got["vertex"] == nil {
		t.Fatalf("both configured: %v %v", got, err)
	}
	if _, err := providersFromEnv(env(map[string]string{"VERTEX_URL": "https://vertex.example"})); err == nil {
		t.Error("a Vertex URL without credentials started the service")
	}
}
