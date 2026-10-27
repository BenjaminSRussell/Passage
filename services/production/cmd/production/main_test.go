package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestConfig(t *testing.T) {
	cfg, err := config(env(nil))
	if err != nil || !cfg.RAFTolerancePct.IsZero() {
		t.Fatalf("default tolerance: %s, %v", cfg.RAFTolerancePct, err)
	}
	cfg, err = config(env(map[string]string{"PRODUCTION_RAF_TOLERANCE_PCT": "2.5"}))
	if err != nil || cfg.RAFTolerancePct.String() != "2.5" {
		t.Fatalf("tolerance 2.5: %s, %v", cfg.RAFTolerancePct, err)
	}
	for _, bad := range []string{"-1", "100.01", "ten"} {
		if _, err := config(env(map[string]string{"PRODUCTION_RAF_TOLERANCE_PCT": bad})); err == nil {
			t.Errorf("tolerance %q accepted", bad)
		}
	}
}

func TestConfig_JournalParameters(t *testing.T) {
	cfg, err := config(env(nil))
	if err != nil || cfg.OverConsumptionPct.String() != "10" || !cfg.AutoStartOnFeedback {
		t.Fatalf("defaults: over %s autostart %v, %v", cfg.OverConsumptionPct, cfg.AutoStartOnFeedback, err)
	}
	cfg, err = config(env(map[string]string{"PRODUCTION_OVERCONSUMPTION_PCT": "250", "PRODUCTION_AUTOSTART_ON_FEEDBACK": "false"}))
	if err != nil || cfg.OverConsumptionPct.String() != "250" || cfg.AutoStartOnFeedback {
		t.Fatalf("overrides: over %s autostart %v, %v", cfg.OverConsumptionPct, cfg.AutoStartOnFeedback, err)
	}
	for name, bad := range map[string]string{"PRODUCTION_OVERCONSUMPTION_PCT": "1000.5", "PRODUCTION_AUTOSTART_ON_FEEDBACK": "yes"} {
		if _, err := config(env(map[string]string{name: bad})); err == nil {
			t.Errorf("%s=%q accepted", name, bad)
		}
	}
}

func TestRunRequiresDatabaseURL(t *testing.T) {
	if err := run(context.Background(), env(nil)); err == nil {
		t.Fatal("run without DATABASE_URL succeeded")
	}
}

func TestLoggerWritesServiceJSON(t *testing.T) {
	var buf bytes.Buffer
	newLogger(&buf).Info("hello")
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line["service"] != "production" || line["message"] != "hello" || line["timestamp"] == nil {
		t.Fatalf("log line %v", line)
	}
}
