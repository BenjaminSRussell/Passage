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
	"github.com/google/uuid"

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
	// ID lets the migration loader keep a legacy-stable id; only party.import.write may set it.
	ID        string `json:"id,omitempty"`
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
	if req.ID != "" {
		caller, _ := tenant.FromContext(r.Context())
		if !caller.HasDuty(dutyImport) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "a caller-supplied id needs duty "+dutyImport))
			return
		}
		id, err := uuid.Parse(req.ID)
		if err != nil || id == uuid.Nil || id.String() != req.ID {
			p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeInvalid, "party failed validation")
			p.Errors = map[string]string{"id": "must be a lowercase UUID"}
			httpx.Fail(w, r, p)
			return
		}
	}
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
		ID: req.ID, PartyType: req.PartyType, Fields: req.PartyFields, Key: key, Hash: domain.RequestHash(req),
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
	respondRead(w, r, res)
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

// writeReceipt is what a write-only importer gets back: enough to chain its
// next call (id, version), none of the party's names or personal data.
type writeReceipt struct {
	ID          string `json:"id"`
	PartyNumber string `json:"partyNumber"`
	PartyType   string `json:"partyType"`
	Status      string `json:"status"`
	Version     int    `json:"version"`
}

func respond(w http.ResponseWriter, r *http.Request, status int, res partyResponse) {
	caller, _ := tenant.FromContext(r.Context())
	w.Header().Set("ETag", `"`+strconv.Itoa(res.Version)+`"`)
	if !caller.HasDuty(dutyRead) && !caller.HasDuty(dutyWrite) {
		// party.import.write opens no read route, so a create or PATCH (even an
		// empty one) must not hand the party record back to the loader.
		httpx.JSON(w, status, writeReceipt{ID: res.ID, PartyNumber: res.PartyNumber, PartyType: res.PartyType, Status: res.Status, Version: res.Version})
		return
	}
	res.DOB = domain.MaskDOB(res.DOB, caller.HasDuty(dutyPII))
	httpx.JSON(w, status, res)
}

// respondRead answers a GET: a caller admitted by party.read or party.import.read gets the record, with the date of
// birth masked unless it holds pii.read.
func respondRead(w http.ResponseWriter, r *http.Request, res partyResponse) {
	caller, _ := tenant.FromContext(r.Context())
	w.Header().Set("ETag", `"`+strconv.Itoa(res.Version)+`"`)
	res.DOB = domain.MaskDOB(res.DOB, caller.HasDuty(dutyPII))
	httpx.JSON(w, http.StatusOK, res)
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
