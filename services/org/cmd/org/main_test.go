package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"missing database url", map[string]string{}, "DATABASE_URL is required"},
		{"max nodes below range", map[string]string{"DATABASE_URL": "postgres://unreachable.invalid/x", "ORG_HIERARCHY_MAX_NODES": "999"}, "ORG_HIERARCHY_MAX_NODES"},
		{"max depth above range", map[string]string{"DATABASE_URL": "postgres://unreachable.invalid/x", "ORG_HIERARCHY_MAX_DEPTH": "26"}, "ORG_HIERARCHY_MAX_DEPTH"},
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

func TestTreeLimits(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    domain.TreeLimits
		wantErr string
	}{
		{"unset keeps the defaults", map[string]string{}, domain.TreeLimits{MaxDepth: 15, MaxNodes: 50000}, ""},
		{"valid values", map[string]string{"ORG_HIERARCHY_MAX_NODES": "1000", "ORG_HIERARCHY_MAX_DEPTH": "25"}, domain.TreeLimits{MaxDepth: 25, MaxNodes: 1000}, ""},
		{"upper nodes bound", map[string]string{"ORG_HIERARCHY_MAX_NODES": "200000"}, domain.TreeLimits{MaxDepth: 15, MaxNodes: 200000}, ""},
		{"lower depth bound", map[string]string{"ORG_HIERARCHY_MAX_DEPTH": "5"}, domain.TreeLimits{MaxDepth: 5, MaxNodes: 50000}, ""},
		{"nodes 999", map[string]string{"ORG_HIERARCHY_MAX_NODES": "999"}, domain.TreeLimits{}, "ORG_HIERARCHY_MAX_NODES"},
		{"nodes 200001", map[string]string{"ORG_HIERARCHY_MAX_NODES": "200001"}, domain.TreeLimits{}, "ORG_HIERARCHY_MAX_NODES"},
		{"nodes abc", map[string]string{"ORG_HIERARCHY_MAX_NODES": "abc"}, domain.TreeLimits{}, "ORG_HIERARCHY_MAX_NODES"},
		{"depth 4", map[string]string{"ORG_HIERARCHY_MAX_DEPTH": "4"}, domain.TreeLimits{}, "ORG_HIERARCHY_MAX_DEPTH"},
		{"depth 26", map[string]string{"ORG_HIERARCHY_MAX_DEPTH": "26"}, domain.TreeLimits{}, "ORG_HIERARCHY_MAX_DEPTH"},
		{"depth 1.5", map[string]string{"ORG_HIERARCHY_MAX_DEPTH": "1.5"}, domain.TreeLimits{}, "ORG_HIERARCHY_MAX_DEPTH"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := treeLimits(func(k string) string { return tc.env[k] })
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %v, want error naming %s", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
			}
		})
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
