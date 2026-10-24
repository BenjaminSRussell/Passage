package identity

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func TestJWTVerifierRequiresHTTPSForRemoteIdentityEndpoints(t *testing.T) {
	tests := []struct {
		name    string
		issuer  string
		jwksURL string
		wantErr bool
	}{
		{name: "remote HTTP issuer", issuer: "http://id.example/tenant-a", jwksURL: "https://id.example/keys", wantErr: true},
		{name: "remote HTTP JWKS", issuer: "https://id.example/tenant-a", jwksURL: "http://keys.example/jwks", wantErr: true},
		{name: "loopback HTTP JWKS for local development", issuer: "https://id.example/tenant-a", jwksURL: "http://127.0.0.1:8080/jwks"},
		{name: "HTTPS endpoints", issuer: "https://id.example/tenant-a", jwksURL: "https://id.example/keys"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewJWTVerifier(tt.issuer, "passage-api", "tenant-a", tt.jwksURL, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewJWTVerifier() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestJWTMiddlewareVerifiesClaimsAndBindsTrustedPrincipal(t *testing.T) {
	verifier, privateKey, jwks := newTestJWTVerifier(t)
	defer jwks.Close()

	t.Run("valid claims", func(t *testing.T) {
		token := signTestJWT(t, privateKey, "key-1", testClaims())
		handler := verifier.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := tenant.FromContext(r.Context())
			if !ok || principal.TenantID != "tenant-a" || principal.UserID != "user-a" || len(principal.Duties) != 0 {
				t.Errorf("principal = %+v, present=%v", principal, ok)
			}
			if verified, _ := r.Context().Value(verifiedPrincipalKey{}).(bool); !verified {
				t.Error("principal lacks verified marker")
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set(tenant.HeaderTenant, "tenant-a")
		req.Header.Set(tenant.HeaderUser, "user-a")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
		}
	})

	invalid := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "wrong issuer", mutate: func(c map[string]any) { c["iss"] = "https://attacker.example" }},
		{name: "wrong audience", mutate: func(c map[string]any) { c["aud"] = "other-api" }},
		{name: "expired", mutate: func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }},
		{name: "lifetime exceeds access token policy", mutate: func(c map[string]any) { c["exp"] = time.Now().Add(11 * time.Minute).Unix() }},
		{name: "not active", mutate: func(c map[string]any) { c["nbf"] = time.Now().Add(time.Minute).Unix() }},
		{name: "wrong tenant", mutate: func(c map[string]any) { c["tid"] = "tenant-b" }},
		{name: "wrong subject", mutate: func(c map[string]any) { c["sub"] = "" }},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			claims := testClaims()
			tt.mutate(claims)
			token := signTestJWT(t, privateKey, "key-1", claims)
			resp := invokeJWT(t, verifier, token, "tenant-a")
			if resp.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
			}
		})
	}

	t.Run("bad signature", func(t *testing.T) {
		otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		token := signTestJWT(t, otherKey, "key-1", testClaims())
		if resp := invokeJWT(t, verifier, token, "tenant-a"); resp.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
		}
	})

	t.Run("unsupported algorithm", func(t *testing.T) {
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","kid":"key-1"}`))
		claims, err := json.Marshal(testClaims())
		if err != nil {
			t.Fatal(err)
		}
		token := header + "." + base64.RawURLEncoding.EncodeToString(claims) + "."
		if resp := invokeJWT(t, verifier, token, "tenant-a"); resp.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
		}
	})

	t.Run("gateway tenant mismatch", func(t *testing.T) {
		token := signTestJWT(t, privateKey, "key-1", testClaims())
		if resp := invokeJWT(t, verifier, token, "tenant-b"); resp.Code != http.StatusNotFound {
			t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
		}
	})
}

func TestJWTMiddlewareReturnsUnavailableWhenJWKSIsDown(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	verifier, err := NewJWTVerifier("https://id.example/realms/tenant-a", "passage-api", "tenant-a", server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	resp := invokeJWT(t, verifier, signTestJWT(t, key, "key-1", testClaims()), "tenant-a")
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func TestRequireOPADeniesUnverifiedContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("OPA should not be called for an unverified principal")
	}))
	defer server.Close()
	client, err := NewOPAClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	handler := RequireOPA(client, http.MethodPut, "/v1/vendors")(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	)
	req := httptest.NewRequest(http.MethodPut, "/v1/vendors/1", nil)
	req = req.WithContext(tenant.With(context.Background(), tenant.Principal{TenantID: "tenant-a", UserID: "user-a"}))
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

func newTestJWTVerifier(t *testing.T) (*JWTVerifier, *rsa.PrivateKey, *httptest.Server) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	modulus := base64.RawURLEncoding.EncodeToString(key.N.Bytes())
	exponent := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwksDocument{Keys: []jwk{{KeyType: "RSA", KeyID: "key-1", Use: "sig", Alg: "RS256", Modulus: modulus, Exponent: exponent}}})
	}))
	verifier, err := NewJWTVerifier("https://id.example/realms/tenant-a", "passage-api", "tenant-a", jwks.URL, jwks.Client())
	if err != nil {
		jwks.Close()
		t.Fatal(err)
	}
	return verifier, key, jwks
}

func testClaims() map[string]any {
	now := time.Now()
	return map[string]any{
		"iss": "https://id.example/realms/tenant-a", "sub": "user-a", "tid": "tenant-a",
		"aud": []string{"passage-api"}, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
	}
}

func signTestJWT(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(jwtHeader{Algorithm: "RS256", KeyID: kid})
	if err != nil {
		t.Fatal(err)
	}
	claimBytes, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claimBytes)
	digest := sha256.Sum256([]byte(encoded))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return encoded + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func invokeJWT(t *testing.T, verifier *JWTVerifier, token, tenantID string) *httptest.ResponseRecorder {
	t.Helper()
	handler := verifier.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if tenantID != "" {
		req.Header.Set(tenant.HeaderTenant, tenantID)
	}
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	return resp
}

func TestJWTClaimsAudienceAcceptsBothStandardForms(t *testing.T) {
	for _, raw := range []string{`"passage-api"`, `["other","passage-api"]`} {
		if !hasAudience(json.RawMessage(raw), "passage-api") {
			t.Errorf("hasAudience(%s) = false", strconv.Quote(raw))
		}
	}
	if hasAudience(json.RawMessage(`{"aud":"passage-api"}`), "passage-api") {
		t.Fatal("object audience must be rejected")
	}
}

func TestJWTAuthorizationHeaderRequiresBearerScheme(t *testing.T) {
	verifier, _, jwks := newTestJWTVerifier(t)
	defer jwks.Close()
	handler := verifier.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, value := range []string{"", "Basic abc", "Bearer", "bearer token", "Bearer  token"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", value)
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q status = %d", strings.TrimSpace(value), resp.Code)
		}
	}
}
