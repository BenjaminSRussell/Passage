package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// The gateway below is the devgateway logic (services/identity/cmd/devgateway) hardened for a listener that is not loopback-only:
// canonical paths only, identity headers stripped from the client and rebuilt from the verified token, the legal-entity
// selection checked against the token, and the web app served from the embedded build for every non-/api path.

const (
	headerTier       = "X-Passage-Tier"
	identityHdrStart = "X-Passage-"
)

type tokenClaims struct {
	LegalEntity string `json:"le"`
	Duties      string `json:"duties"`
	Tier        string `json:"tier"`
}

type selectionKey struct{}

type selection struct{ legalEntity, scope string }

// Guard authenticates and authorizes a request. In production it is verifier.Middleware(RequireOPAForRequest(opa)).
type Guard func(http.Handler) http.Handler

// HealthFunc reports aggregated supervisor health.
type HealthFunc func() (ready bool, statuses []Status)

// NewGateway builds the handler. web may be nil (API only).
func NewGateway(guard Guard, upstreams map[string]*url.URL, web fs.FS, health HealthFunc) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			target := upstreams[serviceOf(pr.In.URL.Path)]
			pr.SetURL(target)
			pr.Out.Host = target.Host
			setIdentity(pr)
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
		selected, _ := r.Context().Value(selectionKey{}).(selection)
		if selected.legalEntity != "" && selected.legalEntity != claimsOf(r.Header.Get("Authorization")).LegalEntity {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity selection is outside the token's scope"))
			return
		}
		proxy.ServeHTTP(w, r)
	})
	guarded := guard(routed)
	static := staticHandler(web)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ready, statuses := health()
		code := http.StatusOK
		if !ready {
			code = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"ready": ready, "services": statuses})
	})
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trimmed, ok := strings.CutPrefix(r.URL.Path, "/api")
		if !ok || !strings.HasPrefix(trimmed, "/") {
			static.ServeHTTP(w, r)
			return
		}
		selected := selection{legalEntity: r.Header.Get(tenant.HeaderWebLegalEntity), scope: r.Header.Get(tenant.HeaderLegalEntityScopeAll)}
		forwarded := r.Clone(context.WithValue(r.Context(), selectionKey{}, selected))
		stripIdentity(forwarded.Header)
		forwarded.URL.Path = trimmed
		guarded.ServeHTTP(w, forwarded)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !canonicalPath(r) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "GW-0400", "request path is not canonical"))
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

// staticHandler serves the embedded web build, falling back to index.html for client-side routes.
func staticHandler(web fs.FS) http.Handler {
	if web == nil {
		return http.NotFoundHandler()
	}
	files := http.FileServerFS(web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if f, err := web.Open(name); err == nil {
			_ = f.Close()
			files.ServeHTTP(w, r)
			return
		}
		if path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		index, err := fs.ReadFile(web, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}

func canonicalPath(r *http.Request) bool {
	p := r.URL.Path
	return r.URL.RawPath == "" && strings.HasPrefix(p, "/") && path.Clean(p) == p
}

func serviceOf(p string) string {
	rest, ok := strings.CutPrefix(p, "/v1/")
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

// setIdentity runs inside Rewrite, after ReverseProxy has removed the hop-by-hop headers, so a client's Connection
// header cannot name an identity header and have it dropped on the way to the service.
func setIdentity(pr *httputil.ProxyRequest) {
	stripIdentity(pr.Out.Header)
	pr.Out.Header.Del("Authorization")
	principal, ok := tenant.FromContext(pr.In.Context())
	if !ok {
		return
	}
	claims := claimsOf(pr.In.Header.Get("Authorization"))
	pr.Out.Header.Set(tenant.HeaderTenant, principal.TenantID)
	pr.Out.Header.Set(tenant.HeaderUser, principal.UserID)
	pr.Out.Header.Set(tenant.HeaderLegalEntity, claims.LegalEntity)
	pr.Out.Header.Set(tenant.HeaderDuties, claims.Duties)
	pr.Out.Header.Set(headerTier, claims.Tier)
	selected, _ := pr.In.Context().Value(selectionKey{}).(selection)
	if selected.legalEntity != "" {
		pr.Out.Header.Set(tenant.HeaderWebLegalEntity, selected.legalEntity)
	}
	if selected.scope != "" {
		pr.Out.Header.Set(tenant.HeaderLegalEntityScopeAll, selected.scope)
	}
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
