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
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/store"
)

const (
	DutyLegalHoldPlace   = "compliance.legal_hold.place"
	DutyLegalHoldRelease = "compliance.legal_hold.release"
	DutyLegalHoldRead    = "compliance.legal_hold.read"
	DutyLegalHoldCheck   = "compliance.legal_hold.check"
	DutyErasureRequest   = "compliance.erasure.request"
	DutyRetentionRead    = "compliance.retention.read"

	maxDataClasses = 5
)

type ReleaseHoldRequest struct {
	Reason string `json:"reason"`
}

func (a *API) registerHolds(r chi.Router) {
	r.With(authz.RequireDuty(DutyLegalHoldPlace)).Post("/v1/privacy/holds", a.placeHold)
	r.With(authz.RequireDuty(DutyLegalHoldRead)).Get("/v1/privacy/holds", a.listHolds)
	r.With(authz.RequireDuty(DutyLegalHoldCheck)).Get("/v1/privacy/holds:check", a.checkHolds)
	r.With(authz.RequireDuty(DutyLegalHoldRead)).Get("/v1/privacy/holds/{id}", a.getHold)
	r.With(authz.RequireDuty(DutyLegalHoldRelease)).Post("/v1/privacy/holds/{id}/release", a.releaseHold)
	r.With(authz.RequireDuty(DutyErasureRequest)).Post("/v1/privacy/erasures", a.requestErasure)
	r.With(authz.RequireDuty(DutyRetentionRead)).Get("/v1/privacy/retention-policies", a.retentionPolicies)
}

func idempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 200 {
		httpx.Fail(w, r, invalid(map[string]string{"Idempotency-Key": "is required (1..200 characters)"}))
		return "", false
	}
	return key, true
}

func holdScopeFields(fields map[string]string, s domain.HoldScope) {
	if !ids.Valid(s.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if s.SubjectRef != "" && !validID(s.SubjectRef) {
		fields["subjectRef"] = fmt.Sprintf("is 1..%d characters", maxIDLength)
	}
	if s.RecordRef != "" && !validID(s.RecordRef) {
		fields["recordRef"] = fmt.Sprintf("is 1..%d characters", maxIDLength)
	}
	if err := s.Validate(); err != nil {
		fields["dataClass"] = "must be AP_PAYEE, FINANCIAL_RECORD, EMPLOYMENT, CANDIDATE or SYSTEM_LOG"
	}
}

func legalEntityInScope(w http.ResponseWriter, r *http.Request, legalEntityID string) bool {
	p, _ := tenant.FromContext(r.Context())
	if tenant.InLegalEntityScope(p, legalEntityID) {
		return true
	}
	httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity is outside the caller's scope"))
	return false
}

func (a *API) placeHold(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var in store.HoldInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	holdScopeFields(fields, in.HoldScope)
	if err := in.ValidHold(); err != nil {
		fields["scope"] = "give a subjectRef, a dataClass or a recordRef"
	}
	if err := domain.ValidHoldReason(in.Reason); err != nil || len(in.Reason) > maxText {
		fields["reason"] = fmt.Sprintf("is required (10..%d characters)", maxText)
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	if !legalEntityInScope(w, r, in.LegalEntityID) {
		return
	}
	out, err := a.store.PlaceHold(r.Context(), in, key, a.now())
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) releaseHold(w http.ResponseWriter, r *http.Request) {
	id, ok := conflictID(w, r)
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var in ReleaseHoldRequest
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := domain.ValidHoldReason(in.Reason); err != nil || len(in.Reason) > maxText {
		httpx.Fail(w, r, invalid(map[string]string{"reason": fmt.Sprintf("is required (10..%d characters)", maxText)}))
		return
	}
	out, err := a.store.ReleaseHold(r.Context(), id, version, in.Reason, a.now())
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getHold(w http.ResponseWriter, r *http.Request) {
	id, ok := conflictID(w, r)
	if !ok {
		return
	}
	out, err := a.store.GetHold(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listHolds(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.HoldFilter{State: q.Get("state"), LegalEntityID: strings.ToLower(q.Get("legalEntityId")), SubjectRef: q.Get("subjectRef"), DataClass: q.Get("dataClass"), Cursor: q.Get("cursor")}
	fields := map[string]string{}
	if f.State != "" && f.State != "ACTIVE" && f.State != "RELEASED" {
		fields["state"] = "must be ACTIVE or RELEASED"
	}
	if f.LegalEntityID != "" && !ids.Valid(f.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if f.DataClass != "" && !domain.ValidDataClass(domain.DataClass(f.DataClass)) {
		fields["dataClass"] = "is not a data class"
	}
	if f.SubjectRef != "" && !validID(f.SubjectRef) {
		fields["subjectRef"] = fmt.Sprintf("is 1..%d characters", maxIDLength)
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxPageSize {
			fields["limit"] = fmt.Sprintf("must be 1..%d", store.MaxPageSize)
		}
		f.Limit = n
	}
	if f.Cursor != "" {
		if _, _, err := store.DecodeCursor(f.Cursor); err != nil {
			fields["cursor"] = "is malformed"
		}
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	page, err := a.store.ListHolds(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *API) checkHolds(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	target := domain.HoldScope{LegalEntityID: strings.ToLower(q.Get("legalEntityId")), SubjectRef: q.Get("subjectRef"), DataClass: q.Get("dataClass"), RecordRef: q.Get("recordRef")}
	fields := map[string]string{}
	holdScopeFields(fields, target)
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	if !legalEntityInScope(w, r, target.LegalEntityID) {
		return
	}
	out, err := a.store.CheckHolds(r.Context(), target)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) requestErasure(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var in store.ErasureInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in.LegalEntityID = strings.ToLower(in.LegalEntityID)
	in.RequestID = strings.ToLower(in.RequestID)
	fields := map[string]string{}
	if !ids.Valid(in.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !validID(in.SubjectRef) {
		fields["subjectRef"] = fmt.Sprintf("is required (1..%d characters)", maxIDLength)
	}
	if in.RequestID != "" && !ids.Valid(in.RequestID) {
		fields["requestId"] = "must be a UUID"
	}
	if len(in.DataClasses) > maxDataClasses {
		fields["dataClasses"] = fmt.Sprintf("at most %d classes", maxDataClasses)
	}
	for _, c := range in.DataClasses {
		if !domain.ValidDataClass(domain.DataClass(c)) {
			fields["dataClasses"] = "must be AP_PAYEE, FINANCIAL_RECORD, EMPLOYMENT, CANDIDATE or SYSTEM_LOG"
		}
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	if !legalEntityInScope(w, r, in.LegalEntityID) {
		return
	}
	out, err := a.store.RequestErasure(r.Context(), in, key, a.now())
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	httpx.JSON(w, http.StatusAccepted, out)
}

func (a *API) retentionPolicies(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": domain.DefaultRetention()})
}
