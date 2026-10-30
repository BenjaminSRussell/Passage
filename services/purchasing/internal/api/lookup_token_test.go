package api

import (
	"crypto/ed25519"
	"net/http"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

const orderPath = "/v1/procurement/purchase-orders/" + planPO

type lookupRig struct {
	h      *harness
	signer *tenant.ServiceSigner
}

func newLookupRig(t *testing.T) *lookupRig {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner("receiving", priv)
	if err != nil {
		t.Fatal(err)
	}
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("purchasing", map[string]ed25519.PublicKey{"receiving": pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	h := newHarness(t)
	h.fake.poByID = map[string]store.PurchaseOrder{planPO: {ID: planPO, RowVersion: 3}}
	return &lookupRig{h: h, signer: signer}
}

func (r *lookupRig) token(t *testing.T, signer *tenant.ServiceSigner, duties []string) map[string]string {
	t.Helper()
	token, err := signer.MintDuties("purchasing", tenant.Principal{TenantID: tenantA, LegalEntityID: leA, UserID: userA}, leA, duties)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{tenant.HeaderServiceToken: token}
}

func TestLookupToken_ReadsTheOrderAndItsPlansForAUserWithoutInquire(t *testing.T) {
	r := newLookupRig(t)
	for _, path := range []string{orderPath, orderPath + "/invoicing-plans"} {
		rec := r.h.do(call{method: "GET", path: path, headers: r.token(t, r.signer, []string{dutyPurchaseOrderLookup})})
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status %d: %s", path, rec.Code, rec.Body)
		}
	}
}

func TestLookupToken_ReachesNoOtherRoute(t *testing.T) {
	r := newLookupRig(t)
	routes := []struct{ method, path string }{
		{"GET", "/v1/procurement/purchase-orders"},
		{"GET", orderPath + "/versions"},
		{"GET", planPath},
		{"GET", "/v1/procurement/scheduling-agreements"},
		{"POST", "/v1/procurement/purchase-orders"},
		{"PATCH", orderPath},
		{"POST", orderPath + ":approve"},
		{"PUT", settlePath},
		{"PUT", planPath},
		{"DELETE", planPath},
	}
	for _, route := range routes {
		rec := r.h.do(call{method: route.method, path: route.path, body: `{}`, headers: r.token(t, r.signer, []string{dutyPurchaseOrderLookup})})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with the lookup duty: status %d, want 403", route.method, route.path, rec.Code)
		}
	}
}

func TestLookupToken_SettleDutyDoesNotOpenTheReads(t *testing.T) {
	r := newLookupRig(t)
	for _, path := range []string{orderPath, orderPath + "/invoicing-plans"} {
		rec := r.h.do(call{method: "GET", path: path, headers: r.token(t, r.signer, []string{dutyPlanRowSettle})})
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s with only the settle duty: status %d, want 403", path, rec.Code)
		}
	}
}

func TestLookupToken_ForgedExpiredAndMisaddressedTokensAreRefused(t *testing.T) {
	r := newLookupRig(t)
	_, strangerKey, _ := ed25519.GenerateKey(nil)
	stranger, _ := tenant.NewServiceSigner("receiving", strangerKey)
	past := r.signer.WithClock(func() time.Time { return time.Now().Add(-2 * time.Minute) })
	closeToken, err := r.signer.Mint("close", tenant.Principal{TenantID: tenantA, LegalEntityID: leA, UserID: userA}, leA)
	if err != nil {
		t.Fatal(err)
	}
	tampered := r.token(t, r.signer, []string{dutyPurchaseOrderLookup})
	good := tampered[tenant.HeaderServiceToken]
	tampered[tenant.HeaderServiceToken] = good[:len(good)-3] + "AAA"
	tokens := map[string]map[string]string{
		"forged":       r.token(t, stranger, []string{dutyPurchaseOrderLookup}),
		"expired":      r.token(t, past, []string{dutyPurchaseOrderLookup}),
		"tampered":     tampered,
		"other target": {tenant.HeaderServiceToken: closeToken},
	}
	for name, headers := range tokens {
		rec := r.h.do(call{method: "GET", path: orderPath, duties: []string{dutyPurchaseOrderInquire}, headers: headers})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s token: status %d, want 401 even with user duty headers", name, rec.Code)
		}
	}
}

func TestLookupToken_ReplayIsRefused(t *testing.T) {
	r := newLookupRig(t)
	headers := r.token(t, r.signer, []string{dutyPurchaseOrderLookup})
	if rec := r.h.do(call{method: "GET", path: orderPath, headers: headers}); rec.Code != http.StatusOK {
		t.Fatalf("first use: %d", rec.Code)
	}
	if rec := r.h.do(call{method: "GET", path: orderPath, headers: headers}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replay: %d, want 401", rec.Code)
	}
}
