package main

import (
	"bytes"
	"context"
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

func TestLoadConfig(t *testing.T) {
	get := func(env map[string]string) func(string) string { return func(k string) string { return env[k] } }

	got, err := loadConfig(get(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	want := config{reopenDefaultDays: 3, reopenApprovers: 2, lockWaitMS: 5000, reopenSweepEvery: time.Minute}
	if got != want {
		t.Fatalf("defaults: got %+v, want %+v", got, want)
	}

	got, err = loadConfig(get(map[string]string{
		"CLOSE_REOPEN_DEFAULT_DAYS": "30", "CLOSE_REOPEN_APPROVERS": "3", "CLOSE_LOCK_WAIT_MS": "30000", "CLOSE_REOPEN_SWEEP_INTERVAL": "15s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want = config{reopenDefaultDays: 30, reopenApprovers: 3, lockWaitMS: 30000, reopenSweepEvery: 15 * time.Second}
	if got != want {
		t.Fatalf("overrides: got %+v, want %+v", got, want)
	}

	invalid := []struct{ key, value string }{
		{"CLOSE_REOPEN_APPROVERS", "1"}, {"CLOSE_REOPEN_APPROVERS", "4"}, {"CLOSE_REOPEN_APPROVERS", "two"},
		{"CLOSE_LOCK_WAIT_MS", "500"}, {"CLOSE_LOCK_WAIT_MS", "30001"}, {"CLOSE_LOCK_WAIT_MS", "5.5"},
		{"CLOSE_REOPEN_DEFAULT_DAYS", "0"}, {"CLOSE_REOPEN_DEFAULT_DAYS", "31"}, {"CLOSE_REOPEN_DEFAULT_DAYS", "-1"},
		{"CLOSE_REOPEN_SWEEP_INTERVAL", "abc"}, {"CLOSE_REOPEN_SWEEP_INTERVAL", "10"}, {"CLOSE_REOPEN_SWEEP_INTERVAL", "0s"}, {"CLOSE_REOPEN_SWEEP_INTERVAL", "-5s"},
	}
	for _, tc := range invalid {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			_, err := loadConfig(get(map[string]string{tc.key: tc.value}))
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("got %v, want error naming %s", err, tc.key)
			}
		})
	}
}

func TestRun_InvalidConfigFailsBeforeConnect(t *testing.T) {
	env := map[string]string{"DATABASE_URL": "postgres://unreachable.invalid/db", "CLOSE_REOPEN_APPROVERS": "1"}
	err := run(context.Background(), func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "CLOSE_REOPEN_APPROVERS") {
		t.Fatalf("got %v, want config error before any connect", err)
	}
}

func TestAddr(t *testing.T) {
	env := map[string]string{"HTTP_ADDR": ":9999"}
	get := func(k string) string { return env[k] }
	if got := addr(get, "HTTP_ADDR", defaultHTTPAddr); got != ":9999" {
		t.Errorf("override: got %q", got)
	}
	if got := addr(get, "ADMIN_ADDR", defaultAdminAddr); got != defaultAdminAddr {
		t.Errorf("default: got %q", got)
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
