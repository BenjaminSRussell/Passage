package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

// DutyHold is "Hold the report library" (FR-15.8): place and release a legal
// hold on a legal entity's report library. It is new in slice 17 and not seeded
// yet (NOT-IMPLEMENTED.md, authz seed).
const DutyHold = "reporting.library.hold"

type placeHoldRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	Reason        string `json:"reason"`
}

type releaseHoldRequest struct {
	Note string `json:"note"`
}

// placeHold puts the report library of a legal entity under legal hold. The
// Idempotency-Key header is required; the same request again returns the
// stored hold with 200.
func (a *API) placeHold(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0003", "Idempotency-Key header is required"))
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", "unreadable body"))
		return
	}
	var req placeHoldRequest
	if err := decodeStrict(raw, &req); err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", err.Error()))
		return
	}
	sum := sha256.Sum256(raw)
	h, err := a.store.PlaceHold(r.Context(), store.HoldSpec{LegalEntityID: req.LegalEntityID, Reason: req.Reason, Actor: p.UserID,
		IdempotencyKey: key, RequestHash: hex.EncodeToString(sum[:])})
	if err != nil {
		fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if h.Replayed {
		status = http.StatusOK
	}
	w.Header().Set("Location", "/v1/reporting/legal-holds/"+h.ID)
	w.Header().Set("ETag", etag(h.Revision))
	httpx.JSON(w, status, h)
}

func holdID(w http.ResponseWriter, r *http.Request, id string) (string, bool) {
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeHoldNotFound, "legal hold "+id+" does not exist"))
		return "", false
	}
	return id, true
}

// holdAction dispatches POST /legal-holds/{id}:release. The body may carry a
// note; If-Match carries the revision read.
func (a *API) holdAction(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	rest := chi.URLParam(r, "*")
	i := strings.LastIndex(rest, ":")
	if i < 0 || rest[i+1:] != "release" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "COM-0404", "unknown legal hold action"))
		return
	}
	id, ok := holdID(w, r, rest[:i])
	if !ok {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", "unreadable body"))
		return
	}
	var body releaseHoldRequest
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := decodeStrict(raw, &body); err != nil {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", err.Error()))
			return
		}
	}
	ifMatch, err := parseIfMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	h, err := a.store.ReleaseHold(r.Context(), id, ifMatch, body.Note, p.UserID)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(h.Revision))
	httpx.JSON(w, http.StatusOK, h)
}

// listHolds lists the legal holds of the caller's legal-entity scope, newest
// first; ?active=true keeps those in force.
func (a *API) listHolds(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	active := false
	if v := r.URL.Query().Get("active"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			badQuery(w, r, "active must be true or false")
			return
		}
		active = b
	}
	items, err := a.store.ListHolds(r.Context(), active)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getHold(w http.ResponseWriter, r *http.Request) {
	id, ok := holdID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	h, err := a.store.GetHold(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(h.Revision))
	httpx.JSON(w, http.StatusOK, h)
}

// listSweeps lists the passes of the library retention sweep, newest first.
func (a *API) listSweeps(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	limit := store.MaxSweepPage
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > store.MaxSweepPage {
			badQuery(w, r, fmt.Sprintf("limit must be 1 to %d", store.MaxSweepPage))
			return
		}
		limit = n
	}
	items, err := a.store.ListSweeps(r.Context(), limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}
