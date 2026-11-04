package main

import (
	"context"
	"strings"
	"testing"
)

func TestRunStopsOnMalformedServicePublicKeys(t *testing.T) {
	err := run(context.Background(), func(k string) string {
		return map[string]string{"PASSAGE_SERVICE_NAME": "transport", "PASSAGE_SERVICE_PUBLIC_KEYS": "workflow-ts=zzz"}[k]
	})
	if err == nil || !strings.Contains(err.Error(), "PASSAGE_SERVICE_PUBLIC_KEYS") {
		t.Fatalf("run with a malformed key = %v", err)
	}
}

func TestRunRequiresDatabaseURL(t *testing.T) {
	err := run(context.Background(), func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("run without DATABASE_URL = %v", err)
	}
}
