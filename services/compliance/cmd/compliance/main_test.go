package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunRequiresDatabase(t *testing.T) {
	err := run(context.Background(), func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("got %v", err)
	}
	err = run(context.Background(), func(k string) string {
		if k == "DATABASE_URL" {
			return "://bad"
		}
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "connect database") {
		t.Fatalf("got %v", err)
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

func TestConfigReadsTenantPolicies(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	cfg, err := config(env(nil))
	if err != nil || !cfg.BlockHigh || cfg.MaxMitigationDays != 365 {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	cfg, err = config(env(map[string]string{"SOD_BLOCK_HIGH": "false", "SOD_MAX_MITIGATION_DAYS": "180"}))
	if err != nil || cfg.BlockHigh || cfg.MaxMitigationDays != 180 {
		t.Fatalf("overrides: %+v %v", cfg, err)
	}
	for _, bad := range []map[string]string{{"SOD_BLOCK_HIGH": "no"}, {"SOD_MAX_MITIGATION_DAYS": "731"}, {"SOD_MAX_MITIGATION_DAYS": "x"}} {
		if _, err := config(env(bad)); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
