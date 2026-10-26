package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

const (
	idempotencyKeyHeader = "Idempotency-Key"
	partySequence        = "PARTY"
	codeNotFound         = "PTY-PARTY-NOT-FOUND"
	codeVersionRequired  = "PTY-VERSION-REQUIRED"
)

type createRequest struct {
	PartyType string `json:"partyType"`
	domain.PartyFields
}

type patchRequest struct {
	PartyType *string `json:"partyType"`
	Status    *string `json:"status"`
	Version   *int    `json:"version"`
	domain.PartyFields
}

type partyResponse struct {
	domain.Party
	RequestedID string `json:"requestedId,omitempty"`
	Redirected  bool   `json:"redirected,omitempty"`
}

func (a *API) createParty(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req createRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.PartyFields = req.PartyFields.Clean()
	if errs := domain.ValidateParty(req.PartyType, req.PartyFields, true); len(errs) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeInvalid, "party failed validation")
		p.Errors = errs
		httpx.Fail(w, r, p)
		return
	}
	key := r.Header.Get(idempotencyKeyHeader)
	numberingKey := key
	if numberingKey == "" {
		numberingKey = ids.New()
	}
	party, created, err := a.parties.CreateParty(r.Context(), store.NewParty{
		PartyType: req.PartyType, Fields: req.PartyFields, Key: key, Hash: domain.RequestHash(req),
	}, func(ctx context.Context) (string, error) { return a.sequence.Next(ctx, partySequence, numberingKey) })
	if err != nil {
		failParty(w, r, err)
		return
	}
	if created {
		a.created.Add(1)
	}
	respond(w, r, http.StatusCreated, partyResponse{Party: party})
}

func (a *API) getParty(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	requestedID := chi.URLParam(r, "id")
	party, redirected, err := a.parties.GetParty(r.Context(), requestedID)
	if err != nil {
		failParty(w, r, err)
		return
	}
	res := partyResponse{Party: party}
	if redirected {
		res.RequestedID, res.Redirected = requestedID, true
	}
	respond(w, r, http.StatusOK, res)
}

func (a *API) patchParty(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req patchRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	version, ok := requestedVersion(r, req.Version)
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, codeVersionRequired, "If-Match or a body version of at least 1 is required"))
		return
	}
	party, err := a.parties.UpdateParty(r.Context(), chi.URLParam(r, "id"), version, store.PartyPatch{PartyType: req.PartyType, Status: req.Status, Fields: req.PartyFields})
	if err != nil {
		failParty(w, r, err)
		return
	}
	respond(w, r, http.StatusOK, partyResponse{Party: party})
}

func requestedVersion(r *http.Request, body *int) (int, bool) {
	if header := r.Header.Get("If-Match"); header != "" {
		v, err := strconv.Atoi(strings.Trim(header, `"`))
		return v, err == nil && v >= 1
	}
	if body != nil && *body >= 1 {
		return *body, true
	}
	return 0, false
}

func respond(w http.ResponseWriter, r *http.Request, status int, res partyResponse) {
	caller, _ := tenant.FromContext(r.Context())
	res.DOB = domain.MaskDOB(res.DOB, caller.HasDuty(dutyPII))
	w.Header().Set("ETag", `"`+strconv.Itoa(res.Version)+`"`)
	httpx.JSON(w, status, res)
}

func failParty(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrInvalidID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeNotFound, "party not found"))
		return
	}
	var problem *httpx.Problem
	if !errors.As(err, &problem) {
		slog.Error("party request failed", "err", err, "path", r.URL.Path, "correlationId", middleware.GetReqID(r.Context()))
	}
	httpx.Fail(w, r, err)
}
