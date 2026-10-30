package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fx/internal/importer"
	"github.com/BenjaminSRussell/passage/services/fx/internal/store"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

type lastSuccessStore struct {
	store.Store
	at      *time.Time
	err     error
	typeErr error
}

func (s lastSuccessStore) GetRateType(context.Context, string) (store.RateType, error) {
	return store.RateType{}, s.typeErr
}

func (s lastSuccessStore) LastSuccess(context.Context, string, string) (*time.Time, error) {
	return s.at, s.err
}

func TestRunRequiresDatabaseURL(t *testing.T) {
	err := run(context.Background(), env(nil))
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunStopsOnMalformedServicePublicKeys(t *testing.T) {
	err := run(context.Background(), func(k string) string {
		return map[string]string{"PASSAGE_SERVICE_NAME": "fx", "PASSAGE_SERVICE_PUBLIC_KEYS": "payables=zzz"}[k]
	})
	if err == nil || !strings.Contains(err.Error(), "PASSAGE_SERVICE_PUBLIC_KEYS") {
		t.Fatalf("run with a malformed key = %v", err)
	}
}

func TestLoggerRenamesTimeAndMessage(t *testing.T) {
	var out bytes.Buffer
	newLogger(&out).Info("hello")
	var line map[string]any
	if err := json.Unmarshal(out.Bytes(), &line); err != nil {
		t.Fatal(err)
	}
	if line["message"] != "hello" || line["timestamp"] == nil || line["service"] != "fx" {
		t.Fatalf("log line = %v", line)
	}
}

func TestImportScheduleConfig(t *testing.T) {
	tenantID := testkit.TenantID(1)
	tests := []struct {
		name    string
		env     map[string]string
		wantNil bool
		wantErr string
	}{
		{"disabled without tenant", nil, true, ""},
		{"invalid tenant", map[string]string{"FX_IMPORT_TENANT_ID": "nope"}, false, "FX_IMPORT_TENANT_ID"},
		{"missing rate type", map[string]string{"FX_IMPORT_TENANT_ID": tenantID}, false, "FX_IMPORT_RATE_TYPE"},
		{"unknown provider", map[string]string{"FX_IMPORT_TENANT_ID": tenantID, "FX_IMPORT_RATE_TYPE": "CLOSING", "FX_IMPORT_PROVIDER": "bloomberg"}, false, "FX_IMPORT_PROVIDER"},
		{"short interval", map[string]string{"FX_IMPORT_TENANT_ID": tenantID, "FX_IMPORT_RATE_TYPE": "CLOSING", "FX_IMPORT_INTERVAL": "1s"}, false, "FX_IMPORT_INTERVAL"},
		{"bad timeout", map[string]string{"FX_IMPORT_TENANT_ID": tenantID, "FX_IMPORT_RATE_TYPE": "CLOSING", "FX_PROVIDER_TIMEOUT_S": "0"}, false, "FX_PROVIDER_TIMEOUT_S"},
		{"ecb defaults", map[string]string{"FX_IMPORT_TENANT_ID": tenantID, "FX_IMPORT_RATE_TYPE": "CLOSING"}, false, ""},
		{"oxr without app id", map[string]string{"FX_IMPORT_TENANT_ID": tenantID, "FX_IMPORT_RATE_TYPE": "CLOSING", "FX_IMPORT_PROVIDER": "openexchangerates"}, false, "FX_OXR_APP_ID"},
		{"oxr", map[string]string{"FX_IMPORT_TENANT_ID": tenantID, "FX_IMPORT_RATE_TYPE": "CLOSING", "FX_IMPORT_PROVIDER": "openexchangerates", "FX_OXR_APP_ID": "app", "FX_IMPORT_INTERVAL": "30m"}, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newImportSchedule(context.Background(), env(tc.env), lastSuccessStore{}, importer.NewMetrics())
			switch {
			case tc.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
			case err != nil:
				t.Fatal(err)
			case (got == nil) != tc.wantNil:
				t.Fatalf("schedule = %v, wantNil %v", got, tc.wantNil)
			}
		})
	}
}

func TestStartupSeedsLastSuccessGauge(t *testing.T) {
	at := time.Date(2026, 10, 20, 14, 0, 0, 0, time.UTC)
	metrics := importer.NewMetrics()
	cfg := env(map[string]string{"FX_IMPORT_TENANT_ID": testkit.TenantID(1), "FX_IMPORT_RATE_TYPE": "CLOSING"})
	if _, err := newImportSchedule(context.Background(), cfg, lastSuccessStore{at: &at}, metrics); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	adminHandler(metrics).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	want := `fx_provider_last_success_timestamp_seconds{provider="ecb",rate_type="CLOSING"} ` + "1792504800"
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("metrics %d:\n%s", rec.Code, rec.Body.String())
	}
}

func TestStartupWithoutPriorSuccessLeavesGaugeUnset(t *testing.T) {
	metrics := importer.NewMetrics()
	cfg := env(map[string]string{"FX_IMPORT_TENANT_ID": testkit.TenantID(1), "FX_IMPORT_RATE_TYPE": "CLOSING"})
	if _, err := newImportSchedule(context.Background(), cfg, lastSuccessStore{err: context.DeadlineExceeded}, metrics); err != nil {
		t.Fatalf("a failed seed lookup must not stop startup: %v", err)
	}
	rec := httptest.NewRecorder()
	adminHandler(metrics).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if strings.Contains(rec.Body.String(), "fx_provider_last_success_timestamp_seconds{") {
		t.Fatalf("gauge must stay unset:\n%s", rec.Body.String())
	}
}

func TestLoopRunsImmediatelyThenOnEveryTickUntilCancelled(t *testing.T) {
	var runs atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	s := &importSchedule{interval: 10 * time.Millisecond, once: func(context.Context) {
		if runs.Add(1) == 3 {
			cancel()
		}
	}}
	done := make(chan struct{})
	go func() { s.loop(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not stop after cancel")
	}
	if runs.Load() < 3 {
		t.Fatalf("runs = %d", runs.Load())
	}
}

func TestImportScheduleRejectsUnknownRateType(t *testing.T) {
	cfg := env(map[string]string{"FX_IMPORT_TENANT_ID": testkit.TenantID(1), "FX_IMPORT_RATE_TYPE": "NOPE"})
	_, err := newImportSchedule(context.Background(), cfg, lastSuccessStore{typeErr: context.DeadlineExceeded}, importer.NewMetrics())
	if err == nil || !strings.Contains(err.Error(), "FX_IMPORT_RATE_TYPE") {
		t.Fatalf("err = %v", err)
	}
}
