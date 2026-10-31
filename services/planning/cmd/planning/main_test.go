package main

import (
	"context"
	"strings"
	"testing"
)

func TestIntEnv(t *testing.T) {
	env := map[string]string{"SET": "35", "LOW": "1", "HIGH": "101", "TEXT": "twenty"}
	getenv := func(k string) string { return env[k] }
	if n, err := intEnv(getenv, "UNSET", 20, 2, 100); err != nil || n != 20 {
		t.Fatalf("unset = %d, %v; want the default 20", n, err)
	}
	if n, err := intEnv(getenv, "SET", 20, 2, 100); err != nil || n != 35 {
		t.Fatalf("set = %d, %v; want 35", n, err)
	}
	for _, key := range []string{"LOW", "HIGH", "TEXT"} {
		if _, err := intEnv(getenv, key, 20, 2, 100); err == nil {
			t.Errorf("%s = %q must be refused", key, env[key])
		}
	}
}

func TestRun_RefusesUnknownFeatureFlagBeforeConnecting(t *testing.T) {
	env := map[string]string{"DATABASE_URL": "postgres://unused.invalid/db", "PLANNING_FEATURE_FLAGS": "planning.dynamc"}
	err := run(context.Background(), func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "PLANNING_FEATURE_FLAGS") {
		t.Fatalf("run = %v; want the unknown flag refused", err)
	}
}

func TestRun_RefusesHorizonOutOfRangeBeforeConnecting(t *testing.T) {
	for _, v := range []string{"29", "1096", "a year"} {
		env := map[string]string{"DATABASE_URL": "postgres://unused.invalid/db", "PLANNING_HORIZON_DAYS": v}
		err := run(context.Background(), func(k string) string { return env[k] })
		if err == nil || !strings.Contains(err.Error(), "PLANNING_HORIZON_DAYS") {
			t.Fatalf("PLANNING_HORIZON_DAYS=%q: run = %v; want it refused", v, err)
		}
	}
}
