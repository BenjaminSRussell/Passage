package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRun_RequiresDatabaseURL(t *testing.T) {
	err := run(context.Background(), func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("err = %v", err)
	}
}

func TestAddr_FallsBackWhenUnset(t *testing.T) {
	env := map[string]string{"HTTP_ADDR": ":9999"}
	get := func(k string) string { return env[k] }
	if got := addr(get, "HTTP_ADDR", ":8080"); got != ":9999" {
		t.Fatalf("set: %s", got)
	}
	if got := addr(get, "OTHER", ":8080"); got != ":8080" {
		t.Fatalf("fallback: %s", got)
	}
}

func TestLogger_UsesPlatformFieldNames(t *testing.T) {
	var buf bytes.Buffer
	newLogger(&buf).Info("hello")
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line["message"] != "hello" || line["service"] != "print" || line["timestamp"] == nil {
		t.Fatalf("log line = %v", line)
	}
}

func TestRun_RejectsInvalidPrintSettingsBeforeConnecting(t *testing.T) {
	env := map[string]string{"DATABASE_URL": "postgres://unused", "PRINT_RETRY": "11"}
	err := run(context.Background(), func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "PRINT_RETRY") {
		t.Fatalf("err = %v", err)
	}
}
