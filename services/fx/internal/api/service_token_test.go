package api_test

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

const rateLookupPath = "/v1/fx/rates?type=CLOSING&from=EUR&to=USD&date=2026-01-02"

type fxRig struct {
	handler http.Handler
	signer  *tenant.ServiceSigner
}

func newFxRig(t *testing.T) *fxRig {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner("payables", priv)
	if err != nil {
		t.Fatal(err)
	}
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("fx", map[string]ed25519.PublicKey{"payables": pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	return &fxRig{handler: newRouter(&fakeStore{book: bookWith(t, rate("EUR", "USD", "1.25"))}), signer: signer}
}

func (r *fxRig) token(t *testing.T, signer *tenant.ServiceSigner) string {
	t.Helper()
	le := testkit.TenantID(9)
	token, err := signer.MintDuties("fx", tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "ap-clerk"}, le, []string{"fx.rate.lookup"})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (r *fxRig) call(method, path, token string, extra map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
	req.Header.Set(tenant.HeaderServiceToken, token)
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.handler.ServeHTTP(rec, req)
	return rec
}

func TestRateLookupToken_ReadsARateForAUserWithoutViewExchangeRates(t *testing.T) {
	r := newFxRig(t)
	rec := r.call(http.MethodGet, rateLookupPath, r.token(t, r.signer), nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"rate":"1.25"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestRateLookupToken_ReachesNoOtherRoute(t *testing.T) {
	r := newFxRig(t)
	routes := []struct{ method, path string }{
		{http.MethodGet, "/v1/fx/rates/history?type=CLOSING&from=EUR&to=USD"},
		{http.MethodGet, "/v1/fx/rate-types"},
		{http.MethodGet, "/v1/fx/rate-types/CLOSING"},
		{http.MethodPost, "/v1/fx/rates"},
		{http.MethodPost, "/v1/fx/rate-types"},
		{http.MethodPut, "/v1/fx/rate-types/CLOSING"},
		{http.MethodPost, "/v1/fx/convert"},
		{http.MethodPost, "/v1/fx/providers/ecb:import"},
	}
	for _, route := range routes {
		if rec := r.call(route.method, route.path, r.token(t, r.signer), nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d, want 403", route.method, route.path, rec.Code)
		}
	}
}

func TestRateLookupToken_RefusesForgedExpiredMisaddressedAndReplayedTokens(t *testing.T) {
	r := newFxRig(t)
	_, strangerKey, _ := ed25519.GenerateKey(nil)
	stranger, _ := tenant.NewServiceSigner("payables", strangerKey)
	good := r.token(t, r.signer)
	ledgerToken, err := r.signer.Mint("ledger", tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: testkit.TenantID(9), UserID: "ap-clerk"}, "")
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{
		"forged":       r.token(t, stranger),
		"expired":      r.token(t, r.signer.WithClock(func() time.Time { return time.Now().Add(-2 * time.Minute) })),
		"tampered":     good[:len(good)-3] + "AAA",
		"other target": ledgerToken,
	}
	for name, token := range tokens {
		rec := r.call(http.MethodGet, rateLookupPath, token, map[string]string{tenant.HeaderTenant: testkit.TenantID(1), tenant.HeaderDuties: "fx.rate.view"})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401 even with user duty headers", name, rec.Code)
		}
	}
	if rec := r.call(http.MethodGet, rateLookupPath, good, nil); rec.Code != http.StatusOK {
		t.Fatalf("first use: %d", rec.Code)
	}
	if rec := r.call(http.MethodGet, rateLookupPath, good, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replay: %d, want 401", rec.Code)
	}
}
