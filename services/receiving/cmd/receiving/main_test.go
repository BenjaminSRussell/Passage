package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/ledger"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/periods"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"missing database url", map[string]string{}, "DATABASE_URL is required"},
		{"unparseable database url", map[string]string{"DATABASE_URL": "://bad", "PASSAGE_ALLOW_FAKES": "1"}, "connect database"},
		{"real adapters required without the fakes flag", map[string]string{"DATABASE_URL": "://bad"}, "LEDGER_URL is required"},
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
	for _, want := range []string{"Deliverable 2", "Deliverable 3", "Deliverable 4", "Deliverable 5", "Deliverable 6", "Deliverable 7", "Deliverable 8"} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("NOT-IMPLEMENTED.md has no row for %s", want)
		}
	}
}

func TestBuildPorts(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"both urls", map[string]string{"LEDGER_URL": "http://ledger", "CLOSE_URL": "http://close"}, ""},
		{"fakes allowed with both urls empty", map[string]string{"PASSAGE_ALLOW_FAKES": "1"}, ""},
		{"empty urls without the flag", map[string]string{}, "LEDGER_URL is required"},
		{"flag must be exactly 1", map[string]string{"PASSAGE_ALLOW_FAKES": "true"}, "LEDGER_URL is required"},
		{"ledger set but close empty", map[string]string{"LEDGER_URL": "http://ledger"}, "CLOSE_URL is required"},
		{"close set but ledger empty", map[string]string{"CLOSE_URL": "http://close"}, "LEDGER_URL is required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ports, err := buildPorts(func(k string) string { return tc.env[k] })
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %v, want error containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || ports.Ledger == nil || ports.Periods == nil {
				t.Fatalf("ports %+v err %v", ports, err)
			}
		})
	}
}

func TestBuildPortsUsesRealAdaptersWhenConfigured(t *testing.T) {
	ports, err := buildPorts(func(k string) string {
		return map[string]string{"LEDGER_URL": "http://l", "CLOSE_URL": "http://c", "PASSAGE_ALLOW_FAKES": "1"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, real := ports.Ledger.(*ledger.Client); !real {
		t.Fatalf("ledger is %T despite LEDGER_URL", ports.Ledger)
	}
	if _, real := ports.Periods.(*periods.Client); !real {
		t.Fatalf("periods is %T despite CLOSE_URL", ports.Periods)
	}
}
