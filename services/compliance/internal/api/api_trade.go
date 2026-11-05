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

// Trade compliance duties (PRD §9.48.5 and §9.48.15).
const (
	DutyTradeView    = "compliance.trade-screening.view"
	DutyTradeOfficer = "compliance.trade-screening.decide"
)

const (
	// maxListEntries bounds the list entries of one screening request until the
	// screening_list store serves them.
	maxListEntries = 10000
	maxNameLen     = 500
)

// ScreenRequest is the body of POST /v1/trade/screening/check. The caller
// supplies the list entries to screen against until the screening_list store
// and its daily refresh exist; the threshold is the tenant's
// screening.threshold, never the caller's.
type ScreenRequest struct {
	Subject domain.Subject     `json:"subject"`
	Entries []domain.ListEntry `json:"entries"`
}

// ExportCheckRequest is the body of POST /v1/trade/export-checks. The caller
// supplies the delivery's lines and the licenses on file until the
// export_license store exists; the decision table is the pack's, the license
// check mode is the legal entity's trade.license_check and the clock is the
// server's.
type ExportCheckRequest struct {
	Destination              string              `json:"destination"`
	Region                   string              `json:"region,omitempty"`
	Items                    []domain.ExportItem `json:"items"`
	Licenses                 []domain.License    `json:"licenses"`
	OfficerReleaseLicenseRef string              `json:"officerReleaseLicenseRef,omitempty"`
}

func (a *API) registerTrade(r chi.Router) {
	r.With(authz.RequireDuty(DutyTradeView)).Post("/v1/trade/screening/check", a.screen)
	r.With(authz.RequireDuty(DutyTradeView)).Get("/v1/trade/screening/hits", a.listHits)
	r.With(authz.RequireDuty(DutyTradeView)).Get("/v1/trade/screening/hits/{id}", a.getHit)
	r.With(authz.RequireDuty(DutyTradeOfficer)).Post("/v1/trade/screening/hits/{id}/decision", a.decideHit)
	r.With(authz.RequireDuty(DutyTradeView)).Post("/v1/trade/export-checks", a.exportCheck)
}

// screen screens a party or document on demand (FR-48.19): new hits are
// stored OPEN and the response says whether the subject is blocked.
func (a *API) screen(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req ScreenRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	s := req.Subject
	s.ID = strings.ToLower(strings.TrimSpace(s.ID))
	s.Country = strings.ToUpper(strings.TrimSpace(s.Country))
	fields := map[string]string{}
	if !domain.ValidSubjectType(s.Type) {
		fields["subject.type"] = "must be PARTY or DOCUMENT"
	}
	if !ids.Valid(s.ID) {
		fields["subject.id"] = "must be a UUID"
	}
	if strings.TrimSpace(s.Name) == "" || len(s.Name) > maxNameLen {
		fields["subject.name"] = fmt.Sprintf("is required (at most %d characters)", maxNameLen)
	}
	if len(s.Address) > maxText {
		fields["subject.address"] = fmt.Sprintf("at most %d characters", maxText)
	}
	if s.Country != "" && len(s.Country) != 2 {
		fields["subject.country"] = "must be an ISO 3166 alpha-2 code"
	}
	if len(req.Entries) > maxListEntries {
		fields["entries"] = fmt.Sprintf("at most %d entries", maxListEntries)
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	res, err := a.store.Screen(r.Context(), s, req.Entries, a.cfg.ScreeningThreshold, a.now())
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (a *API) listHits(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.HitFilter{State: q.Get("state"), ListCode: q.Get("listCode"), SubjectType: q.Get("subjectType"),
		SubjectID: strings.ToLower(q.Get("subjectId")), Cursor: q.Get("cursor")}
	fields := map[string]string{}
	if f.State != "" && !domain.ValidHitState(domain.HitState(f.State)) {
		fields["state"] = "must be OPEN, FALSE_POSITIVE or CONFIRMED"
	}
	if f.SubjectType != "" && !domain.ValidSubjectType(f.SubjectType) {
		fields["subjectType"] = "must be PARTY or DOCUMENT"
	}
	if f.SubjectID != "" && !ids.Valid(f.SubjectID) {
		fields["subjectId"] = "must be a UUID"
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
	page, err := a.store.ListHits(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *API) getHit(w http.ResponseWriter, r *http.Request) {
	id, ok := conflictID(w, r)
	if !ok {
		return
	}
	h, err := a.store.GetHit(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, h.Version)
	httpx.JSON(w, http.StatusOK, h)
}

// decideHit clears an OPEN hit as a false positive or confirms it; the caller
// is the decider. If-Match and Idempotency-Key are required.
func (a *API) decideHit(w http.ResponseWriter, r *http.Request) {
	id, ok := conflictID(w, r)
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 200 {
		httpx.Fail(w, r, invalid(map[string]string{"Idempotency-Key": "is required (1..200 characters)"}))
		return
	}
	var in store.DecisionInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if in.Decision != domain.DecisionClearFalsePositive && in.Decision != domain.DecisionConfirm {
		fields["decision"] = "must be CLEAR_FALSE_POSITIVE or CONFIRM"
	}
	if strings.TrimSpace(in.Reason) == "" || len(in.Reason) > maxText {
		fields["reason"] = "is required"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	h, err := a.store.DecideHit(r.Context(), id, version, in, key, a.now())
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	setETag(w, h.Version)
	httpx.JSON(w, http.StatusOK, h)
}

// exportCheck determines the license requirement of a delivery (FR-48.20,
// BR-48.16). Releasing an embargoed delivery with a license reference needs
// compliance.trade-screening.decide; nothing is stored yet.
func (a *API) exportCheck(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req ExportCheckRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	ref := strings.TrimSpace(req.OfficerReleaseLicenseRef)
	if p, _ := tenant.FromContext(r.Context()); ref != "" && !p.HasDuty(DutyTradeOfficer) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "missing duty "+DutyTradeOfficer+": only a trade compliance officer can release an embargoed delivery"))
		return
	}
	if len(ref) > maxText {
		httpx.Fail(w, r, invalid(map[string]string{"officerReleaseLicenseRef": fmt.Sprintf("at most %d characters", maxText)}))
		return
	}
	res, err := domain.EvaluateExport(domain.ExportCheckInput{Destination: req.Destination, Region: req.Region, Items: req.Items,
		Licenses: req.Licenses, Mode: a.cfg.LicenseCheck, Now: a.now(), OfficerReleaseLicenseRef: ref})
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
