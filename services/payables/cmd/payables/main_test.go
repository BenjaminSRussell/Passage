package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
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

func TestAddrFallsBackToDefault(t *testing.T) {
	none := func(string) string { return "" }
	if got := addr(none, "HTTP_ADDR", defaultHTTPAddr); got != ":8080" {
		t.Fatalf("got %s", got)
	}
	set := func(string) string { return ":9999" }
	if got := addr(set, "HTTP_ADDR", defaultHTTPAddr); got != ":9999" {
		t.Fatalf("got %s", got)
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

func TestNotImplementedListsEveryDeliverable(t *testing.T) {
	doc, err := os.ReadFile("../../NOT-IMPLEMENTED.md")
	if err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 9; n++ {
		if !strings.Contains(string(doc), fmt.Sprintf("Deliverable %d:", n)) {
			t.Errorf("NOT-IMPLEMENTED.md has no row for deliverable %d", n)
		}
	}
}
