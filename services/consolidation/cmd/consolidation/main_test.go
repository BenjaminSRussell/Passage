package main

import (
	"context"
	"strings"
	"testing"
)

func TestRun_RequiresDatabaseURL(t *testing.T) {
	err := run(context.Background(), func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("run without DATABASE_URL = %v", err)
	}
}

func TestRun_RejectsMalformedDatabaseURL(t *testing.T) {
	env := map[string]string{"DATABASE_URL": "postgres://%zz"}
	err := run(context.Background(), func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "connect database") {
		t.Fatalf("run with a malformed DATABASE_URL = %v", err)
	}
}
