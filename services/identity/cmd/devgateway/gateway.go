package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/identity"
)

const (
	headerTier       = "X-Passage-Tier"
	identityHdrStart = "X-Passage-"
)

type tokenClaims struct {
	LegalEntity string `json:"le"`
	Duties      string `json:"duties"`
	Tier        string `json:"tier"`
}

func newGateway(verifier *identity.JWTVerifier, opa *identity.OPAClient, upstreams map[string]*url.URL) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			target := upstreams[serviceOf(pr.In.URL.Path)]
			pr.SetURL(target)
			pr.Out.Host = target.Host
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, "GW-0502", "upstream service is unavailable"))
		},
	}
	routed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstreams[serviceOf(r.URL.Path)] == nil {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "GW-0404", "no route"))
			return
		}
		proxy.ServeHTTP(w, r)
	})
	guarded := verifier.Middleware(identity.RequireOPAForRequest(opa)(identityHeaders(routed)))
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stripIdentity(r.Header)
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api")
		guarded.ServeHTTP(w, r)
	}))
	return mux
}

func serviceOf(path string) string {
	rest, ok := strings.CutPrefix(path, "/v1/")
	if !ok {
		return ""
	}
	service, _, _ := strings.Cut(rest, "/")
	return service
}

func stripIdentity(h http.Header) {
	for name := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(name), identityHdrStart) {
			h.Del(name)
		}
	}
}

func identityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := tenant.FromContext(r.Context())
		if !ok {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "no principal"))
			return
		}
		claims := claimsOf(r.Header.Get("Authorization"))
		r.Header.Set(tenant.HeaderTenant, principal.TenantID)
		r.Header.Set(tenant.HeaderUser, principal.UserID)
		r.Header.Set(tenant.HeaderLegalEntity, claims.LegalEntity)
		r.Header.Set(tenant.HeaderDuties, claims.Duties)
		r.Header.Set(headerTier, claims.Tier)
		r.Header.Del("Authorization")
		next.ServeHTTP(w, r)
	})
}

// claimsOf reads the claims of a token the verifier has already accepted; it checks nothing itself.
func claimsOf(authorization string) tokenClaims {
	var claims tokenClaims
	fields := strings.Fields(authorization)
	if len(fields) != 2 {
		return claims
	}
	parts := strings.Split(fields[1], ".")
	if len(parts) != 3 {
		return claims
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims
	}
	_ = json.Unmarshal(payload, &claims)
	return claims
}
