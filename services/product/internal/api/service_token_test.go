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

func batchCall(t *testing.T, signer *tenant.ServiceSigner, method, path, body string, headers map[string]string, only ...string) *httptest.ResponseRecorder {
	t.Helper()
	var duties []string
	if len(only) > 0 {
		duties = only
	}
	token, err := signer.MintDuties("product", tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: testkit.TenantID(9), UserID: testkit.TenantID(8)}, "", duties)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderServiceToken, token)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	newRouter(newFake()).ServeHTTP(rec, req)
	return rec
}

func TestBatchServiceTokenReadsReleasedProductsButCannotReleaseOrSmuggleUserDuties(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	batch, _ := tenant.NewServiceSigner("batch", priv)
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("product", map[string]ed25519.PublicKey{"batch": pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })

	if rec := batchCall(t, batch, http.MethodGet, "/v1/product/released-products?limit=2", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("batch read status %d: %s", rec.Code, rec.Body)
	}
	if rec := batchCall(t, batch, http.MethodPost, "/v1/product/releases", `{}`, map[string]string{tenant.HeaderDuties: "product.release", tenant.HeaderTenant: testkit.TenantID(1)}); rec.Code != http.StatusForbidden {
		t.Fatalf("batch holds no release duty and its token replaces user headers: %d %s", rec.Code, rec.Body)
	}

	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/v1/product/products"},
		{http.MethodGet, "/v1/product/products"},
		{http.MethodGet, "/v1/product/products/" + testkit.TenantID(50)},
		{http.MethodPost, "/v1/product/products/" + testkit.TenantID(50) + "/attributes"},
		{http.MethodPost, "/v1/product/products/" + testkit.TenantID(50) + "/identifiers"},
	} {
		if rec := batchCall(t, batch, c.method, c.path, `{}`, nil); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with the whole batch grant = %d; batch must not reach product's broad routes", c.method, c.path, rec.Code)
		}
	}
	if rec := batchCall(t, batch, http.MethodPost, "/v1/product/products/"+testkit.TenantID(50)+"/variants:explode", `{"selected":{}}`, nil); rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
		t.Errorf("the explode route must accept product.variants.explode, got %d", rec.Code)
	}
	if rec := batchCall(t, batch, http.MethodGet, "/v1/product/released-products", "", nil, "product.jobs.write"); rec.Code != http.StatusForbidden {
		t.Errorf("a token narrowed to jobs.write must not read released products, got %d", rec.Code)
	}

	_, strangerKey, _ := ed25519.GenerateKey(nil)
	stranger, _ := tenant.NewServiceSigner("batch", strangerKey)
	if rec := batchCall(t, stranger, http.MethodGet, "/v1/product/released-products", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a token signed with an unknown key must be 401, got %d", rec.Code)
	}
}
