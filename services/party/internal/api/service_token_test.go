package api_test

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

const vendorLookupPath = "/v1/party/vendors/" + knownID

type partyRig struct {
	handler  http.Handler
	receiver *tenant.ServiceSigner
	payables *tenant.ServiceSigner
}

func newPartyRig(t *testing.T) *partyRig {
	t.Helper()
	receivingPub, receivingKey, _ := ed25519.GenerateKey(nil)
	payablesPub, payablesKey, _ := ed25519.GenerateKey(nil)
	receiving, err := tenant.NewServiceSigner("receiving", receivingKey)
	if err != nil {
		t.Fatal(err)
	}
	payables, err := tenant.NewServiceSigner("payables", payablesKey)
	if err != nil {
		t.Fatal(err)
	}
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("party", map[string]ed25519.PublicKey{"receiving": receivingPub, "payables": payablesPub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	return &partyRig{handler: vendorRouter(&fakeVendors{}), receiver: receiving, payables: payables}
}

func (r *partyRig) call(t *testing.T, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, strings.NewReader(""))
	req.Header.Set(tenant.HeaderServiceToken, token)
	rec := httptest.NewRecorder()
	r.handler.ServeHTTP(rec, req)
	return rec
}

func mintFor(t *testing.T, signer *tenant.ServiceSigner, aud string) string {
	t.Helper()
	token, err := signer.Mint(aud, tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: testkit.TenantID(9), UserID: "clerk"}, testkit.TenantID(9))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestServiceToken_ReceivingReadsAVendorButCannotRevealBankAccounts(t *testing.T) {
	r := newPartyRig(t)
	rec := r.call(t, vendorLookupPath, mintFor(t, r.receiver, "party"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"vendorGroupId":"`+vendorGroup+`"`) {
		t.Fatalf("signed receiving lookup: %d %s", rec.Code, rec.Body)
	}
	if rec := r.call(t, vendorLookupPath+"/bank-accounts:reveal", mintFor(t, r.receiver, "party")); rec.Code != http.StatusForbidden {
		t.Fatalf("receiving holds party.read only, reveal = %d", rec.Code)
	}
}

func TestServiceToken_PayablesRevealsAndLooksUpAVendor(t *testing.T) {
	r := newPartyRig(t)
	if rec := r.call(t, vendorLookupPath+"/bank-accounts:reveal", mintFor(t, r.payables, "party")); rec.Code != http.StatusOK {
		t.Fatalf("signed payables reveal: %d %s", rec.Code, rec.Body)
	}
	// payables holds party.vendor.lookup, which reads a vendor by id and nothing wider.
	if rec := r.call(t, vendorLookupPath, mintFor(t, r.payables, "party")); rec.Code != http.StatusOK {
		t.Fatalf("payables holds the vendor lookup duty, vendor read = %d", rec.Code)
	}
	if rec := r.call(t, vendorLookupPath+"/bank-accounts", mintFor(t, r.payables, "party")); rec.Code != http.StatusForbidden {
		t.Fatalf("payables holds no party.read, bank-account list = %d", rec.Code)
	}
}

func TestServiceToken_RefusesUnsignedForeignAndReplayedTokens(t *testing.T) {
	r := newPartyRig(t)
	_, strangerKey, _ := ed25519.GenerateKey(nil)
	stranger, _ := tenant.NewServiceSigner("receiving", strangerKey)
	good := mintFor(t, r.receiver, "party")
	for name, token := range map[string]string{
		"forged":       mintFor(t, stranger, "party"),
		"other target": mintFor(t, r.receiver, "ledger"),
		"tampered":     good[:len(good)-3] + "AAA",
	} {
		if rec := r.call(t, vendorLookupPath, token); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, rec.Code)
		}
	}
	if rec := r.call(t, vendorLookupPath, good); rec.Code != http.StatusOK {
		t.Fatalf("first use: %d", rec.Code)
	}
	if rec := r.call(t, vendorLookupPath, good); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replay: %d, want 401", rec.Code)
	}
}

func TestServiceToken_UnsignedCallWithoutUserHeadersIsRefused(t *testing.T) {
	r := newPartyRig(t)
	if rec := r.call(t, vendorLookupPath, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token and no tenant headers: %d, want 401", rec.Code)
	}
}
