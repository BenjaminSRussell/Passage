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
