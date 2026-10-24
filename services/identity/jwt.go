package identity

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

var (
	errInvalidToken    = errors.New("invalid access token")
	errJWKSUnavailable = errors.New("JWKS unavailable")
)

type verifiedPrincipalKey struct{}

type jwk struct {
	KeyType  string `json:"kty"`
	KeyID    string `json:"kid"`
	Use      string `json:"use"`
	Alg      string `json:"alg"`
	Modulus  string `json:"n"`
	Exponent string `json:"e"`
}

type jwksDocument struct {
	Keys []jwk `json:"keys"`
}

type jwtHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
}

type jwtClaims struct {
	Issuer    string          `json:"iss"`
	Subject   string          `json:"sub"`
	TenantID  string          `json:"tid"`
	Audience  json.RawMessage `json:"aud"`
	IssuedAt  int64           `json:"iat"`
	NotBefore int64           `json:"nbf"`
	Expires   int64           `json:"exp"`
}

// JWTVerifier validates Keycloak RS256 access tokens against a tenant realm's
// configured issuer and JWKS endpoint. The URL and expected tenant are
// configuration; they are never taken from the incoming token.
type JWTVerifier struct {
	issuer   string
	audience string
	tenantID string
	jwksURL  string
	client   *http.Client

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

// NewJWTVerifier creates a verifier pinned to one tenant issuer and JWKS URL.
func NewJWTVerifier(issuer, audience, tenantID, jwksURL string, client *http.Client) (*JWTVerifier, error) {
	issuerURL, err := url.Parse(issuer)
	if err != nil || !validIdentityURL(issuerURL) {
		return nil, errors.New("invalid JWT issuer URL")
	}
	keyURL, err := url.Parse(jwksURL)
	if err != nil || !validIdentityURL(keyURL) {
		return nil, errors.New("invalid JWKS URL")
	}
	if audience == "" || tenantID == "" {
		return nil, errors.New("JWT audience and tenant ID are required")
	}
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	return &JWTVerifier{issuer: strings.TrimRight(issuer, "/"), audience: audience, tenantID: tenantID, jwksURL: jwksURL, client: client}, nil
}

// validIdentityURL requires TLS for identity-provider endpoints, except for
// loopback HTTP endpoints used by local development and tests. This prevents
// an on-path attacker from replacing JWKS keys for a remote HTTP endpoint.
func validIdentityURL(endpoint *url.URL) bool {
	if endpoint == nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return false
	}
	if endpoint.Scheme == "https" {
		return true
	}
	if endpoint.Scheme != "http" {
		return false
	}
	host := endpoint.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Middleware validates a bearer token, binds its verified sub/tid claims into
// the request context, and marks the principal as verified for RequireOPA.
func (v *JWTVerifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v == nil {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "AUTH-0502", "identity provider is unavailable"))
			return
		}
		bearer := strings.Fields(r.Header.Get("Authorization"))
		if len(bearer) != 2 || !strings.EqualFold(bearer[0], "Bearer") {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing bearer token"))
			return
		}
		principal, err := v.verify(r.Context(), bearer[1])
		if err != nil {
			if errors.Is(err, errJWKSUnavailable) {
				httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "AUTH-0502", "identity provider is unavailable"))
			} else {
				httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "invalid bearer token"))
			}
			return
		}
		if tenantHeader := r.Header.Get(tenant.HeaderTenant); tenantHeader != "" && tenantHeader != principal.TenantID {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "AUTH-0404", "resource not found"))
			return
		}
		if userHeader := r.Header.Get(tenant.HeaderUser); userHeader != "" && userHeader != principal.UserID {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "AUTH-0404", "resource not found"))
			return
		}
		ctx := context.WithValue(r.Context(), verifiedPrincipalKey{}, true)
		next.ServeHTTP(w, r.WithContext(tenant.With(ctx, principal)))
	})
}

