package api

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const (
	dutyLearningAdmin = "HcmLearningAdmin"
	dutyEssLearning   = "EssLearning"

	crsMaxSkills      = 20
	crsMaxGroup       = 60
	crsMaxLocation    = 200
	crsMaxVirtualURL  = 500
	crsMaxSeats       = 10000
	crsMaxResultLines = 500
)

var (
	// crsMaxCost keeps a cost inside numeric(20,6).
	crsMaxCost = decimal.New(1, 14)
	// crsMaxScore keeps a score inside numeric(5,2).
	crsMaxScore = decimal.NewFromInt(1000)
)

// courseOutcomeRequest is one skill a course trains, at the rating a PASS awards.
type courseOutcomeRequest struct {
	SkillCode string `json:"skillCode"`
	Rating    *int   `json:"rating"`
}

// courseRequest creates a catalog course (FR-42.14). A certificate needs its
// code and name; validityMonths needs a certificate (0 or absent: the
// certificate does not expire).
type courseRequest struct {
	Code                    string                 `json:"code"`
	Name                    string                 `json:"name"`
	CourseGroup             *string                `json:"courseGroup"`
	SkillOutcomes           []courseOutcomeRequest `json:"skillOutcomes"`
	CertificationCode       *string                `json:"certificationCode"`
	CertificationName       *string                `json:"certificationName"`
	ValidityMonths          *int                   `json:"validityMonths"`
	RequiresManagerApproval bool                   `json:"requiresManagerApproval"`
}

// sessionRequest schedules a session. It needs a location, a virtual link,
// or both; at most one instructor; a cost needs its currency.
type sessionRequest struct {
	StartsAt           string  `json:"startsAt"`
	EndsAt             string  `json:"endsAt"`
	Location           *string `json:"location"`
	VirtualURL         *string `json:"virtualUrl"`
	InstructorWorkerID *string `json:"instructorWorkerId"`
	InstructorPartyID  *string `json:"instructorPartyId"`
	Capacity           *int    `json:"capacity"`
	WaitlistCapacity   *int    `json:"waitlistCapacity"`
	CostPerAttendee    *string `json:"costPerAttendee"`
	Currency           *string `json:"currency"`
}

type registerRequest struct {
	WorkerID string `json:"workerId"`
}

// completionRequest records attendance for a session.
type completionRequest struct {
	Results []completionLine `json:"results"`
}

type completionLine struct {
	WorkerID string  `json:"workerId"`
	Outcome  string  `json:"outcome"`
	Result   string  `json:"result"`
	Score    *string `json:"score"`
}

func courseRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return idRoute(store.CodeCourseNotFound, "course not found", next)
}

func sessionRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return idRoute(store.CodeSessionNotFound, "session not found", next)
}

func registrationRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return idRoute(store.CodeRegistrationNotFound, "registration not found", next)
}

// idRoute checks the tenant and the id shape; a malformed id answers the
// route's not-found code, the same answer an unknown id gets.
func idRoute(code, detail string, next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, code, detail))
			return
		}
		next(w, r, id)
	}
}

// crsText trims an optional text; blank is absent. It reports false when the
// value is longer than max runes.
func crsText(s *string, max int) (*string, bool) {
	if s == nil {
		return nil, true
	}
	v := strings.TrimSpace(*s)
	if v == "" {
		return nil, true
	}
	return &v, len([]rune(v)) <= max
}

