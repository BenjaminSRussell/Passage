package api_test

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/api"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

const tokenLegalEntity = "00000000-0000-0000-0000-0000000000e1"

type tokenRig struct {
	handler http.Handler
	signer  *tenant.ServiceSigner
}

func newTokenRig(t *testing.T) *tokenRig {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner("receiving", priv)
	if err != nil {
		t.Fatal(err)
	}
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("inventory", map[string]ed25519.PublicKey{"receiving": pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	router := httpx.NewRouter()
	api.New(&fakeStore{}, store.PostParams{InventoryAccount: "1300", AdjustmentAccount: "5100", OpeningAccount: "3900"}).Register(router)
	return &tokenRig{handler: router, signer: signer}
}

func (r *tokenRig) token(t *testing.T, signer *tenant.ServiceSigner, duties []string) string {
	t.Helper()
	p := tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: tokenLegalEntity, UserID: "warehouse-user"}
	token, err := signer.MintDuties("inventory", p, tokenLegalEntity, duties)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (r *tokenRig) call(method, path, token string, extra map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
	req.Header.Set(tenant.HeaderServiceToken, token)
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.handler.ServeHTTP(rec, req)
	return rec
}

var journalDuty = []string{"inventory.ownership-change.read"}

func TestOwnershipChangeToken_ReadsJournalsForAUserWithoutInventoryRead(t *testing.T) {
	r := newTokenRig(t)
	for _, path := range []string{"/v1/inventory/journals?type=OWNERSHIP_CHANGE&status=POSTED&limit=200", "/v1/inventory/journals/" + ids.New()} {
		if rec := r.call(http.MethodGet, path, r.token(t, r.signer, journalDuty), nil); rec.Code != http.StatusOK {
			t.Errorf("GET %s: %d %s", path, rec.Code, rec.Body)
		}
	}
}

func TestOwnershipChangeToken_ReachesNoOtherRoute(t *testing.T) {
	r := newTokenRig(t)
	j := "/v1/inventory/journals/" + ids.New()
	routes := []struct{ method, path string }{
		{http.MethodPost, "/v1/inventory/journals"},
		{http.MethodPost, j + ":post"},
		{http.MethodPost, j + ":reverse"},
		{http.MethodPost, j + ":approve"},
		{http.MethodPost, j + "/lines:bulk"},
		{http.MethodGet, "/v1/inventory/onhand"},
		{http.MethodGet, "/v1/inventory/transactions"},
		{http.MethodGet, "/v1/inventory/consignment-orders"},
		{http.MethodGet, "/v1/inventory/reservations"},
	}
	for _, route := range routes {
		if rec := r.call(route.method, route.path, r.token(t, r.signer, journalDuty), nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d, want 403", route.method, route.path, rec.Code)
		}
	}
}

func TestOwnershipChangeToken_RefusesForgedExpiredMisaddressedAndReplayedTokens(t *testing.T) {
	r := newTokenRig(t)
	path := "/v1/inventory/journals/" + ids.New()
	_, strangerKey, _ := ed25519.GenerateKey(nil)
	stranger, _ := tenant.NewServiceSigner("receiving", strangerKey)
	good := r.token(t, r.signer, journalDuty)
	ledgerToken, err := r.signer.Mint("ledger", tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: tokenLegalEntity, UserID: "warehouse-user"}, "")
	if err != nil {
		t.Fatal(err)
	}
	tokens := map[string]string{
		"forged":       r.token(t, stranger, journalDuty),
		"expired":      r.token(t, r.signer.WithClock(func() time.Time { return time.Now().Add(-2 * time.Minute) }), journalDuty),
		"tampered":     good[:len(good)-3] + "AAA",
		"other target": ledgerToken,
	}
	for name, token := range tokens {
		rec := r.call(http.MethodGet, path, token, map[string]string{tenant.HeaderTenant: testkit.TenantID(1), tenant.HeaderDuties: "inventory.read"})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401 even with user duty headers", name, rec.Code)
		}
	}
	if rec := r.call(http.MethodGet, path, good, nil); rec.Code != http.StatusOK {
		t.Fatalf("first use: %d", rec.Code)
	}
	if rec := r.call(http.MethodGet, path, good, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replay: %d, want 401", rec.Code)
	}
}