func (v *JWTVerifier) verify(ctx context.Context, token string) (tenant.Principal, error) {
	if len(token) > 16<<10 {
		return tenant.Principal{}, errInvalidToken
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return tenant.Principal{}, errInvalidToken
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return tenant.Principal{}, errInvalidToken
	}
	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil || header.Algorithm != "RS256" || header.KeyID == "" {
		return tenant.Principal{}, errInvalidToken
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return tenant.Principal{}, errInvalidToken
	}
	var claims jwtClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return tenant.Principal{}, errInvalidToken
	}
	if claims.Issuer != v.issuer || claims.Subject == "" || claims.TenantID != v.tenantID || claims.Expires == 0 || claims.IssuedAt == 0 || !hasAudience(claims.Audience, v.audience) {
		return tenant.Principal{}, errInvalidToken
	}
	now := time.Now().Unix()
	if claims.Expires <= now || claims.Expires <= claims.IssuedAt || claims.IssuedAt > now+30 || claims.Expires-claims.IssuedAt > int64((10*time.Minute+30*time.Second)/time.Second) || (claims.NotBefore != 0 && (claims.NotBefore > now+30 || claims.NotBefore >= claims.Expires)) {
		return tenant.Principal{}, errInvalidToken
	}
	key, err := v.publicKey(ctx, header.KeyID)
	if err != nil {
		return tenant.Principal{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return tenant.Principal{}, errInvalidToken
	}
	signed := parts[0] + "." + parts[1]
	digest := sha256.Sum256([]byte(signed))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return tenant.Principal{}, errInvalidToken
	}
	return tenant.Principal{TenantID: claims.TenantID, UserID: claims.Subject}, nil
}

func hasAudience(raw json.RawMessage, want string) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single == want
	}
	var many []string
	if json.Unmarshal(raw, &many) != nil {
		return false
	}
	for _, audience := range many {
		if audience == want {
			return true
		}
	}
	return false
}

func (v *JWTVerifier) publicKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if time.Since(v.fetchedAt) >= 5*time.Minute || v.keys == nil {
		if err := v.fetchKeys(ctx); err != nil {
			return nil, err
		}
	}
	if key := v.keys[kid]; key != nil {
		return key, nil
	}
	// Unknown key IDs trigger one refresh for key rotation, but repeated
	// unknown IDs cannot force a JWKS request more often than every 15 seconds.
	if time.Since(v.fetchedAt) >= 15*time.Second {
		if err := v.fetchKeys(ctx); err != nil {
			return nil, err
		}
		if key := v.keys[kid]; key != nil {
			return key, nil
		}
	}
	return nil, errInvalidToken
}

func (v *JWTVerifier) fetchKeys(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", errJWKSUnavailable, err)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", errJWKSUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: status %d", errJWKSUnavailable, resp.StatusCode)
	}
	var document jwksDocument
	dec := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 1<<20))
	if err := dec.Decode(&document); err != nil {
		return fmt.Errorf("%w: %v", errJWKSUnavailable, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: invalid response", errJWKSUnavailable)
	}
	keys := make(map[string]*rsa.PublicKey, len(document.Keys))
	for _, entry := range document.Keys {
		if entry.KeyType != "RSA" || entry.KeyID == "" || (entry.Use != "" && entry.Use != "sig") || (entry.Alg != "" && entry.Alg != "RS256") {
			continue
		}
		modulusBytes, errN := base64.RawURLEncoding.DecodeString(entry.Modulus)
		exponentBytes, errE := base64.RawURLEncoding.DecodeString(entry.Exponent)
		if errN != nil || errE != nil || len(exponentBytes) == 0 || len(exponentBytes) > 4 {
			continue
		}
		modulus := new(big.Int).SetBytes(modulusBytes)
		exponent := 0
		for _, b := range exponentBytes {
			exponent = exponent<<8 | int(b)
		}
		if modulus.BitLen() < 2048 || exponent < 3 || exponent%2 == 0 {
			continue
		}
		if _, exists := keys[entry.KeyID]; exists {
			return fmt.Errorf("%w: duplicate key ID", errJWKSUnavailable)
		}
		keys[entry.KeyID] = &rsa.PublicKey{N: modulus, E: exponent}
	}
	if len(keys) == 0 {
		return fmt.Errorf("%w: no usable signing keys", errJWKSUnavailable)
	}
	v.keys = keys
	v.fetchedAt = time.Now()
	return nil
}
