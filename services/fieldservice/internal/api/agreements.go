package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

// maxReasonLen bounds the free text of a status reason or visit note.
const maxReasonLen = 500

func (a *API) registerAgreements(r chi.Router) {
	r.With(authz.RequireDuty(dutyAgreement)).Post("/v1/service/agreements/{id}:transition", a.transitionAgreement)
	r.With(authz.RequireDuty(dutyAgreement)).Post("/v1/service/agreements:expire", a.expireAgreements)
	r.With(authz.RequireDuty(dutyAgreement)).Post("/v1/service/agreements/{id}/visits", a.recordVisit)
	r.With(authz.RequireDuty(dutyInquire)).Get("/v1/service/agreements/{id}/visits", a.listVisits)
	r.With(authz.RequireDuty(dutyInquire)).Get("/v1/service/agreements/{id}/history", a.agreementHistory)
}

type agreementTransitionRequest struct {
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// transitionAgreement activates, suspends, resumes or cancels an agreement
// (FR-45.3). If-Match carries the version the caller read; an Idempotency-Key
// makes a retry return the first answer. Suspending and canceling need a reason.
func (a *API) transitionAgreement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeAgreementNotFound, "service agreement not found")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req agreementTransitionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	fields := map[string]string{}
	if !oneOf(req.To, domain.AgreementActive, domain.AgreementSuspended, domain.AgreementCanceled) {
		fields["to"] = "must be ACTIVE, SUSPENDED or CANCELED"
	}
	if len(req.Reason) > maxReasonLen {
		fields["reason"] = fmt.Sprintf("must be at most %d characters", maxReasonLen)
	} else if req.Reason == "" && oneOf(req.To, domain.AgreementSuspended, domain.AgreementCanceled) {
		fields["reason"] = "is required to suspend or cancel"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key := r.Header.Get(idempotencyKey)
	if len(key) > 255 {
		validationFailed(w, r, map[string]string{idempotencyKey: "must be at most 255 characters"})
		return
	}
	hash := hashOf(struct {
		Op      string `json:"op"`
		ID      string `json:"id"`
		Version int    `json:"version"`
		To      string `json:"to"`
		Reason  string `json:"reason"`
	}{"agreement.transition", id, version, req.To, req.Reason})
	agr, replayed, err := a.store.TransitionAgreement(r.Context(), id, version, req.To, req.Reason, a.now(), key, hash)
	if err != nil {
		a.fail(w, r, "transition agreement", err)
		return
	}
	setETag(w, agr.Version)
	a.respond(w, replayed, http.StatusOK, agr)
}

// expireAgreements runs one expiry pass for the caller's tenant and legal-entity
// scope: ACTIVE and SUSPENDED agreements whose validTo is behind today (UTC)
// become EXPIRED. Repeating it changes nothing. Entitlement, visits and posting
// already check validity or status, so a late pass delays only the status and
// the event; no schedule runs it yet (NOT-IMPLEMENTED.md).
func (a *API) expireAgreements(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit := defaultScanLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxScanLimit {
			validationFailed(w, r, map[string]string{"limit": fmt.Sprintf("must be 1 to %d", maxScanLimit)})
			return
		}
		limit = n
	}
	res, err := a.store.ExpireAgreements(r.Context(), a.now(), limit)
	if err != nil {
		a.fail(w, r, "expire agreements", err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

type visitRequest struct {
	LineIndex   int     `json:"lineIndex"`
	Kind        string  `json:"kind"`
	VisitDate   string  `json:"visitDate"`
	WorkOrderID *string `json:"workOrderId"`
	Note        string  `json:"note"`
}

// recordVisit consumes one included visit of an agreement line (FR-45.3).
func (a *API) recordVisit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeAgreementNotFound, "service agreement not found")
	if !ok {
		return
	}
	var req visitRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Note = strings.TrimSpace(req.Note)
	fields := map[string]string{}
	if req.LineIndex < 0 {
		fields["lineIndex"] = "must be zero or more"
	}
	if !oneOf(req.Kind, domain.VisitPreventive, domain.VisitCorrective) {
		fields["kind"] = "must be PREVENTIVE or CORRECTIVE"
	}
	if _, ok := parseDate(req.VisitDate); !ok {
		fields["visitDate"] = "must be a date as YYYY-MM-DD"
	}
	optionalUUID(fields, "workOrderId", req.WorkOrderID)
	if len(req.Note) > maxReasonLen {
		fields["note"] = fmt.Sprintf("must be at most %d characters", maxReasonLen)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, ok := idempotencyKeyOf(w, r)
	if !ok {
		return
	}
	v, replayed, err := a.store.RecordVisit(r.Context(), store.VisitInput{AgreementID: id, LineIndex: req.LineIndex, Kind: req.Kind,
		VisitDate: req.VisitDate, WorkOrderID: req.WorkOrderID, Note: req.Note}, key, hashOf(struct {
		ID string `json:"id"`
		visitRequest
	}{id, req}))
	if err != nil {
		a.fail(w, r, "record visit", err)
		return
	}
	a.respond(w, replayed, http.StatusCreated, v)
}

func (a *API) listVisits(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeAgreementNotFound, "service agreement not found"))
		return
	}
	items, err := a.store.ListVisits(r.Context(), id)
	if err != nil {
		a.fail(w, r, "list visits", err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []store.AgreementVisit `json:"items"`
	}{items})
}

func (a *API) agreementHistory(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeAgreementNotFound, "service agreement not found"))
		return
	}
	items, err := a.store.AgreementHistory(r.Context(), id)
	if err != nil {
		a.fail(w, r, "agreement history", err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []store.AgreementChange `json:"items"`
	}{items})
}
