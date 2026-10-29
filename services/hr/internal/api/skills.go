package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const (
	sklMaxSearchTerms = 20
	// sklMaxValidityMonths is the longest certificate validity accepted (20 years).
	sklMaxValidityMonths = 240
	sklMaxName           = 120
	// sklDefaultExpiringDays is the widest reminder lead time (BR-42.8: 60/30/7).
	sklDefaultExpiringDays = 60
	sklMaxExpiringDays     = 366
)

const codeShape = "must be 1-20 characters: A-Z, 0-9, hyphen and underscore, starting with a letter or digit"

// skillRequest adds a catalog skill. A missing scaleMax takes the default (5).
type skillRequest struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	ScaleMax *int   `json:"scaleMax"`
}

// rateSkillRequest records a worker's rating on a skill (FR-42.10).
type rateSkillRequest struct {
	SkillCode  string `json:"skillCode"`
	Rating     *int   `json:"rating"`
	AssessedOn string `json:"assessedOn"`
}

// certificationRequest issues a certificate. Give expiresOn or
// validityMonths, or neither for a certificate that does not expire.
type certificationRequest struct {
	Code           string  `json:"code"`
	Name           string  `json:"name"`
	Issuer         *string `json:"issuer"`
	IssuedOn       string  `json:"issuedOn"`
	ExpiresOn      *string `json:"expiresOn"`
	ValidityMonths *int    `json:"validityMonths"`
}

// requirementRequest sets the minimum rating a position needs in a skill.
type requirementRequest struct {
	SkillCode string `json:"skillCode"`
	MinRating *int   `json:"minRating"`
}

