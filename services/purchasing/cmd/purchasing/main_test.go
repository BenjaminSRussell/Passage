package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func TestRun_RequiresDatabaseURL(t *testing.T) {
	err := run(context.Background(), func(string) string { return "" })
	if err == nil || err.Error() != "DATABASE_URL is required" {
		t.Fatalf("run without DATABASE_URL = %v", err)
	}
}

func TestNewLogger_WritesServiceTaggedJSON(t *testing.T) {
	var buf bytes.Buffer
	newLogger(&buf).Info("hello")
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line["service"] != "purchasing" || line["message"] != "hello" || line["timestamp"] == nil {
		t.Fatalf("log line = %v", line)
	}
}

func TestAddr_FallsBackWhenUnset(t *testing.T) {
	env := map[string]string{"HTTP_ADDR": ":7000"}
	getenv := func(k string) string { return env[k] }
	if got := addr(getenv, "HTTP_ADDR", ":8080"); got != ":7000" {
		t.Fatalf("addr = %s", got)
	}
	if got := addr(getenv, "ADMIN_ADDR", ":9090"); got != ":9090" {
		t.Fatalf("admin addr = %s", got)
	}
}