// validateCourse checks a course request and returns the course to store.
func validateCourse(req courseRequest) (store.Course, map[string]string) {
	fields := map[string]string{}
	if !jobCode.MatchString(req.Code) {
		fields["code"] = codeShape
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > sklMaxName {
		fields["name"] = fmt.Sprintf("must be 1-%d characters", sklMaxName)
	}
	group, ok := crsText(req.CourseGroup, crsMaxGroup)
	if !ok {
		fields["courseGroup"] = fmt.Sprintf("must be at most %d characters", crsMaxGroup)
	}
	if len(req.SkillOutcomes) > crsMaxSkills {
		fields["skillOutcomes"] = fmt.Sprintf("at most %d skills", crsMaxSkills)
	}
	seen := map[string]bool{}
	outcomes := make([]store.CourseSkillOutcome, 0, len(req.SkillOutcomes))
	for i, o := range req.SkillOutcomes {
		key := fmt.Sprintf("skillOutcomes[%d]", i)
		switch {
		case !jobCode.MatchString(o.SkillCode):
			fields[key+".skillCode"] = codeShape
		case seen[o.SkillCode]:
			fields[key+".skillCode"] = "each skill may appear once"
		}
		seen[o.SkillCode] = true
		if o.Rating == nil || *o.Rating < 1 || *o.Rating > domain.MaxSkillScale {
			fields[key+".rating"] = fmt.Sprintf("must be between 1 and %d (and on the skill's scale)", domain.MaxSkillScale)
			continue
		}
		outcomes = append(outcomes, store.CourseSkillOutcome{SkillCode: o.SkillCode, Rating: *o.Rating})
	}
	certCode, _ := crsText(req.CertificationCode, 20)
	certName, nameOK := crsText(req.CertificationName, sklMaxName)
	switch {
	case (certCode == nil) != (certName == nil):
		fields["certificationName"] = "give certificationCode and certificationName together"
	case certCode != nil && !jobCode.MatchString(*certCode):
		fields["certificationCode"] = codeShape
	case !nameOK:
		fields["certificationName"] = fmt.Sprintf("must be at most %d characters", sklMaxName)
	}
	validity := 0
	if req.ValidityMonths != nil {
		validity = *req.ValidityMonths
	}
	switch {
	case validity < 0 || validity > sklMaxValidityMonths:
		fields["validityMonths"] = fmt.Sprintf("must be between 0 and %d", sklMaxValidityMonths)
	case validity > 0 && certCode == nil:
		fields["validityMonths"] = "a validity needs a certificate"
	}
	return store.Course{
		Code: req.Code, Name: name, CourseGroup: group, SkillOutcomes: outcomes,
		CertificationCode: certCode, CertificationName: certName, ValidityMonths: validity,
		RequiresManagerApproval: req.RequiresManagerApproval,
	}, fields
}

// validateSession checks the schedule, place, instructor, seating and cost.
func validateSession(courseID string, req sessionRequest) (store.Session, map[string]string) {
	fields := map[string]string{}
	starts, errStart := time.Parse(time.RFC3339, req.StartsAt)
	if errStart != nil {
		fields["startsAt"] = "must be an RFC 3339 timestamp"
	}
	ends, errEnd := time.Parse(time.RFC3339, req.EndsAt)
	if errEnd != nil {
		fields["endsAt"] = "must be an RFC 3339 timestamp"
	}
	if errStart == nil && errEnd == nil && !ends.After(starts) {
		fields["endsAt"] = "must be after startsAt"
	}
	location, ok := crsText(req.Location, crsMaxLocation)
	if !ok {
		fields["location"] = fmt.Sprintf("must be at most %d characters", crsMaxLocation)
	}
	virtual, _ := crsText(req.VirtualURL, crsMaxVirtualURL)
	if virtual != nil {
		u, err := url.Parse(*virtual)
		if len(*virtual) > crsMaxVirtualURL || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			fields["virtualUrl"] = fmt.Sprintf("must be an http or https URL of at most %d characters", crsMaxVirtualURL)
		}
	}
	if location == nil && virtual == nil && fields["location"] == "" {
		fields["location"] = "give a location, a virtual link, or both"
	}
	instructorWorker, _ := crsText(req.InstructorWorkerID, 36)
	instructorParty, _ := crsText(req.InstructorPartyID, 36)
	switch {
	case instructorWorker != nil && instructorParty != nil:
		fields["instructorPartyId"] = "give an internal or an external instructor, not both"
	case instructorWorker != nil && !ids.Valid(*instructorWorker):
		fields["instructorWorkerId"] = "must be a UUID"
	case instructorParty != nil && !ids.Valid(*instructorParty):
		fields["instructorPartyId"] = "must be a UUID"
	}
	capacity := 0
	if req.Capacity == nil || *req.Capacity < 1 || *req.Capacity > crsMaxSeats {
		fields["capacity"] = fmt.Sprintf("must be between 1 and %d", crsMaxSeats)
	} else {
		capacity = *req.Capacity
	}
	waitlist := domain.DefaultWaitlistCapacity
	if req.WaitlistCapacity != nil {
		waitlist = *req.WaitlistCapacity
	}
	if waitlist < 0 || waitlist > domain.MaxWaitlistCapacity {
		fields["waitlistCapacity"] = fmt.Sprintf("must be between 0 and %d", domain.MaxWaitlistCapacity)
	}
	cost, _ := crsText(req.CostPerAttendee, 40)
	currency, _ := crsText(req.Currency, 3)
	switch {
	case (cost == nil) != (currency == nil):
		fields["currency"] = "give costPerAttendee and currency together"
	case currency != nil && !benCurrency.MatchString(*currency):
		fields["currency"] = "must be an ISO 4217 code such as USD"
	case cost != nil:
		places := money.MinorUnits(*currency)
		d, ok := absDecimal(*cost, places, crsMaxCost, true)
		if !ok {
			fields["costPerAttendee"] = fmt.Sprintf("must be a non-negative decimal with at most %d places", places)
		} else {
			v := d.StringFixed(places)
			cost = &v
		}
	}
	return store.Session{
		CourseID: courseID, StartsAt: starts.UTC(), EndsAt: ends.UTC(), Location: location, VirtualURL: virtual,
		InstructorWorkerID: instructorWorker, InstructorPartyID: instructorParty,
		Capacity: capacity, WaitlistCapacity: waitlist, CostPerAttendee: cost, Currency: currency,
	}, fields
}

