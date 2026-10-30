package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"missing database url", map[string]string{}, "DATABASE_URL is required"},
		{"unparseable database url", map[string]string{"DATABASE_URL": "://bad"}, "connect database"},
		{"smtp enabled without credentials", map[string]string{"DATABASE_URL": "://bad", "NOTIFY_SMTP_ENABLED": "true"}, "notify config: smtp notify adapter enabled but missing smtp-host"},
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

func testSigningKey() string {
	return base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)).Seed())
}

func TestLoadDispatchConfig(t *testing.T) {
	env := func(extra map[string]string) func(string) string {
		base := map[string]string{"PRODUCT_URL": "http://product:8080", "PASSAGE_SERVICE_NAME": "batch", "PASSAGE_SERVICE_SIGNING_KEY": testSigningKey()}
		for k, v := range extra {
			base[k] = v
		}
		return func(k string) string { return base[k] }
	}
	if cfg, err := loadDispatchConfig(func(string) string { return "" }); cfg != nil || err != nil {
		t.Fatalf("no PRODUCT_URL must leave the dispatcher off: %v, %v", cfg, err)
	}
	cfg, err := loadDispatchConfig(env(nil))
	if err != nil || cfg.interval != 5*time.Second || cfg.concurrency != 4 || cfg.maxAttempts != 5 || cfg.retryAfter != time.Minute {
		t.Fatalf("defaults = %+v, %v", cfg, err)
	}
	cfg, err = loadDispatchConfig(env(map[string]string{"DISPATCH_INTERVAL": "250ms", "DISPATCH_CONCURRENCY": "2", "DISPATCH_MAX_RUN_ATTEMPTS": "9", "DISPATCH_RETRY_AFTER": "30s"}))
	if err != nil || cfg.interval != 250*time.Millisecond || cfg.concurrency != 2 || cfg.maxAttempts != 9 || cfg.retryAfter != 30*time.Second {
		t.Fatalf("overrides = %+v, %v", cfg, err)
	}
	for name, extra := range map[string]map[string]string{
		"no signing key":      {"PASSAGE_SERVICE_SIGNING_KEY": ""},
		"malformed key":       {"PASSAGE_SERVICE_SIGNING_KEY": "not-a-seed"},
		"wrong service name":  {"PASSAGE_SERVICE_NAME": "receiving"},
		"bad interval":        {"DISPATCH_INTERVAL": "soon"},
		"zero concurrency":    {"DISPATCH_CONCURRENCY": "0"},
		"bad attempts":        {"DISPATCH_MAX_RUN_ATTEMPTS": "-1"},
		"negative retry wait": {"DISPATCH_RETRY_AFTER": "-5s"},
	} {
		if _, err := loadDispatchConfig(env(extra)); err == nil {
			t.Errorf("%s: startup must fail instead of dispatching without a valid service identity", name)
		}
	}
}

func TestRun_ProductURLWithoutSigningKeyStopsStartup(t *testing.T) {
	err := run(context.Background(), func(k string) string {
		return map[string]string{"DATABASE_URL": "://bad", "PRODUCT_URL": "http://product", "PASSAGE_SERVICE_NAME": "batch"}[k]
	})
	if err == nil || !strings.Contains(err.Error(), "PASSAGE_SERVICE_SIGNING_KEY") {
		t.Fatalf("err = %v", err)
	}
}
