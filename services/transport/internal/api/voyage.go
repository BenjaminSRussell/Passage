package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

var voyageStatuses = []string{domain.VoyageEstimated, domain.VoyagePosted}

func (a *API) createVoyage(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var in domain.Voyage
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	// Status, allocations and the audit fields are server-owned.
	v := domain.NormalizeVoyage(in)
	if fields := domain.ValidateVoyage(v); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if fields := domain.ContainerErrors(v); len(fields) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeContainerCheck, "a container number fails the ISO 6346 check digit")
		p.Errors = fields
		httpx.Fail(w, r, p)
		return
	}
	created, err := a.store.CreateVoyage(r.Context(), v, idem)
	if err != nil {
		a.fail(w, r, "create voyage failed", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *API) listVoyages(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	f := store.VoyageFilter{Status: r.URL.Query().Get("status")}
	if f.Status != "" && !slices.Contains(voyageStatuses, f.Status) {
		validationFailed(w, r, map[string]string{"status": "must be one of " + strings.Join(voyageStatuses, ", ")})
		return
	}
	voyages, err := a.store.ListVoyages(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list voyages failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": voyages})
}

func (a *API) getVoyage(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		httpx.Fail(w, r, store.NotFound("landed-cost voyage"))
		return
	}
	v, err := a.store.GetVoyage(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get voyage failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// allocateVoyage allocates and posts the voyage's landed cost (FR-34.9). A
// POSTED voyage is returned unchanged, so a retried call is safe.
func (a *API) allocateVoyage(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		httpx.Fail(w, r, store.NotFound("landed-cost voyage"))
		return
	}
	v, err := a.store.AllocateVoyage(r.Context(), id, a.now())
	if err != nil {
		a.fail(w, r, "allocate voyage failed", err)
		return
	}
	a.voyagesPosted.Add(1)
	httpx.JSON(w, http.StatusOK, v)
}
