package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/pki"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

// WithIssuer sets the certificate issuer enrollment and renewal sign with. Without one both answer
// SF-0173 (503) and the rest of the service is unaffected.
func (a *API) WithIssuer(i pki.Issuer) *API {
	a.issuer = i
	return a
}

// registerEnrollment mounts the routes of the connector's certificate life (FR-25.19, BR-25.14): a
// supervisor mints a one-time token (the configure duty) and the connector renews with its bound
// identity (the connect duty). Redeeming the token is RegisterPublic.
func (a *API) registerEnrollment(r chi.Router) {
	r.With(authz.RequireDuty(DutyEdgeConfigure)).Post("/v1/shopfloor/edge-nodes/{id}:enrollment-token", a.mintEnrollmentToken)
	r.With(authz.RequireDuty(DutyEdgeConnect)).Post("/v1/shopfloor/edge-nodes/{id}:renew-certificate", a.renewCertificate)
}

// RegisterPublic mounts the one route that carries no identity: a connector redeeming its enrollment
// token. The token in the body is the credential; it names its own tenant, so the route is mounted on a
// router without the tenant middleware (see cmd/shopfloor), and the gateway lets it through without a
// session (a deployment handoff, see NOT-IMPLEMENTED.md). Every refusal of a token is the same
// SF-0171, so a caller learns nothing about which tokens exist.
func (a *API) RegisterPublic(r chi.Router) {
	r.Post("/v1/shopfloor/edge-nodes/{id}:enroll", a.enrollEdge)
}

func (a *API) mintEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "minting an enrollment token")
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodeEdgeNotFound, "edge node")
	if !ok {
		return
	}
	out, err := a.store.IssueEnrollmentToken(r.Context(), id, user, a.now().UTC())
	if err != nil {
		a.fail(w, r, "mint enrollment token", err)
		return
	}
	a.enrollTokens.Add(1)
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusCreated, out)
}

type enrollRequest struct {
	Token string `json:"token"`
	CSR   string `json:"csr"`
}

func (req enrollRequest) check() map[string]string {
	fields := map[string]string{}
	if strings.TrimSpace(req.Token) == "" {
		fields["token"] = "is required"
	}
	if c := strings.TrimSpace(req.CSR); c == "" || len(c) > 16<<10 {
		fields["csr"] = "is required (a PEM certificate request, at most 16 KiB)"
	}
	return fields
}

func (a *API) enrollEdge(w http.ResponseWriter, r *http.Request) {
	var req enrollRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := req.check(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	// The token names its node; a path that names another is the same refusal as a bad token.
	if _, edgeID, _, ok := store.ParseEnrollmentToken(req.Token); !ok || !strings.EqualFold(edgeID, chi.URLParam(r, "id")) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, store.CodeEnrollmentInvalid, "the enrollment token is not valid; ask a supervisor for a new one"))
		return
	}
	out, err := a.store.Enroll(r.Context(), req.Token, req.CSR, a.issuer, a.now().UTC())
	if err != nil {
		a.fail(w, r, "enroll edge node", err)
		return
	}
	a.enrolled.Add(1)
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) renewCertificate(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeEdgeNotFound, "edge node")
	if !ok {
		return
	}
	var req struct {
		CSR string `json:"csr"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if c := strings.TrimSpace(req.CSR); c == "" || len(c) > 16<<10 {
		validationFailed(w, r, map[string]string{"csr": "is required (a PEM certificate request, at most 16 KiB)"})
		return
	}
	out, err := a.store.RenewCertificate(r.Context(), id, connectorSubject(r), req.CSR, a.issuer, time.Now().UTC())
	if err != nil {
		a.fail(w, r, "renew edge certificate", err)
		return
	}
	a.certRenewed.Add(1)
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, out)
}
