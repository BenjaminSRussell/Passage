package api_test

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

func TestCheckAcceptsReceivingServiceTokenOnly(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("close", map[string]ed25519.PublicKey{"receiving": pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	signer, _ := tenant.NewServiceSigner("receiving", priv)
	user := tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: testkit.TenantID(9), UserID: "warehouse-user"}
	open := fakeStore{rows: []domain.Row{{Module: domain.ModuleAP, Status: domain.StatusOpen}}}

	send := func(token string) *httptest.ResponseRecorder {
		r, _ := newRouter(open)
		req := httptest.NewRequest(http.MethodPost, checkPath, strings.NewReader(body("ap", "")))
		req.Header.Set(tenant.HeaderServiceToken, token)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	good, _ := signer.Mint("close", user, "")
	if rec := send(good); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"allowed":true`) {
		t.Fatalf("receiving token: %d %s", rec.Code, rec.Body.String())
	}
	misaddressed, _ := signer.Mint("ledger", user, "")
	if rec := send(misaddressed); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token for another service: %d", rec.Code)
	}
	if rec := send(good); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed token: %d", rec.Code)
	}
}
