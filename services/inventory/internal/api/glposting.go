package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// GL posting of the inventory subledger (#477).
const (
	dutyGLPost        = "inventory.gl.post"
	glPostingsPath    = "/v1/inventory/gl-postings"
	CodeGLUnavailable = "INV-GL-005"
)

// WithGL books the posted journals' GL entries in the general ledger through ledger.
func (a *API) WithGL(books store.GLBooks, ledger ledgerclient.Ledger) *API {
	a.gl, a.ledger = books, ledger
	return a
}

func (a *API) registerGL(r chi.Router) {
	r.With(authz.RequireDuty(dutyGLPost)).Post(glPostingsPath, a.postToGL)
	r.With(authz.RequireDuty(dutyGLPost)).Post(glPostingsPath+":requeue", a.requeueGL)
}

type glRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	LedgerID      string `json:"ledgerId"`
}

// glEntity checks the tenant, the configuration and the legal entity (a UUID in the caller's scope) of a GL request.
func (a *API) glEntity(w http.ResponseWriter, r *http.Request, req glRequest, needLedger bool) (string, bool) {
	if !validTenant(w, r) {
		return "", false
	}
	if a.gl == nil || a.ledger == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeGLUnavailable, "the general ledger is not configured for this service (LEDGER_URL)"))
		return "", false
	}
	entity := strings.ToLower(req.LegalEntityID)
	fields := map[string]string{}
	if !ids.Valid(entity) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if needLedger && !ids.Valid(req.LedgerID) {
		fields["ledgerId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return "", false
	}
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, entity) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, store.CodeOutOfScope, "the legal entity is outside your scope"))
		return "", false
	}
	return entity, true
}

// postToGL books the legal entity's posted journals that the general ledger has not confirmed yet. The answer lists what was
// booked and what failed; failures are retried by a later call once their backoff has passed.
func (a *API) postToGL(w http.ResponseWriter, r *http.Request) {
	var req glRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	entity, ok := a.glEntity(w, r, req, true)
	if !ok {
		return
	}
	res, err := a.gl.PostToGL(r.Context(), a.ledger, store.GLPostRequest{LegalEntityID: entity, LedgerID: req.LedgerID})
	if err != nil {
		a.failed(w, r, "book inventory journals in the general ledger", err)
		return
	}
	a.glBooked.Add(int64(len(res.Booked)))
	httpx.JSON(w, http.StatusOK, res)
}

// requeueGL makes the legal entity's parked postings due again.
func (a *API) requeueGL(w http.ResponseWriter, r *http.Request) {
	var req glRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	entity, ok := a.glEntity(w, r, req, false)
	if !ok {
		return
	}
	n, err := a.gl.RequeueParkedGL(r.Context(), entity)
	if err != nil {
		a.failed(w, r, "requeue parked GL postings", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]int{"requeued": n})
}
