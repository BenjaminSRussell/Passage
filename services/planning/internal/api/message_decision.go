package api

import (
	"bufio"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

const maxDecisionNote = 500

// messageDecisionRequest is the optional body of :apply and :dismiss.
type messageDecisionRequest struct {
	Note string `json:"note"`
}

func messageNotFound(w http.ResponseWriter, r *http.Request) {
	httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeMessageNotFound, "message not found"))
}

// getMessage reads one message with its status and ETag.
func (a *API) getMessage(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		messageNotFound(w, r)
		return
	}
	m, err := a.store.GetMessage(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etagOf(m.Version))
	httpx.JSON(w, http.StatusOK, m)
}

// applyMessage applies an open message about existing supply (FR-22.7).
func (a *API) applyMessage(w http.ResponseWriter, r *http.Request) { a.decideMessage(w, r, true) }

// dismissMessage dismisses an open message (FR-22.7).
func (a *API) dismissMessage(w http.ResponseWriter, r *http.Request) { a.decideMessage(w, r, false) }

func (a *API) decideMessage(w http.ResponseWriter, r *http.Request, apply bool) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		messageNotFound(w, r)
		return
	}
	expected, ok := ifMatch(w, r, 1)
	if !ok {
		return
	}
	key := r.Header.Get(idempotencyKey)
	if len(key) > 255 {
		validationFailed(w, r, map[string]string{idempotencyKey: "must be at most 255 characters"})
		return
	}
	var req messageDecisionRequest
	// The body is optional; a chunked request (no Content-Length) may also be empty.
	body := bufio.NewReader(r.Body)
	if _, err := body.Peek(1); err == nil {
		r.Body = io.NopCloser(body)
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	if utf8.RuneCountInString(req.Note) > maxDecisionNote {
		validationFailed(w, r, map[string]string{"note": "must be at most 500 characters"})
		return
	}
	m, replayed, err := a.store.DecideMessage(r.Context(), id, expected, store.MessageDecision{Apply: apply, Note: req.Note, IdempotencyKey: key})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("ETag", etagOf(m.Version))
	httpx.JSON(w, http.StatusOK, m)
}
