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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const dutyProvisionSCIM = "Provision users via SCIM"

// TenantVerifiers authenticates bearer tokens for routes that sit outside the header-based tenant middleware.
// The token's tid claim only selects which tenant's verifier checks it: the realm (and so the issuer and JWKS
// URL) comes from the tenant registry, never from the token, and the selected verifier rejects any token whose
// issuer, audience or tid differs from that tenant's.
type TenantVerifiers struct {
	pool       *pgxpool.Pool
	issuerBase string
	audience   string
	client     *http.Client

	mu        sync.Mutex
	verifiers map[string]*JWTVerifier
}

// NewTenantVerifiers creates the per-tenant verifier set. issuerBase is the identity provider's realms URL, such
// as https://sso.example.com/realms; a tenant's issuer is issuerBase/<realm_name>.
func NewTenantVerifiers(pool *pgxpool.Pool, issuerBase, audience string, client *http.Client) (*TenantVerifiers, error) {
	base, err := url.Parse(issuerBase)
	if err != nil || !validIdentityURL(base) {
		return nil, errors.New("invalid identity provider realms URL")
	}
	if audience == "" {
		return nil, errors.New("JWT audience is required")
	}
	return &TenantVerifiers{pool: pool, issuerBase: strings.TrimRight(issuerBase, "/"), audience: audience, client: client, verifiers: map[string]*JWTVerifier{}}, nil
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
	ctx := tenant.With(r.Context(), tenant.Principal{TenantID: tenantID})
	var realm string
	err := dbx.WithTenantTx(ctx, t.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select realm_name from identity_tenants where tenant_id = $1 and status = 'active'`, tenantID).Scan(&realm)
	})
	if err != nil {
		return nil, err
	}
	issuer := t.issuerBase + "/" + url.PathEscape(realm)
	t.mu.Lock()
	defer t.mu.Unlock()
	key := tenantID + " " + issuer
	if v := t.verifiers[key]; v != nil {
		return v, nil
	}
	v, err := NewJWTVerifier(issuer, t.audience, tenantID, issuer+"/protocol/openid-connect/certs", t.client)
	if err != nil {
		return nil, err
	}
	t.verifiers[key] = v
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

// withProvisionDuty marks the principal as holding the SCIM duty once the policy has allowed the request, so the
// handlers can open the tenant-wide transaction through the same duty gate as the other admin APIs.
func withProvisionDuty(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := tenant.FromContext(r.Context())
		if !ok {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "no principal"))
			return
		}
		p.Duties = map[string]struct{}{dutyProvisionSCIM: {}}
		next.ServeHTTP(w, r.WithContext(tenant.With(r.Context(), p)))
	})
}
