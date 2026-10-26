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

func TestAddr(t *testing.T) {
	env := map[string]string{"HTTP_ADDR": ":7000"}
	getenv := func(k string) string { return env[k] }
	if got := addr(getenv, "HTTP_ADDR", defaultHTTPAddr); got != ":7000" {
		t.Errorf("set HTTP_ADDR = %q, want :7000", got)
	}
	if got := addr(getenv, "ADMIN_ADDR", defaultAdminAddr); got != defaultAdminAddr {
		t.Errorf("unset ADMIN_ADDR = %q, want %s", got, defaultAdminAddr)
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
	if line["service"] != "costing" {
		t.Errorf("service = %v, want costing", line["service"])
	}
}
