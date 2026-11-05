package numbering_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/hr/internal/numbering"
)

const entityID = "00000000-0000-0000-0000-0000000000e1"

func principalContext() context.Context {
	return tenant.With(context.Background(), tenant.Principal{
		TenantID: "00000000-0000-0000-0000-000000000001", LegalEntityID: entityID, UserID: "user-1",
		Duties: map[string]struct{}{"hr.recruiting.maintain": {}},
	})
}

func testSigner() *tenant.ServiceSigner {
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("hr", priv)
	return signer
}

func wantProblem(t *testing.T, err error, status int, code string) {
	t.Helper()
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status != status || p.Code != code {
		t.Fatalf("got %v, want %d %s", err, status, code)
	}
}

func TestNext_SignedSendsOnlyTheAllocateDuty(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("hr", priv)
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		_, _ = w.Write([]byte(`{"value":7,"formatted":"REQ-000007"}`))
	}))
	defer srv.Close()
	formatted, err := numbering.New(srv.URL+"/").WithSigner(signer).Next(principalContext(), "REQ", "key-1")
	if err != nil || formatted != "REQ-000007" {
		t.Fatalf("got %q, %v", formatted, err)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/v1/numbering/sequences/REQ/next" || got.Header.Get("Idempotency-Key") != "key-1" || got.Header.Get(tenant.HeaderDuties) != "" {
		t.Fatalf("request %s %s headers %v", got.Method, got.URL.Path, got.Header)
	}
	claims, err := tenant.NewServiceVerifier("numbering", map[string]ed25519.PublicKey{"hr": pub}).Verify(got.Header.Get(tenant.HeaderServiceToken))
	if err != nil || claims.Le != entityID || claims.Act != "user-1" || !claims.Principal().HasDuty("numbering.sequence.allocate") || claims.Principal().HasDuty("numbering.write") || claims.Principal().HasDuty("hr.recruiting.maintain") {
		t.Fatalf("claims %+v err %v", claims, err)
	}
}

func TestNext_WithoutASignerIsUnavailableAndSendsNothing(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer srv.Close()
	_, err := numbering.New(srv.URL).Next(principalContext(), "REQ", "key-2")
	wantProblem(t, err, http.StatusServiceUnavailable, numbering.CodeUnavailable)
	if calls != 0 {
		t.Fatalf("numbering called %d times without a signer", calls)
	}
}

func TestNext_FailuresAreUnavailableAndNeedASingleEntity(t *testing.T) {
	for _, handler := range []http.HandlerFunc{
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not json")) },
		func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"value":1}`)) },
	} {
		srv := httptest.NewServer(handler)
		_, err := numbering.New(srv.URL).WithSigner(testSigner()).Next(principalContext(), "REQ", "k")
		srv.Close()
		wantProblem(t, err, http.StatusServiceUnavailable, numbering.CodeUnavailable)
	}
	_, err := numbering.Unavailable{}.Next(principalContext(), "REQ", "k")
	wantProblem(t, err, http.StatusServiceUnavailable, numbering.CodeUnavailable)

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer srv.Close()
	for _, entity := range []string{"", entityID + ",00000000-0000-0000-0000-0000000000e2"} {
		ctx := tenant.With(context.Background(), tenant.Principal{TenantID: "00000000-0000-0000-0000-000000000001", LegalEntityID: entity})
		_, err := numbering.New(srv.URL).Next(ctx, "REQ", "k")
		wantProblem(t, err, http.StatusUnprocessableEntity, numbering.CodeEntityRequired)
	}
	if calls != 0 {
		t.Fatalf("numbering called %d times without a single legal entity", calls)
	}
}
