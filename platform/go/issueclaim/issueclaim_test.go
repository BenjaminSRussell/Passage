package issueclaim_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/issueclaim"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	tenantID = "11111111-1111-4111-8111-111111111111"
	entityID = "22222222-2222-4222-8222-222222222222"
	itemID   = "44444444-4444-4444-8444-444444444444"
)

func d(n int64) decimal.Decimal { return decimal.NewFromInt(n) }

func principalCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: entityID, UserID: "picker"})
}

func newClient(t *testing.T, handler http.HandlerFunc) *issueclaim.Client {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("warehouse", priv)
	if err != nil {
		t.Fatal(err)
	}
	verifier := tenant.NewServiceVerifier("inventory", map[string]ed25519.PublicKey{"warehouse": pub})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
		if err != nil || len(claims.Duties) != 1 || claims.Duties[0] != "inventory.issue.claim" {
			t.Errorf("token %v duties %v", err, claims.Duties)
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	c, err := issueclaim.New(srv.URL, signer, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func claim(key string, qty int64) issueclaim.Claim {
	return issueclaim.Claim{LegalEntityID: entityID, ProductID: itemID, Warehouse: "WH-1", Batch: "B1", Kind: issueclaim.Pick, Qty: d(qty), Key: key}
}

func TestClientClaim_SendsTheSignedClaimAndMapsTheAnswer(t *testing.T) {
	var path, key string
	var body map[string]string
	status, code := http.StatusCreated, ""
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		path, key = r.URL.Path, r.Header.Get("Idempotency-Key")
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"code":"` + code + `"}`))
	})
	if err := c.Claim(principalCtx(), claim("k1", 2)); err != nil {
		t.Fatal(err)
	}
	if path != "/v1/inventory/issue-claims" || key != "k1" || body["productId"] != itemID || body["warehouse"] != "WH-1" || body["batch"] != "B1" ||
		body["kind"] != "PICK" || body["qty"] != "2" || body["legalEntityId"] != entityID {
		t.Fatalf("request %s %s %v", path, key, body)
	}
	status, code = http.StatusUnprocessableEntity, "QUALITY_HOLD"
	if err := c.Claim(principalCtx(), claim("k2", 2)); !errors.Is(err, issueclaim.ErrHeld) {
		t.Fatalf("hold refusal: %v", err)
	}
	status, code = http.StatusUnprocessableEntity, "INV-NEG-001"
	if err := c.Claim(principalCtx(), claim("k3", 2)); err == nil || issueclaim.Refused(err) {
		t.Fatalf("other 422 must be an outage, not a refusal: %v", err)
	}
	status = http.StatusInternalServerError
	if err := c.Claim(principalCtx(), claim("k4", 2)); err == nil || issueclaim.Refused(err) {
		t.Fatalf("500 must be an outage: %v", err)
	}
}

func TestClientClaim_RefusesAnItemThatIsNotAProductWithoutCalling(t *testing.T) {
	called := false
	c := newClient(t, func(http.ResponseWriter, *http.Request) { called = true })
	cl := claim("k", 1)
	cl.ProductID = "BOLT"
	if err := c.Claim(principalCtx(), cl); !errors.Is(err, issueclaim.ErrNotProduct) || called {
		t.Fatalf("non-UUID item: %v called=%t", err, called)
	}
}

func TestClientRelease_PostsTheKey(t *testing.T) {
	var path, key string
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		path, key = r.URL.Path, r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusNoContent)
	})
	c.Release(principalCtx(), claim("k9", 1))
	if path != "/v1/inventory/issue-claims:release" || key != "k9" {
		t.Fatalf("release %s %s", path, key)
	}
}

func TestFromEnv(t *testing.T) {
	c, err := issueclaim.FromEnv(func(string) string { return "" })
	if c != nil || err != nil {
		t.Fatalf("unset URL: %v %v", c, err)
	}
	if _, err := issueclaim.FromEnv(func(k string) string {
		if k == "INVENTORY_URL" {
			return "http://inventory:8080"
		}
		return ""
	}); err == nil {
		t.Fatal("URL without a signing key must fail")
	}
}

func TestClaimAll_MergesAKeyAndWithdrawsEarlierClaimsOnRefusal(t *testing.T) {
	f := &issueclaim.Fake{}
	a, b := ids.New(), ids.New()
	f.Set(a, d(10), d(8))
	f.Set(b, d(10), d(10))
	mk := func(product, key string, qty int64) issueclaim.Claim {
		return issueclaim.Claim{LegalEntityID: entityID, ProductID: product, Warehouse: "W", Kind: issueclaim.Ship, Qty: d(qty), Key: key}
	}
	if _, err := issueclaim.ClaimAll(context.Background(), f, []issueclaim.Claim{mk(a, "a", 1), mk(a, "a", 2)}); !errors.Is(err, issueclaim.ErrHeld) {
		t.Fatalf("two lines of 1+2 against 2 free: %v", err)
	}
	if !f.Claimed(a, issueclaim.Ship).IsZero() {
		t.Fatalf("a refused merge left %s claimed", f.Claimed(a, issueclaim.Ship))
	}
	undo, err := issueclaim.ClaimAll(context.Background(), f, []issueclaim.Claim{mk(a, "a", 1), mk(a, "a", 1)})
	if err != nil || !f.Claimed(a, issueclaim.Ship).Equal(d(2)) {
		t.Fatalf("1+1 against 2 free: %v claimed %s", err, f.Claimed(a, issueclaim.Ship))
	}
	undo()
	if !f.Claimed(a, issueclaim.Ship).IsZero() {
		t.Fatal("undo left a claim")
	}
	if _, err := issueclaim.ClaimAll(context.Background(), f, []issueclaim.Claim{mk(a, "a2", 1), mk(b, "b", 1)}); !errors.Is(err, issueclaim.ErrHeld) {
		t.Fatalf("second product fully held: %v", err)
	}
	if !f.Claimed(a, issueclaim.Ship).IsZero() {
		t.Fatal("the first product's claim survived the second's refusal")
	}
}
