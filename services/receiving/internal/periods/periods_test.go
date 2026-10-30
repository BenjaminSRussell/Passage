package periods

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const (
	tenantID = "00000000-0000-0000-0000-000000000001"
	entityID = "00000000-0000-0000-0000-0000000000e1"
)

func query() domain.PeriodQuery {
	return domain.PeriodQuery{LegalEntityID: entityID, LedgerID: "l-1", FiscalPeriodID: "p-1", PostingDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
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
			if got.Method != http.MethodPost || got.URL.Path != checkPath || got.Header.Get(tenant.HeaderDuties) != dutyPeriodCheck || got.Header.Get(tenant.HeaderTenant) != tenantID || got.Header.Get(tenant.HeaderLegalEntity) != entityID {
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

func TestAP_ForwardsLegalEntity(t *testing.T) {
	tests := []struct {
		name      string
		queryID   string
		principal string
		want      string
		wantErr   bool
	}{
		{name: "invoice entity wins", queryID: entityID, principal: "", want: entityID},
		{name: "falls back to the caller's single entity", queryID: "", principal: entityID, want: entityID},
		{name: "no entity fails closed", wantErr: true},
		{name: "multi-entity caller without an invoice entity fails closed", principal: entityID + "," + tenantID, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Get(tenant.HeaderLegalEntity)
				_, _ = w.Write([]byte(`{"allowed":true,"status":"open"}`))
			}))
			t.Cleanup(srv.Close)
			q := query()
			q.LegalEntityID = tc.queryID
			c := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: tc.principal})

			_, err := New(srv.URL, nil).AP(c, q)

			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("err %v entity header %q, want %q wantErr %v", err, got, tc.want, tc.wantErr)
			}
		})
	}
}

func TestAP_DoesNotFollowRedirectsWithTheCallersIdentity(t *testing.T) {
	followed := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed = true }))
	t.Cleanup(target.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)

	_, err := New(redirector.URL, nil).AP(ctx(), query())

	if err == nil || followed {
		t.Fatalf("redirect followed %v, err %v", followed, err)
	}
}

func TestAP_SignedSendsServiceTokenForTheQueriedLegalEntity(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("receiving", priv)
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"allowed":true,"status":"open"}`))
	}))
	defer srv.Close()
	principal := tenant.Principal{TenantID: tenantID, LegalEntityID: entityID, UserID: "u-1"}
	status, err := New(srv.URL, nil).WithSigner(signer).AP(tenant.With(context.Background(), principal), query())
	if err != nil || status != domain.PeriodOpen {
		t.Fatalf("status %q err %v", status, err)
	}
	if got.Get(tenant.HeaderDuties) != "" || got.Get(tenant.HeaderTenant) != "" {
		t.Fatalf("user identity headers sent alongside the token: %v", got)
	}
	claims, err := tenant.NewServiceVerifier("close", map[string]ed25519.PublicKey{"receiving": pub}).Verify(got.Get(tenant.HeaderServiceToken))
	if err != nil || claims.Le != entityID || claims.Act != "u-1" || !claims.Principal().HasDuty("period.check") {
		t.Fatalf("claims %+v err %v", claims, err)
	}
}

const resolvedID = "00000000-0000-0000-0000-0000000000f1"

func TestResolve_SendsGetAndMapsOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		want    domain.ResolvedPeriod
		wantErr error
		anyErr  bool
	}{
		{name: "open", status: http.StatusOK, body: `{"fiscalPeriodId":"` + resolvedID + `","startDate":"2026-09-01","endDate":"2026-09-30","module":"ap","status":"open"}`, want: domain.ResolvedPeriod{FiscalPeriodID: resolvedID, Status: domain.PeriodOpen}},
		{name: "closed is an answer, not an error", status: http.StatusOK, body: `{"fiscalPeriodId":"` + resolvedID + `","status":"closed"}`, want: domain.ResolvedPeriod{FiscalPeriodID: resolvedID, Status: domain.PeriodClosed}},
		{name: "no period", status: http.StatusNotFound, body: `{"code":"period_not_found","serviceCode":"CLOSE-0005"}`, wantErr: domain.ErrFiscalPeriodNotFound},
		{name: "ambiguous", status: http.StatusConflict, body: `{"code":"period_ambiguous","serviceCode":"CLOSE-0006"}`, wantErr: domain.ErrFiscalPeriodAmbiguous},
		{name: "unknown 404 is an error", status: http.StatusNotFound, body: `{"code":"OTHER"}`, anyErr: true},
		{name: "validation failure is an error", status: http.StatusBadRequest, body: `{"serviceCode":"CLOSE-0004"}`, anyErr: true},
		{name: "server error", status: http.StatusInternalServerError, body: `{}`, anyErr: true},
		{name: "200 with a malformed id", status: http.StatusOK, body: `{"fiscalPeriodId":"p-1","status":"open"}`, anyErr: true},
		{name: "200 without status", status: http.StatusOK, body: `{"fiscalPeriodId":"` + resolvedID + `"}`, anyErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got http.Request
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = *r.Clone(context.Background())
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)

			out, err := New(srv.URL+"/", nil).Resolve(ctx(), query())

			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) || tc.wantErr == nil && (err != nil) != tc.anyErr || out != tc.want {
				t.Fatalf("got %+v err %v, want %+v err %v (any %v)", out, err, tc.want, tc.wantErr, tc.anyErr)
			}
			q := got.URL.Query()
			if got.Method != http.MethodGet || got.URL.Path != resolvePath || q.Get("ledgerId") != "l-1" || q.Get("postingDate") != "2026-09-01" || q.Get("sourceSystem") != "ap" || q.Get("fiscalPeriodId") != "" {
				t.Fatalf("request %s %s", got.Method, got.URL)
			}
			if got.Header.Get(tenant.HeaderDuties) != dutyPeriodCheck || got.Header.Get(tenant.HeaderTenant) != tenantID || got.Header.Get(tenant.HeaderLegalEntity) != entityID {
				t.Fatalf("headers %v", got.Header)
			}
		})
	}
}

func TestResolve_FailsClosedWithoutPrincipalOrSingleEntity(t *testing.T) {
	if _, err := New("http://unused", nil).Resolve(context.Background(), query()); err == nil {
		t.Fatal("expected error without principal")
	}
	q := query()
	q.LegalEntityID = ""
	c := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: entityID + "," + tenantID})
	if _, err := New("http://unused", nil).Resolve(c, q); err == nil {
		t.Fatal("expected error for a multi-entity caller without an explicit entity")
	}
}

func TestResolve_SignedSendsServiceTokenForTheQueriedLegalEntity(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("receiving", priv)
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"fiscalPeriodId":"` + resolvedID + `","status":"open"}`))
	}))
	defer srv.Close()
	principal := tenant.Principal{TenantID: tenantID, LegalEntityID: entityID, UserID: "u-1"}
	out, err := New(srv.URL, nil).WithSigner(signer).Resolve(tenant.With(context.Background(), principal), query())
	if err != nil || out.FiscalPeriodID != resolvedID {
		t.Fatalf("out %+v err %v", out, err)
	}
	if got.Get(tenant.HeaderDuties) != "" || got.Get(tenant.HeaderTenant) != "" {
		t.Fatalf("user identity headers sent alongside the token: %v", got)
	}
	claims, err := tenant.NewServiceVerifier("close", map[string]ed25519.PublicKey{"receiving": pub}).Verify(got.Get(tenant.HeaderServiceToken))
	if err != nil || claims.Le != entityID || !claims.Principal().HasDuty("period.check") {
		t.Fatalf("claims %+v err %v", claims, err)
	}
}
