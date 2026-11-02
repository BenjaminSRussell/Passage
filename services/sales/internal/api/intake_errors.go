package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const (
	intakeErrors = "/v1/sales/intake-errors"
	// maxResolutionNote bounds the note that closes an intake error.
	maxResolutionNote = 500
	// maxExternalRef is the longest reference an order accepts; a longer one
	// is refused and logged cut to this length.
	maxExternalRef = 100
	// uiChannel is the channel of orders typed in the web app; only other
	// channels (EDI, portal, API clients) are logged when refused.
	uiChannel = "UI"
)

// registerIntakeErrors mounts the intake error routes (PRD 5.27.14).
func (a *API) registerIntakeErrors(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get(intakeErrors, a.listIntakeErrors)
	r.With(authz.RequireDuty(dutyRead)).Get(intakeErrors+"/{id}", a.getIntakeError)
	r.With(authz.RequireDuty(dutyWrite)).Post(intakeErrors+"/{id}:resolve", a.resolveIntakeError)
}

// logIntakeFailure records why a channel order was refused, best effort: a
// failure to log it is only logged and never changes the answer. Orders typed
// in the UI, requests without a valid legal entity in the caller's scope,
// server faults (status 5xx) and the 409 of a concurrent request that stored
// the same order first (a retry returns it) are not logged. A reference that
// failed validation is kept to its first maxExternalRef characters.
func (a *API) logIntakeFailure(r *http.Request, req createOrderRequest, err error) {
	channel := req.Channel
	if channel == "" || channel == uiChannel || !channelName.MatchString(channel) || !ids.Valid(req.LegalEntityID) {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	if !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		return
	}
	var prob *httpx.Problem
	if !errors.As(err, &prob) || prob.Status >= http.StatusInternalServerError || store.IsConcurrentIntake(err) {
		return
	}
	hash, herr := domain.RequestHash(req)
	if herr != nil {
		return
	}
	message := prob.Detail
	if len(prob.Errors) > 0 {
		keys := make([]string, 0, len(prob.Errors))
		for k, v := range prob.Errors {
			keys = append(keys, k+" "+v)
		}
		sort.Strings(keys)
		message += ": " + strings.Join(keys, "; ")
	}
	in := store.NewIntakeError{LegalEntityID: req.LegalEntityID, Channel: channel, RequestHash: hash, ErrorCode: prob.Code, Message: message}
	if req.ExternalRef != nil {
		in.ExternalRef = *req.ExternalRef
		if ref := []rune(in.ExternalRef); len(ref) > maxExternalRef {
			in.ExternalRef = string(ref[:maxExternalRef])
		}
	}
	if _, serr := a.store.RecordIntakeError(r.Context(), in, a.now()); serr != nil {
		slog.Error("record intake error failed", "err", serr)
	}
}

func (a *API) listIntakeErrors(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.IntakeErrorFilter{LegalEntityID: legalEntity, Channel: q.Get("channel"), Status: q.Get("status"), ErrorCode: q.Get("errorCode")}
	fields := map[string]string{}
	if f.Channel != "" && !channelName.MatchString(f.Channel) {
		fields["channel"] = "must be 1 to 40 letters, digits, - or _"
	}
	if f.Status != "" && f.Status != store.IntakeErrorOpen && f.Status != store.IntakeErrorResolvedStatus {
		fields["status"] = "must be OPEN or RESOLVED"
	}
	if len(f.ErrorCode) > 40 {
		fields["errorCode"] = "must be at most 40 characters"
	}
	limit, ok := pageLimit(q.Get("limit"))
	if !ok {
		fields["limit"] = fmt.Sprintf("must be 1 to %d", maxPageSize)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	f.Limit = limit
	items, err := a.store.ListIntakeErrors(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list intake errors", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func intakeErrorRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeIntakeErrorNotFound, "intake error not found"))
			return
		}
		next(w, r, id)
	}
}

func (a *API) getIntakeError(w http.ResponseWriter, r *http.Request) {
	intakeErrorRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		e, err := a.store.GetIntakeError(r.Context(), id)
		if err != nil {
			a.fail(w, r, "get intake error", err)
			return
		}
		setETag(w, e.Version)
		httpx.JSON(w, http.StatusOK, e)
	})(w, r)
}

type resolveIntakeErrorRequest struct {
	Note string `json:"note"`
}

// resolveIntakeError closes an OPEN intake error with a required note.
func (a *API) resolveIntakeError(w http.ResponseWriter, r *http.Request) {
	intakeErrorRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		var req resolveIntakeErrorRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		note := strings.TrimSpace(req.Note)
		if note == "" || len([]rune(note)) > maxResolutionNote {
			validationFailed(w, r, map[string]string{"note": fmt.Sprintf("must be 1 to %d characters", maxResolutionNote)})
			return
		}
		p, _ := tenant.FromContext(r.Context())
		e, err := a.store.ResolveIntakeError(r.Context(), id, version, note, p.UserID, a.now())
		if err != nil {
			a.fail(w, r, "resolve intake error", err)
			return
		}
		setETag(w, e.Version)
		httpx.JSON(w, http.StatusOK, e)
	})(w, r)
}
