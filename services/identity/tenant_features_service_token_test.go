package identity_test

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/features"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/identity"
)

// featuresRouter is the features route behind identity's real tenant middleware, verifying service tokens against v.
func featuresRouter(f *adminFixture, v *tenant.ServiceVerifier) http.Handler {
	r := chi.NewRouter()
	r.Use(tenant.MiddlewareWith(v))
	identity.NewTenantFeaturesAPI(f.app).Register(r)
	return r
}

func signedFeaturesRequest(t *testing.T, signer *tenant.ServiceSigner, tenantID string, duties []string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/tenant/features", nil)
	p := tenant.Principal{TenantID: tenantID, UserID: "u1"}
	if signed, err := signer.ApplyDuties(req, p, "identity", "", duties); err != nil || !signed {
		t.Fatalf("sign: %v %v", signed, err)
	}
	return req
}

// A consumer's feature read (the token features.HTTPSource signs) passes identity's real tenant middleware and
// returns that tenant's features (#556).
func TestTenantFeaturesAcceptsTheConsumersServiceToken(t *testing.T) {
	f := newAdminFixture(t)
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("processmfg", priv)
	if err != nil {
		t.Fatal(err)
	}
	verifier := tenant.NewServiceVerifierKids("identity", map[string]map[string]ed25519.PublicKey{"processmfg": {"": pub}})
	h := featuresRouter(f, verifier)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedFeaturesRequest(t, signer, f.tenantA, []string{features.DutyView}))
	body := decode[featuresBody](t, rec, http.StatusOK)
	if len(body.Features) == 0 {
		t.Fatalf("body = %+v, want the tenant's features", body)
	}

	// The token is for one read: the grant of processmfg on identity holds no other duty, so none can be signed.
	req := httptest.NewRequest(http.MethodGet, "/v1/tenant/features", nil)
	if signed, err := signer.ApplyDuties(req, tenant.Principal{TenantID: f.tenantA, UserID: "u1"}, "identity", "", []string{"identity.feature.maintain"}); err == nil || signed {
		t.Fatalf("a duty outside the grant was signed: %v %v", signed, err)
	}
}

// Identity started without service public keys refuses every service token (401); the consumer's gate then fails
// open, which is why the deploy note asks for the keys to be set (#556).
func TestTenantFeaturesRefusesEveryServiceTokenWithoutPublicKeys(t *testing.T) {
	f := newAdminFixture(t)
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("processmfg", priv)
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]*tenant.ServiceVerifier{
		"no verifier configured": nil,
		"no key for the caller":  tenant.NewServiceVerifierKids("identity", map[string]map[string]ed25519.PublicKey{}),
	} {
		rec := httptest.NewRecorder()
		featuresRouter(f, v).ServeHTTP(rec, signedFeaturesRequest(t, signer, f.tenantA, []string{features.DutyView}))
		wantProblem(t, rec, http.StatusUnauthorized, "AUTH-0001")
		_ = name
	}
}
