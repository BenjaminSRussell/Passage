package api_test

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/numbering/internal/store"
)

func TestAllocateAcceptsPartyServiceTokenWithoutUserDuties(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("numbering", map[string]ed25519.PublicKey{"party": pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	signer, _ := tenant.NewServiceSigner("party", priv)
	user := tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: testkit.TenantID(9), UserID: "clerk"}
	r, _ := newRouter(&fakeStore{issued: store.Issued{Value: 1, Formatted: "INV-000001", Sequence: "INV", LegalEntityID: testkit.TenantID(9)}})

	send := func(aud string) *httptest.ResponseRecorder {
		token, _ := signer.Mint(aud, user, "")
		req := httptest.NewRequest(http.MethodPost, nextPath, nil)
		req.Header.Set(tenant.HeaderServiceToken, token)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	if rec := send("numbering"); rec.Code != http.StatusOK {
		t.Fatalf("party token: %d %s", rec.Code, rec.Body.String())
	}
	if rec := send("ledger"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("mis-addressed token (no party->ledger grant mints nothing): %d", rec.Code)
	}
}