// parseCompletion checks each attendance line; field keys name the line.
// A worker may appear once.
func parseCompletion(lines []completionLine) ([]store.Outcome, map[string]string) {
	fields := map[string]string{}
	if len(lines) == 0 || len(lines) > crsMaxResultLines {
		fields["results"] = fmt.Sprintf("must hold between 1 and %d lines", crsMaxResultLines)
		return nil, fields
	}
	seen := map[string]bool{}
	out := make([]store.Outcome, 0, len(lines))
	for i, l := range lines {
		key := fmt.Sprintf("results[%d]", i)
		if !ids.Valid(l.WorkerID) {
			fields[key+".workerId"] = "must be a UUID"
			continue
		}
		if seen[l.WorkerID] {
			fields[key+".workerId"] = "each worker may appear once"
			continue
		}
		seen[l.WorkerID] = true
		score := ""
		if l.Score != nil && strings.TrimSpace(*l.Score) != "" {
			d, ok := absDecimal(strings.TrimSpace(*l.Score), 2, crsMaxScore, true)
			if !ok {
				fields[key+".score"] = "must be a decimal from 0 to 999.99 with at most 2 places"
				continue
			}
			score = d.StringFixed(2)
		}
		if err := domain.ValidateOutcome(l.Outcome, l.Result, score != ""); err != nil {
			fields[key] = err.(*domain.Error).Detail
			continue
		}
		out = append(out, store.Outcome{WorkerID: l.WorkerID, Status: l.Outcome, Result: l.Result, Score: score})
	}
	return out, fields
}

// createCourse is POST /v1/hr/courses (HcmLearningAdmin).
func (a *API) createCourse(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req courseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	course, fields := validateCourse(req)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, inserted, err := a.store.CreateCourse(r.Context(), course, r.Header.Get(idempotencyKey))
	if err != nil {
		logFailure(r, "create course", err)
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
		a.coursesCreated.Add(1)
	}
	httpx.JSON(w, status, out)
}

// listCourses is GET /v1/hr/courses (EssLearning): every employee browses
// the catalog. Paged by code.
func (a *API) listCourses(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, ok := pageParams(r)
	if !ok {
		badPage(w, r)
		return
	}
	page, err := a.store.ListCourses(r.Context(), after, limit)
	if err != nil {
		logFailure(r, "list courses", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.Course]{Items: page.Items, NextCursor: cursor(page.NextAfter)})
}

