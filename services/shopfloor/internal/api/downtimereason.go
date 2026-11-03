package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

// registerDowntimeReasons mounts the downtime reason code master routes (FR-25.11).
func (a *API) registerDowntimeReasons(r chi.Router) {
	r.With(authz.RequireDuty(DutySetup)).Post("/v1/shopfloor/downtime-reasons", a.createDowntimeReason)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/downtime-reasons", a.listDowntimeReasons)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/downtime-reasons/{id}", a.getDowntimeReason)
	r.With(authz.RequireDuty(DutySetup)).Put("/v1/shopfloor/downtime-reasons/{id}", a.updateDowntimeReason)
}

type downtimeReasonRequest struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Active   *bool  `json:"active"`
}

func (req downtimeReasonRequest) input(creating bool) (store.DowntimeReasonInput, map[string]string) {
	fields := map[string]string{}
	code, name := strings.TrimSpace(req.Code), strings.TrimSpace(req.Name)
	if creating && (len(code) < 1 || len(code) > maxReasonLen) {
		fields["code"] = fmt.Sprintf("must be 1 to %d characters", maxReasonLen)
	}
	if len(name) < 1 || len(name) > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if req.Category != "planned" && req.Category != "unplanned" {
		fields["category"] = "must be planned or unplanned"
	}
	return store.DowntimeReasonInput{Code: code, Name: name, Category: req.Category, Active: req.Active == nil || *req.Active}, fields
}

func (a *API) createDowntimeReason(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "maintaining a downtime reason")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req downtimeReasonRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := req.input(true)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.CreateDowntimeReason(r.Context(), in, user, idem)
	if err != nil {
		a.fail(w, r, "create downtime reason", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) updateDowntimeReason(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "maintaining a downtime reason")
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodeReasonNotFound, "downtime reason")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req downtimeReasonRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := req.input(false)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.UpdateDowntimeReason(r.Context(), id, version, in, user)
	if err != nil {
		a.fail(w, r, "update downtime reason", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getDowntimeReason(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeReasonNotFound, "downtime reason")
	if !ok {
		return
	}
	out, err := a.store.GetDowntimeReason(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get downtime reason", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listDowntimeReasons(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := limitParam(w, r, 200, 1000)
	if !ok {
		return
	}
	active, ok := activeFilter(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListDowntimeReasons(r.Context(), active, limit)
	if err != nil {
		a.fail(w, r, "list downtime reasons", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.DowntimeReason]{Items: items})
}
