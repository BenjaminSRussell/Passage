package identity

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// TenantVerifiers authenticates bearer tokens for routes that sit outside the header-based tenant middleware.
// The token's tid claim only selects which tenant's verifier checks it: the realm (and so the issuer and JWKS
// URL) comes from the tenant registry, never from the token, and the selected verifier rejects any token whose
// issuer, audience or tid differs from that tenant's.
type TenantVerifiers struct {
	pool       *pgxpool.Pool
	issuerBase string
	audience   string
	client     *http.Client

	cacheTTL time.Duration
	now      func() time.Time

	mu       sync.Mutex
	resolved map[string]resolvedTenant
}

type resolvedTenant struct {
	verifier *JWTVerifier
	expires  time.Time
}

// NewTenantVerifiers creates the per-tenant verifier set. issuerBase is the identity provider's realms URL, such
// as https://sso.example.com/realms; a tenant's issuer is issuerBase/<realm_name>. A tenant's realm and status
// are cached for cacheTTL, which is also how long a suspension can take to reach SCIM; zero disables the cache.
func NewTenantVerifiers(pool *pgxpool.Pool, issuerBase, audience string, client *http.Client, cacheTTL time.Duration) (*TenantVerifiers, error) {
	base, err := url.Parse(issuerBase)
	if err != nil || !validIdentityURL(base) {
		return nil, errors.New("invalid identity provider realms URL")
	}
	if audience == "" {
		return nil, errors.New("JWT audience is required")
	}
	return &TenantVerifiers{pool: pool, issuerBase: strings.TrimRight(issuerBase, "/"), audience: audience, client: client, cacheTTL: cacheTTL, now: time.Now, resolved: map[string]resolvedTenant{}}, nil
}

func unverifiedTenant(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(token) > 16<<10 {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false
	}
	var claims struct {
		TenantID string `json:"tid"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return "", false
	}
	return canonicalUUID(claims.TenantID)
}

func (t *TenantVerifiers) verifierFor(r *http.Request, tenantID string) (*JWTVerifier, error) {
	now := t.now()
	t.mu.Lock()
	hit, ok := t.resolved[tenantID]
	t.mu.Unlock()
	if ok && now.Before(hit.expires) {
		return hit.verifier, nil
	}
	ctx := tenant.With(r.Context(), tenant.Principal{TenantID: tenantID})
	var realm string
	err := dbx.WithTenantTx(ctx, t.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select realm_name from identity_tenants where tenant_id = $1 and status = 'active'`, tenantID).Scan(&realm)
	})
	if err != nil {
		t.mu.Lock()
		delete(t.resolved, tenantID)
		t.mu.Unlock()
		return nil, err
	}
	issuer := t.issuerBase + "/" + url.PathEscape(realm)
	t.mu.Lock()
	defer t.mu.Unlock()
	v := hit.verifier
	if !ok || v.issuer != issuer {
		if v, err = NewJWTVerifier(issuer, t.audience, tenantID, issuer+"/protocol/openid-connect/certs", t.client); err != nil {
			return nil, err
		}
	}
	t.resolved[tenantID] = resolvedTenant{verifier: v, expires: now.Add(t.cacheTTL)}
	return v, nil
}

// Middleware selects the caller's tenant verifier and runs it. A missing, malformed or unknown-tenant token is
// the same 401 so the response does not reveal which tenants exist.
func (t *TenantVerifiers) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := strings.Fields(r.Header.Get("Authorization"))
		if len(bearer) != 2 || !strings.EqualFold(bearer[0], "Bearer") {
			scimFail(w, r, errUnauthorized)
			return
		}
		tenantID, ok := unverifiedTenant(bearer[1])
		if !ok {
			scimFail(w, r, errUnauthorized)
			return
		}
		v, err := t.verifierFor(r, tenantID)
		if errors.Is(err, pgx.ErrNoRows) {
			scimFail(w, r, errUnauthorized)
			return
		}
		if err != nil {
			slog.Error("scim tenant lookup", "err", err)
			scimFail(w, r, &scimError{status: http.StatusServiceUnavailable, detail: "identity provider is unavailable"})
			return
		}
		v.Middleware(next).ServeHTTP(w, r)
	})
}

var errUnauthorized = &scimError{status: http.StatusUnauthorized, detail: "invalid bearer token"}