// getCourse is GET /v1/hr/courses/{id}.
func (a *API) getCourse(w http.ResponseWriter, r *http.Request, id string) {
	course, err := a.store.GetCourse(r.Context(), id)
	if err != nil {
		logFailure(r, "get course", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, course)
}

// createSession is POST /v1/hr/courses/{id}/sessions (HcmLearningAdmin).
func (a *API) createSession(w http.ResponseWriter, r *http.Request, courseID string) {
	var req sessionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	session, fields := validateSession(courseID, req)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, inserted, err := a.store.CreateSession(r.Context(), session, r.Header.Get(idempotencyKey))
	if err != nil {
		logFailure(r, "create course session", err)
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
		a.sessionsCreated.Add(1)
	}
	httpx.JSON(w, status, out)
}

// listSessions is GET /v1/hr/courses/{id}/sessions.
func (a *API) listSessions(w http.ResponseWriter, r *http.Request, courseID string) {
	items, err := a.store.ListSessions(r.Context(), courseID)
	if err != nil {
		logFailure(r, "list course sessions", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.Session]{Items: items})
}

// getSession is GET /v1/hr/courses/sessions/{id} with the seat counts.
func (a *API) getSession(w http.ResponseWriter, r *http.Request, id string) {
	session, err := a.store.GetSession(r.Context(), id)
	if err != nil {
		logFailure(r, "get course session", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, session)
}

// listRegistrations is GET /v1/hr/courses/sessions/{id}/registrations
// (HcmLearningAdmin): the attendance list in registration order.
func (a *API) listRegistrations(w http.ResponseWriter, r *http.Request, id string) {
	items, err := a.store.ListRegistrations(r.Context(), id)
	if err != nil {
		logFailure(r, "list course registrations", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.Registration]{Items: items})
}

// registrationResponse is the registration plus any non-blocking warnings
// (HCM-CRS-002 when the course needs manager approval).
type registrationResponse struct {
	store.Registration
	Warnings []domain.RuleWarning `json:"warnings"`
}

// crsOwnOnly: an employee with only EssLearning acts on their own
// registrations; a learning administrator or a manager (MssApprove) registers
// and cancels for others (8.42.7 "Register worker (self or manager)").
func crsOwnOnly(r *http.Request) bool {
	p := caller(r)
	return !p.HasDuty(dutyLearningAdmin) && !p.HasDuty(dutyMssApprove)
}

// registerForSession is POST /v1/hr/courses/sessions/{id}/registrations
// (EssLearning). It answers 201 with a new registration and 200 with the
// worker's existing one; a full session and waitlist is HCM-CRS-001.
func (a *API) registerForSession(w http.ResponseWriter, r *http.Request, id string) {
	var req registerRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(req.WorkerID) {
		validationFailed(w, r, map[string]string{"workerId": "must be a UUID"})
		return
	}
	reg, warnings, inserted, err := a.store.RegisterWorker(r.Context(), id, req.WorkerID, crsOwnOnly(r))
	if err != nil {
		logFailure(r, "register for course session", err)
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
		a.registrations.Add(1)
	}
	httpx.JSON(w, status, registrationResponse{Registration: reg, Warnings: warnings})
}

// approveRegistration is POST /v1/hr/courses/registrations/{id}/approve (MssApprove).
func (a *API) approveRegistration(w http.ResponseWriter, r *http.Request, id string) {
	if tenantWide := caller(r).HasDuty(dutyLearningAdmin); !tenantWide {
		workerID, err := a.store.RegistrationWorker(r.Context(), id)
		if err != nil {
			a.failStore(w, r, "resolve registration worker", err)
			return
		}
		if !a.confineToTeam(w, r, tenantWide, workerID, store.CodeRegistrationNotFound, "registration not found") {
			return
		}
	}
	reg, err := a.store.ApproveRegistration(r.Context(), id)
	if err != nil {
		logFailure(r, "approve course registration", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, reg)
}

// cancelRegistration is POST /v1/hr/courses/registrations/{id}/cancel (EssLearning).
func (a *API) cancelRegistration(w http.ResponseWriter, r *http.Request, id string) {
	reg, changed, err := a.store.CancelRegistration(r.Context(), id, crsOwnOnly(r))
	if err != nil {
		logFailure(r, "cancel course registration", err)
		httpx.Fail(w, r, err)
		return
	}
	if changed {
		a.cancellations.Add(1)
	}
	httpx.JSON(w, http.StatusOK, reg)
}

// completeSession is POST /v1/hr/courses/sessions/{id}/complete (HcmLearningAdmin).
func (a *API) completeSession(w http.ResponseWriter, r *http.Request, id string) {
	var req completionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	outcomes, fields := parseCompletion(req.Results)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	res, err := a.store.CompleteSession(r.Context(), id, outcomes)
	if err != nil {
		logFailure(r, "complete course session", err)
		httpx.Fail(w, r, err)
		return
	}
	a.passes.Add(int64(res.Passed))
	httpx.JSON(w, http.StatusOK, res)
}
