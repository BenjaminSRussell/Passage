package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	"github.com/BenjaminSRussell/passage/services/eam/internal/store"
)

func catalogType(r *http.Request) (string, error) {
	at := chi.URLParam(r, "assetType")
	if !assetType.MatchString(at) {
		return "", invalid("assetType must match ^[A-Z0-9_]{2,20}$")
	}
	return at, nil
}

func etag(w http.ResponseWriter, version int) {
	w.Header().Set("ETag", `"`+strconv.Itoa(version)+`"`)
}

func (h *Handler) listFaultCatalogs(w http.ResponseWriter, r *http.Request) {
	out, err := h.store.ListFaultCatalogs(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

// getFaultCatalog returns an asset type's fault catalog; its ETag is the version PUT expects in If-Match.
func (h *Handler) getFaultCatalog(w http.ResponseWriter, r *http.Request) {
	at, err := catalogType(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.GetFaultCatalog(r.Context(), at)
	if err != nil {
		fail(w, r, err)
		return
	}
	etag(w, out.Version)
	httpx.JSON(w, http.StatusOK, out)
}

// putFaultCatalog replaces an asset type's fault catalog (FR-44.16). If-Match
// carries the version the designer read, "0" when the type has no catalog yet.
func (h *Handler) putFaultCatalog(w http.ResponseWriter, r *http.Request) {
	at, err := catalogType(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body domain.FaultCatalog
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if err := domain.CheckCatalog(body); err != nil {
		fail(w, r, invalid(err.Error()))
		return
	}
	out, err := h.store.PutFaultCatalog(r.Context(), at, body, version)
	if err != nil {
		fail(w, r, err)
		return
	}
	etag(w, out.Version)
	httpx.JSON(w, http.StatusOK, out)
}

type faultIn struct {
	domain.Fault
	Notes string `json:"notes,omitempty"`
}

// registerFault records the fault found on a work order (FR-44.16).
func (h *Handler) registerFault(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body faultIn
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	for _, c := range []string{body.Symptom, body.Area, body.Type, body.Cause, body.Remedy} {
		if !domain.ValidFaultCode(c) {
			fail(w, r, invalid("symptom, area, type, cause and remedy are required codes matching ^[A-Z0-9_]{1,20}$"))
			return
		}
	}
	notes := strings.TrimSpace(body.Notes)
	if len(notes) > 1000 {
		fail(w, r, invalid("notes must be at most 1000 characters"))
		return
	}
	p, _ := tenant.FromContext(r.Context())
	out, err := h.store.RegisterFault(r.Context(), id, body.Fault, notes, p.UserID, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

type downtimeIn struct {
	From       *time.Time `json:"from"`
	To         *time.Time `json:"to"`
	ResourceID string     `json:"resourceId,omitempty"`
}

// recordDowntime books a production resource stop on a work order (BR-44.5).
func (h *Handler) recordDowntime(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body downtimeIn
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if body.From == nil || body.To == nil {
		fail(w, r, invalid("from and to are required"))
		return
	}
	if body.ResourceID != "" && !ids.Valid(body.ResourceID) {
		fail(w, r, invalid("resourceId must be a UUID"))
		return
	}
	if _, err := domain.DowntimeMinutes(*body.From, *body.To); err != nil {
		fail(w, r, invalid(err.Error()))
		return
	}
	p, _ := tenant.FromContext(r.Context())
	out, err := h.store.RecordDowntime(r.Context(), id, store.DowntimeInput{ResourceID: body.ResourceID,
		From: body.From.UTC(), To: body.To.UTC(), Actor: p.UserID}, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

type triageIn struct {
	Action string `json:"action"`
	Note   string `json:"note,omitempty"`
}

// triageRequest applies TRIAGE, REJECT or DEFER to a maintenance request (FR-44.6).
func (h *Handler) triageRequest(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body triageIn
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	switch body.Action {
	case domain.TriageTriage, domain.TriageReject, domain.TriageDefer:
	default:
		fail(w, r, invalid("action must be TRIAGE, REJECT or DEFER"))
		return
	}
	note := strings.TrimSpace(body.Note)
	if len(note) > 500 {
		fail(w, r, invalid("note must be at most 500 characters"))
		return
	}
	p, _ := tenant.FromContext(r.Context())
	out, err := h.store.TriageRequest(r.Context(), id, store.TriageInput{Action: body.Action, Note: note, Actor: p.UserID}, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
