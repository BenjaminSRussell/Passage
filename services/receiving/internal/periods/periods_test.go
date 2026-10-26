package periods

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const tenantID = "00000000-0000-0000-0000-000000000001"

func query() domain.PeriodQuery {
	return domain.PeriodQuery{LedgerID: "l-1", FiscalPeriodID: "p-1", PostingDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
}

func ctx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID})
}

func TestAP_SendsCheckRequestAndMapsOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		want    domain.PeriodStatus
		wantErr bool
	}{
		{name: "open", status: http.StatusOK, body: `{"allowed":true,"module":"ap","status":"open"}`, want: domain.PeriodOpen},
		{name: "on hold allowed through", status: http.StatusOK, body: `{"allowed":true,"module":"all","status":"on_hold"}`, want: domain.PeriodOnHold},
		{name: "closed", status: http.StatusUnprocessableEntity, body: `{"type":"about:blank","title":"Period closed","status":422,"code":"period_closed","serviceCode":"LEDGER-0013","instance":"/v1/close/period-statuses:check"}`, want: domain.PeriodClosed},
		{name: "on hold rejected", status: http.StatusForbidden, body: `{"type":"about:blank","title":"Period on hold","status":403,"code":"LEDGER-0014","serviceCode":"LEDGER-0014","instance":"/v1/close/period-statuses:check"}`, want: domain.PeriodOnHold},
		{name: "validation failure is an error", status: http.StatusBadRequest, body: `{"code":"validation_failed","serviceCode":"CLOSE-0004"}`, wantErr: true},
		{name: "bare code still maps", status: http.StatusUnprocessableEntity, body: `{"code":"LEDGER-0013"}`, want: domain.PeriodClosed},
		{name: "unknown 422 code is an error", status: http.StatusUnprocessableEntity, body: `{"code":"OTHER","serviceCode":"OTHER"}`, wantErr: true},
		{name: "server error", status: http.StatusInternalServerError, body: `{}`, wantErr: true},
		{name: "200 without status", status: http.StatusOK, body: `{"allowed":true}`, wantErr: true},
		{name: "200 not allowed", status: http.StatusOK, body: `{"allowed":false,"status":"open"}`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got http.Request
			var sent requestBody
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = *r.Clone(context.Background())
				_ = json.NewDecoder(r.Body).Decode(&sent)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)

			status, err := New(srv.URL+"/", nil).AP(ctx(), query())

			if (err != nil) != tc.wantErr || status != tc.want {
				t.Fatalf("status %q err %v, want %q wantErr %v", status, err, tc.want, tc.wantErr)
			}
			if got.Method != http.MethodPost || got.URL.Path != checkPath || got.Header.Get(tenant.HeaderDuties) != dutyPeriodCheck || got.Header.Get(tenant.HeaderTenant) != tenantID {
				t.Fatalf("request %s %s headers %v", got.Method, got.URL.Path, got.Header)
			}
			if sent != (requestBody{LedgerID: "l-1", FiscalPeriodID: "p-1", PostingDate: "2026-09-01", SourceSystem: "ap"}) {
				t.Fatalf("body %+v", sent)
			}
		})
	}
}

func TestAP_FailsWithoutPrincipalAndOnTimeout(t *testing.T) {
	if _, err := New("http://unused", nil).AP(context.Background(), query()); err == nil {
		t.Fatal("expected error without principal")
	}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(func() { close(release); srv.Close() })
	status, err := New(srv.URL, &http.Client{Timeout: 50 * time.Millisecond}).AP(ctx(), query())
	if err == nil || status != "" {
		t.Fatalf("status %q err %v, want timeout error", status, err)
	}
}
