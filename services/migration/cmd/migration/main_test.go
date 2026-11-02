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
		{"bad egress allowlist", map[string]string{"DATABASE_URL": "postgres://x", "MIG_EGRESS_ALLOW": "10.0.0.0/99"}, "MIG_EGRESS_ALLOW"},
		{"target address without a signing key", map[string]string{"DATABASE_URL": "postgres://x", "MIG_TARGET_PARTY_URL": "http://party:8080"}, "signing key"},
		{"signing key for another service", map[string]string{"DATABASE_URL": "postgres://x", "MIG_TARGET_PARTY_URL": "http://party:8080", "PASSAGE_SERVICE_NAME": "receiving", "PASSAGE_SERVICE_SIGNING_KEY": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))}, `must be "migration"`},
		{"bad fieldcrypt keys", map[string]string{"DATABASE_URL": "postgres://x", "FIELDCRYPT_KEYS": "k=not-base64", "FIELDCRYPT_CURRENT_KEY_ID": "k"}, "fieldcrypt keys"},
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

func TestConnectionDeps(t *testing.T) {
	keys, prober, err := connectionDeps(func(string) string { return "" })
	if err != nil || keys != nil || prober == nil || prober.Guard == nil {
		t.Fatalf("unset = %v %v %v", keys, prober, err)
	}
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	env := map[string]string{"FIELDCRYPT_KEYS": "k1=" + key, "FIELDCRYPT_CURRENT_KEY_ID": "k1", "MIG_EGRESS_ALLOW": "10.0.0.0/8, erp.corp:1433", "MIG_DROP_ROOT": "/drop"}
	keys, prober, err = connectionDeps(func(k string) string { return env[k] })
	if err != nil || keys == nil || prober.DropRoot != "/drop" {
		t.Fatalf("set = %v %v %v", keys, prober, err)
	}
}

func TestTargetConfig(t *testing.T) {
	cfg, err := targetConfig(func(string) string { return "" })
	if err != nil || cfg.Configured() {
		t.Fatalf("unset = %+v, %v; the service must stay on the 501 backend", cfg, err)
	}
	env := map[string]string{
		"MIG_TARGET_PARTY_URL": "http://party:8080", "PASSAGE_SERVICE_NAME": "migration",
		"PASSAGE_SERVICE_SIGNING_KEY": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)),
	}
	cfg, err = targetConfig(func(k string) string { return env[k] })
	if err != nil || !cfg.Configured() || cfg.PartyURL != "http://party:8080" || cfg.ProductURL != "" {
		t.Fatalf("set = %+v, %v", cfg, err)
	}
	delete(env, "MIG_TARGET_PARTY_URL")
	if cfg, err = targetConfig(func(k string) string { return env[k] }); err != nil || cfg.Configured() {
		t.Fatalf("a key without an address is not a configured loader: %+v, %v", cfg, err)
	}
	// Misconfiguration stops the service instead of falling back to 501 or sending unsigned calls.
	for name, bad := range map[string]map[string]string{
		"address without a key":  {"MIG_TARGET_PRODUCT_URL": "http://product:8080", "PASSAGE_SERVICE_NAME": "migration"},
		"key of another service": {"MIG_TARGET_PRODUCT_URL": "http://product:8080", "PASSAGE_SERVICE_NAME": "party", "PASSAGE_SERVICE_SIGNING_KEY": env["PASSAGE_SERVICE_SIGNING_KEY"]},
		"address without scheme": {"MIG_TARGET_RECEIVABLES_URL": "receivables:8080", "PASSAGE_SERVICE_NAME": "migration", "PASSAGE_SERVICE_SIGNING_KEY": env["PASSAGE_SERVICE_SIGNING_KEY"]},
		"address with userinfo":  {"MIG_TARGET_PARTY_URL": "http://u:p@party:8080", "PASSAGE_SERVICE_NAME": "migration", "PASSAGE_SERVICE_SIGNING_KEY": env["PASSAGE_SERVICE_SIGNING_KEY"]},
		"malformed key":          {"MIG_TARGET_PARTY_URL": "http://party:8080", "PASSAGE_SERVICE_NAME": "migration", "PASSAGE_SERVICE_SIGNING_KEY": "not-base64"},
	} {
		if _, err := targetConfig(func(k string) string { return bad[k] }); err == nil {
			t.Errorf("%s: want a startup error", name)
		} else if strings.Contains(err.Error(), "p@") {
			t.Errorf("%s: the error repeats the address's credentials: %v", name, err)
		}
	}
}
