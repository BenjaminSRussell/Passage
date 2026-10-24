package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunRequiresDatabaseURL(t *testing.T) {
	err := run(context.Background(), func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoggerUsesStructuredServiceFields(t *testing.T) {
	var output bytes.Buffer
	logger := newLogger(&output)
	logger.Info("started")
	got := output.String()
	if !strings.Contains(got, `"service":"metadata"`) || !strings.Contains(got, `"message":"started"`) {
		t.Fatalf("log = %s", got)
	}
}
