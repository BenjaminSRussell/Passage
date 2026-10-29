package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunRefusesMissingPackDirectory(t *testing.T) {
	err := run(context.Background(), func(k string) string {
		if k == "COUNTRY_PACKS_DIR" {
			return t.TempDir()
		}
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "load country packs") {
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
