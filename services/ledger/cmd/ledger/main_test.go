package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAddr_FallsBackWhenUnset(t *testing.T) {
	none := func(string) string { return "" }
	if got := addr(none, "HTTP_ADDR", defaultHTTPAddr); got != defaultHTTPAddr {
		t.Fatalf("addr = %q, want %q", got, defaultHTTPAddr)
	}
	set := func(k string) string {
		if k == "HTTP_ADDR" {
			return ":9999"
		}
		return ""
	}
	if got := addr(set, "HTTP_ADDR", defaultHTTPAddr); got != ":9999" {
		t.Fatalf("addr = %q, want :9999", got)
	}
}

func TestRun_RequiresDatabaseURL(t *testing.T) {
	err := run(context.Background(), func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("err = %v, want DATABASE_URL error", err)
	}
}

func TestNewLogger_EmitsServiceAndMessageKeys(t *testing.T) {
	var buf bytes.Buffer
	newLogger(&buf).Info("hello")
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line["service"] != "ledger" || line["message"] != "hello" || line["timestamp"] == nil {
		t.Fatalf("log line missing required keys: %v", line)
	}
}

func TestRun_MalformedServicePublicKeysStopStartup(t *testing.T) {
	err := run(context.Background(), func(k string) string {
		return map[string]string{"PASSAGE_SERVICE_NAME": "svc", "PASSAGE_SERVICE_PUBLIC_KEYS": "receiving=zzz"}[k]
	})
	if err == nil || !strings.Contains(err.Error(), "PASSAGE_SERVICE_PUBLIC_KEYS") {
		t.Fatalf("err = %v, want the malformed public keys to stop startup", err)
	}
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestPeriodGate_RequiresCloseOrAnExplicitOptOut(t *testing.T) {
	if g, err := periodGate(envOf(nil)); err == nil || g != nil || !strings.Contains(err.Error(), "LEDGER_PERIOD_CHECK=disabled") {
		t.Fatalf("unset must refuse to start, got %v %v", g, err)
	}
	if g, err := periodGate(envOf(map[string]string{"LEDGER_PERIOD_CHECK": "disabled"})); err != nil || g != nil {
		t.Fatalf("explicit opt-out: %v %v", g, err)
	}
	if _, err := periodGate(envOf(map[string]string{"LEDGER_PERIOD_CHECK": "off"})); err == nil {
		t.Fatal("only the exact word disabled opts out")
	}
}

func TestPeriodGate_CloseUrlNeedsASigningKey(t *testing.T) {
	if _, err := periodGate(envOf(map[string]string{"CLOSE_URL": "http://close"})); err == nil || !strings.Contains(err.Error(), "signing key") {
		t.Fatalf("want a signing key error, got %v", err)
	}
	g, err := periodGate(envOf(map[string]string{"CLOSE_URL": "http://close", "PASSAGE_SERVICE_NAME": "ledger",
		"PASSAGE_SERVICE_SIGNING_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}))
	if err != nil || g == nil {
		t.Fatalf("configured: %v %v", g, err)
	}
}