// createSkill is POST /v1/hr/skills (HcmSetupAdmin).
func (a *API) createSkill(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req skillRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !jobCode.MatchString(req.Code) {
		fields["code"] = codeShape
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > sklMaxName {
		fields["name"] = fmt.Sprintf("must be 1-%d characters", sklMaxName)
	}
	scale := domain.DefaultSkillScale
	if req.ScaleMax != nil {
		scale = *req.ScaleMax
	}
	if domain.ValidateScale(scale) != nil {
		fields["scaleMax"] = fmt.Sprintf("must be between 1 and %d", domain.MaxSkillScale)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	skill, inserted, err := a.store.CreateSkill(r.Context(), store.Skill{Code: req.Code, Name: name, ScaleMax: scale}, r.Header.Get(idempotencyKey))
	if err != nil {
		logFailure(r, "create skill", err)
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
		a.skillsCreated.Add(1)
	}
	httpx.JSON(w, status, skill)
}

// listSkills is GET /v1/hr/skills, paged by code.
func (a *API) listSkills(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, ok := pageParams(r)
	if !ok {
		badPage(w, r)
		return
	}
	page, err := a.store.ListSkills(r.Context(), after, limit)
	if err != nil {
		logFailure(r, "list skills", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.Skill]{Items: page.Items, NextCursor: cursor(page.NextAfter)})
}

// rateWorkerSkill is POST /v1/hr/workers/{id}/skills (HcmWorkerMaintain). It
// answers 200 with the stored rating whether or not it changed.
func (a *API) rateWorkerSkill(w http.ResponseWriter, r *http.Request, workerID string) {
	var req rateSkillRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !jobCode.MatchString(req.SkillCode) {
		fields["skillCode"] = codeShape
	}
	if req.Rating == nil {
		fields["rating"] = "is required"
	}
	assessed, ok := parseDate(req.AssessedOn)
	switch {
	case !ok:
		fields["assessedOn"] = "must be YYYY-MM-DD"
	case assessed.After(today()):
		fields["assessedOn"] = "must not be in the future"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	rating, changed, err := a.store.RateWorkerSkill(r.Context(), workerID, req.SkillCode, *req.Rating, assessed)
	if err != nil {
		logFailure(r, "rate worker skill", err)
		httpx.Fail(w, r, err)
		return
	}
	if changed {
		a.skillRatings.Add(1)
	}
	httpx.JSON(w, http.StatusOK, rating)
}

// listWorkerSkills is GET /v1/hr/workers/{id}/skills.
func (a *API) listWorkerSkills(w http.ResponseWriter, r *http.Request, workerID string) {
	items, err := a.store.ListWorkerSkills(r.Context(), workerID)
	if err != nil {
		logFailure(r, "list worker skills", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.WorkerSkill]{Items: items})
}

// issueCertification is POST /v1/hr/workers/{id}/certifications
// (HcmWorkerMaintain, BR-42.8). validityMonths is turned into expiresOn here.
func (a *API) issueCertification(w http.ResponseWriter, r *http.Request, workerID string) {
	var req certificationRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !jobCode.MatchString(req.Code) {
		fields["code"] = codeShape
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > sklMaxName {
		fields["name"] = fmt.Sprintf("must be 1-%d characters", sklMaxName)
	}
	var issuer *string
	if req.Issuer != nil {
		if v := strings.TrimSpace(*req.Issuer); v != "" {
			if len([]rune(v)) > sklMaxName {
				fields["issuer"] = fmt.Sprintf("must be at most %d characters", sklMaxName)
			}
			issuer = &v
		}
	}
	issued, issuedOK := parseDate(req.IssuedOn)
	switch {
	case !issuedOK:
		fields["issuedOn"] = "must be YYYY-MM-DD"
	case issued.After(today()):
		fields["issuedOn"] = "must not be in the future"
	}
	expiresGiven := req.ExpiresOn != nil && *req.ExpiresOn != ""
	var expires *string
	if req.ValidityMonths != nil {
		months := *req.ValidityMonths
		switch {
		case expiresGiven:
			fields["validityMonths"] = "give expiresOn or validityMonths, not both"
		case months < 1 || months > sklMaxValidityMonths:
			fields["validityMonths"] = fmt.Sprintf("must be between 1 and %d", sklMaxValidityMonths)
		case issuedOK:
			e := domain.CertificationExpiry(issued, months).Format(dateLayout)
			expires = &e
		}
	} else if expiresGiven {
		exp, ok := parseDate(*req.ExpiresOn)
		switch {
		case !ok:
			fields["expiresOn"] = "must be YYYY-MM-DD"
		case issuedOK && !exp.After(issued):
			fields["expiresOn"] = "must be after issuedOn"
		default:
			e := exp.Format(dateLayout)
			expires = &e
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	cert, inserted, err := a.store.IssueCertification(r.Context(), store.Certification{
		WorkerID: workerID, Code: req.Code, Name: name, Issuer: issuer, IssuedOn: issued.Format(dateLayout), ExpiresOn: expires,
	}, today(), r.Header.Get(idempotencyKey))
	if err != nil {
		logFailure(r, "issue certification", err)
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
		a.certsIssued.Add(1)
	}
	httpx.JSON(w, status, cert)
}

// certificationList is a worker's certificates with the as-of date used for
// their validity.
type certificationList struct {
	AsOf  string                `json:"asOf"`
	Items []store.Certification `json:"items"`
}

// listCertifications is GET /v1/hr/workers/{id}/certifications?asOf=.
func (a *API) listCertifications(w http.ResponseWriter, r *http.Request, workerID string) {
	asOf, ok := asOfParam(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListCertifications(r.Context(), workerID, asOf)
	if err != nil {
		logFailure(r, "list certifications", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, certificationList{AsOf: asOf.Format(dateLayout), Items: items})
}

// expiringList is one page of the certifications-expiring inquiry.
type expiringList struct {
	AsOf       string                        `json:"asOf"`
	WithinDays int                           `json:"withinDays"`
	Items      []store.ExpiringCertification `json:"items"`
	NextCursor string                        `json:"nextCursor,omitempty"`
}

// expiringCertifications is GET /v1/hr/certifications/expiring?withinDays=&asOf=
// (8.42.14 "Certifications expiring", BR-42.8).
func (a *API) expiringCertifications(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	asOf, ok := asOfParam(w, r)
	if !ok {
		return
	}
	within := sklDefaultExpiringDays
	if raw := r.URL.Query().Get("withinDays"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > sklMaxExpiringDays {
			validationFailed(w, r, map[string]string{"withinDays": fmt.Sprintf("must be 1 to %d", sklMaxExpiringDays)})
			return
		}
		within = n
	}
	limit, after, ok := pageParams(r)
	if !ok {
		badPage(w, r)
		return
	}
	page, err := a.store.ListExpiringCertifications(r.Context(), asOf, within, after, limit)
	if err != nil {
		logFailure(r, "list expiring certifications", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, expiringList{AsOf: asOf.Format(dateLayout), WithinDays: within, Items: page.Items, NextCursor: cursor(page.NextAfter)})
}

// skillGap is GET /v1/hr/workers/{id}/skill-gap?positionId= (FR-42.18).
func (a *API) skillGap(w http.ResponseWriter, r *http.Request, workerID string) {
	positionID := r.URL.Query().Get("positionId")
	if !ids.Valid(positionID) {
		validationFailed(w, r, map[string]string{"positionId": "must be a UUID"})
		return
	}
	gap, err := a.store.SkillGapFor(r.Context(), workerID, positionID)
	if err != nil {
		logFailure(r, "skill gap", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, gap)
}

// skillSearchResponse carries the matches and the as-of date of the
// certificate validity and employment checks.
type skillSearchResponse struct {
	AsOf string `json:"asOf"`
	store.SkillSearch
}

// searchSkills is GET /v1/hr/skills/search?skills=WELDING:3,PLC&minRating=2&certs=OSHA10&asOf=
// (FR-42.18). Each skill term is CODE or CODE:min; a bare CODE takes
// minRating, which defaults to 1. availableFrom is refused until availability
// exists (NOT-IMPLEMENTED.md).
func (a *API) searchSkills(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	defaultMin := 1
	if raw := q.Get("minRating"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > domain.MaxSkillScale {
			fields["minRating"] = fmt.Sprintf("must be between 1 and %d", domain.MaxSkillScale)
		} else {
			defaultMin = n
		}
	}
	reqs, msg := parseSkillTerms(q.Get("skills"), defaultMin)
	if msg != "" {
		fields["skills"] = msg
	}
	certs, msg := parseCodes(q.Get("certs"))
	if msg != "" {
		fields["certs"] = msg
	}
	if len(fields) == 0 && len(reqs)+len(certs) == 0 {
		fields["skills"] = "give at least one skill or certificate"
	}
	if q.Get("availableFrom") != "" {
		fields["availableFrom"] = "the availability filter is not built yet"
	}
	asOf, ok := parseAsOf(q.Get("asOf"))
	if !ok {
		fields["asOf"] = "must be YYYY-MM-DD"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	found, err := a.store.SearchWorkers(r.Context(), reqs, certs, asOf)
	if err != nil {
		logFailure(r, "skill search", err)
		httpx.Fail(w, r, err)
		return
	}
	a.skillSearches.Add(1)
	httpx.JSON(w, http.StatusOK, skillSearchResponse{AsOf: asOf.Format(dateLayout), SkillSearch: found})
}

// setRequirement is POST /v1/hr/positions/{id}/skill-requirements
// (HcmOrgMaintain). It sets or replaces one skill's minimum, so a retry is safe.
func (a *API) setRequirement(w http.ResponseWriter, r *http.Request, positionID string) {
	var req requirementRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !jobCode.MatchString(req.SkillCode) {
		fields["skillCode"] = codeShape
	}
	if req.MinRating == nil {
		fields["minRating"] = "is required"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.SetRequirement(r.Context(), positionID, req.SkillCode, *req.MinRating)
	if err != nil {
		logFailure(r, "set skill requirement", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// removeRequirement is DELETE /v1/hr/positions/{id}/skill-requirements/{skillCode}.
func (a *API) removeRequirement(w http.ResponseWriter, r *http.Request, positionID string) {
	code := chi.URLParam(r, "skillCode")
	if !jobCode.MatchString(code) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRequirementNotFound, "the position does not require that skill"))
		return
	}
	if err := a.store.RemoveRequirement(r.Context(), positionID, code); err != nil {
		logFailure(r, "remove skill requirement", err)
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listRequirements is GET /v1/hr/positions/{id}/skill-requirements.
func (a *API) listRequirements(w http.ResponseWriter, r *http.Request, positionID string) {
	items, err := a.store.ListRequirements(r.Context(), positionID)
	if err != nil {
		logFailure(r, "list skill requirements", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.PositionRequirement]{Items: items})
}

// parseSkillTerms reads comma-separated "CODE" or "CODE:min" terms. A bare
// code takes defaultMin. Codes are upper-cased for convenience. It returns a
// message for the skills field when the list is malformed.
func parseSkillTerms(raw string, defaultMin int) ([]domain.Requirement, string) {
	var out []domain.Requirement
	if strings.TrimSpace(raw) == "" {
		return out, ""
	}
	terms := strings.Split(raw, ",")
	if len(terms) > sklMaxSearchTerms {
		return nil, fmt.Sprintf("at most %d terms", sklMaxSearchTerms)
	}
	for _, term := range terms {
		code, minText, hasMin := strings.Cut(strings.TrimSpace(term), ":")
		code = strings.ToUpper(strings.TrimSpace(code))
		if !jobCode.MatchString(code) {
			return nil, "each term must be CODE or CODE:minRating"
		}
		min := defaultMin
		if hasMin {
			n, err := strconv.Atoi(strings.TrimSpace(minText))
			if err != nil || n < 1 || n > domain.MaxSkillScale {
				return nil, fmt.Sprintf("minRating must be between 1 and %d", domain.MaxSkillScale)
			}
			min = n
		}
		out = append(out, domain.Requirement{Skill: code, MinRating: min})
	}
	return out, ""
}

// parseCodes reads a comma-separated list of certificate codes.
func parseCodes(raw string) ([]string, string) {
	var out []string
	if strings.TrimSpace(raw) == "" {
		return out, ""
	}
	terms := strings.Split(raw, ",")
	if len(terms) > sklMaxSearchTerms {
		return nil, fmt.Sprintf("at most %d codes", sklMaxSearchTerms)
	}
	for _, term := range terms {
		code := strings.ToUpper(strings.TrimSpace(term))
		if !jobCode.MatchString(code) {
			return nil, "each code must be a certificate code"
		}
		out = append(out, code)
	}
	return out, ""
}

// today is the current UTC day.
func today() time.Time { return domain.AsOfDay(time.Now().UTC()) }

// parseAsOf reads an optional YYYY-MM-DD, defaulting to today in UTC.
func parseAsOf(raw string) (time.Time, bool) {
	if raw == "" {
		return today(), true
	}
	return parseDate(raw)
}

// asOfParam reads ?asOf= and answers the 400 itself when it is malformed.
func asOfParam(w http.ResponseWriter, r *http.Request) (time.Time, bool) {
	t, ok := parseAsOf(r.URL.Query().Get("asOf"))
	if !ok {
		validationFailed(w, r, map[string]string{"asOf": "must be YYYY-MM-DD"})
	}
	return t, ok
}
