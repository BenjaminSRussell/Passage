package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const (
	dutyPerfManage = "hr.performance.manage"
	dutyPerfAdmin  = "hr.performance.admin"
	dutyPerfSelf   = "hr.performance.self"

	perfMaxTitle       = 200
	perfMaxDescription = 2000
)

type goalRequest struct {
	Title       string  `json:"title"`
	Description *string `json:"description"`
	DueOn       *string `json:"dueOn"`
}

type goalProgressRequest struct {
	Status          *string `json:"status"`
	ProgressPercent *int    `json:"progressPercent"`
}

type reviewRequest struct {
	PeriodStart string `json:"periodStart"`
	PeriodEnd   string `json:"periodEnd"`
}

type selfAssessmentRequest struct {
	Achievements string  `json:"achievements"`
	Development  *string `json:"development"`
}

type signOffRequest struct {
	ManagerComments *string `json:"managerComments"`
	OverallRating   *int    `json:"overallRating"`
}

func (a *API) registerPerformance(r chi.Router) {
	goal := idRoute(store.CodeGoalNotFound, "goal not found", a.updateGoal)
	review := idRoute(store.CodeReviewNotFound, "review not found", a.getReview)
	signOff := idRoute(store.CodeReviewNotFound, "review not found", a.signOffReview)
	selfAssess := idRoute(store.CodeReviewNotFound, "review not found", a.submitSelfAssessment)
	manager := authz.RequireAnyDuty(dutyPerfManage, dutyPerfAdmin)
	r.With(manager).Post("/v1/hr/workers/{id}/goals", workerRoute(a.createGoal))
	r.With(manager).Get("/v1/hr/workers/{id}/goals", workerRoute(a.listGoals))
	r.With(manager).Post("/v1/hr/goals/{id}/progress", goal)
	r.With(manager).Post("/v1/hr/workers/{id}/reviews", workerRoute(a.openReview))
	r.With(manager).Get("/v1/hr/workers/{id}/reviews", workerRoute(a.listReviews))
	r.With(manager).Get("/v1/hr/reviews/{id}", review)
	r.With(manager).Post("/v1/hr/reviews/{id}/sign-off", signOff)
	r.With(authz.RequireDuty(dutyPerfSelf)).Get("/v1/hr/me/goals", a.myGoals)
	r.With(authz.RequireDuty(dutyPerfSelf)).Get("/v1/hr/me/reviews", a.myReviews)
	r.With(authz.RequireDuty(dutyPerfSelf)).Post("/v1/hr/me/reviews/{id}/self-assessment", selfAssess)
}

// confinePerformance lets hr.performance.admin act on any worker; any other
// manager acts on their own record and their direct reports. Everyone else is
// the same 404 as a record that does not exist.
func (a *API) confinePerformance(w http.ResponseWriter, r *http.Request, workerID, code, detail string) bool {
	if caller(r).HasDuty(dutyPerfAdmin) {
		return true
	}
	self, err := a.store.SelfWorkerID(r.Context())
	if err != nil {
		a.failStore(w, r, "resolve own worker", err)
		return false
	}
	if workerID == self {
		return true
	}
	reports, err := a.store.IsDirectReport(r.Context(), self, workerID, today())
	if err != nil {
		a.failStore(w, r, "check direct report", err)
		return false
	}
	if !reports {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, code, detail))
	}
	return reports
}

func (a *API) confineWorker(w http.ResponseWriter, r *http.Request, workerID string) bool {
	return a.confinePerformance(w, r, workerID, store.CodeWorkerNotFound, "worker not found")
}

func (a *API) confineByOwner(w http.ResponseWriter, r *http.Request, lookup func(string) (string, error), id, code, detail string) bool {
	owner, err := lookup(id)
	if err != nil {
		a.failStore(w, r, "resolve owner", err)
		return false
	}
	return a.confinePerformance(w, r, owner, code, detail)
}

func (a *API) createGoal(w http.ResponseWriter, r *http.Request, workerID string) {
	var req goalRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	title := strings.TrimSpace(req.Title)
	if title == "" || len([]rune(title)) > perfMaxTitle {
		fields["title"] = fmt.Sprintf("must be 1-%d characters", perfMaxTitle)
	}
	description, ok := crsText(req.Description, perfMaxDescription)
	if !ok {
		fields["description"] = fmt.Sprintf("must be at most %d characters", perfMaxDescription)
	}
	due, ok := optionalDate(req.DueOn)
	if !ok {
		fields["dueOn"] = "must be YYYY-MM-DD"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !a.confineWorker(w, r, workerID) {
		return
	}
	in := store.Goal{WorkerID: workerID, Title: title, Description: description}
	if due != nil {
		d := due.Format(dateLayout)
		in.DueOn = &d
	}
	goal, inserted, err := a.store.CreateGoal(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		a.failStore(w, r, "create goal", err)
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
		a.goalsCreated.Add(1)
	}
	httpx.JSON(w, status, goal)
}

func (a *API) listGoals(w http.ResponseWriter, r *http.Request, workerID string) {
	if !a.confineWorker(w, r, workerID) {
		return
	}
	a.writeGoals(w, r, workerID)
}

func (a *API) myGoals(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	self, err := a.store.SelfWorkerID(r.Context())
	if err != nil {
		a.failStore(w, r, "resolve own worker", err)
		return
	}
	a.writeGoals(w, r, self)
}

func (a *API) writeGoals(w http.ResponseWriter, r *http.Request, workerID string) {
	items, err := a.store.ListWorkerGoals(r.Context(), workerID)
	if err != nil {
		a.failStore(w, r, "list goals", err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.Goal]{Items: items})
}

func (a *API) updateGoal(w http.ResponseWriter, r *http.Request, id string) {
	var req goalProgressRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.Status == nil && req.ProgressPercent == nil {
		fields["status"] = "give status or progressPercent"
	}
	if req.Status != nil && !domain.ValidGoalStatus(*req.Status) {
		fields["status"] = "must be OPEN, ACHIEVED or CANCELLED"
	}
	if req.ProgressPercent != nil && (*req.ProgressPercent < 0 || *req.ProgressPercent > 100) {
		fields["progressPercent"] = "must be between 0 and 100"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !a.confineByOwner(w, r, func(id string) (string, error) { return a.store.GoalWorker(r.Context(), id) }, id, store.CodeGoalNotFound, "goal not found") {
		return
	}
	goal, err := a.store.UpdateGoal(r.Context(), id, req.Status, req.ProgressPercent)
	if err != nil {
		a.failStore(w, r, "update goal", err)
		return
	}
	httpx.JSON(w, http.StatusOK, goal)
}

func (a *API) openReview(w http.ResponseWriter, r *http.Request, workerID string) {
	var req reviewRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	start, startOK := parseDate(req.PeriodStart)
	end, endOK := parseDate(req.PeriodEnd)
	if !startOK {
		fields["periodStart"] = "must be YYYY-MM-DD"
	}
	if !endOK {
		fields["periodEnd"] = "must be YYYY-MM-DD"
	}
	if startOK && endOK && end.Before(start) {
		fields["periodEnd"] = "must not be before periodStart"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !a.confineWorker(w, r, workerID) {
		return
	}
	review, inserted, err := a.store.OpenReview(r.Context(), workerID, start, end, r.Header.Get(idempotencyKey))
	if err != nil {
		a.failStore(w, r, "open review", err)
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
		a.reviewsOpened.Add(1)
	}
	httpx.JSON(w, status, review)
}

func (a *API) listReviews(w http.ResponseWriter, r *http.Request, workerID string) {
	if !a.confineWorker(w, r, workerID) {
		return
	}
	a.writeReviews(w, r, workerID)
}

func (a *API) myReviews(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	self, err := a.store.SelfWorkerID(r.Context())
	if err != nil {
		a.failStore(w, r, "resolve own worker", err)
		return
	}
	a.writeReviews(w, r, self)
}

func (a *API) writeReviews(w http.ResponseWriter, r *http.Request, workerID string) {
	items, err := a.store.ListWorkerReviews(r.Context(), workerID)
	if err != nil {
		a.failStore(w, r, "list reviews", err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.Review]{Items: items})
}

func (a *API) getReview(w http.ResponseWriter, r *http.Request, id string) {
	if !a.confineByOwner(w, r, func(id string) (string, error) { return a.store.ReviewWorker(r.Context(), id) }, id, store.CodeReviewNotFound, "review not found") {
		return
	}
	review, err := a.store.GetReview(r.Context(), id)
	if err != nil {
		a.failStore(w, r, "get review", err)
		return
	}
	httpx.JSON(w, http.StatusOK, review)
}

func (a *API) submitSelfAssessment(w http.ResponseWriter, r *http.Request, id string) {
	var req selfAssessmentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	achievements := strings.TrimSpace(req.Achievements)
	if achievements == "" || len([]rune(achievements)) > domain.MaxReviewText {
		fields["achievements"] = fmt.Sprintf("must be 1-%d characters", domain.MaxReviewText)
	}
	development, ok := crsText(req.Development, domain.MaxReviewText)
	if !ok {
		fields["development"] = fmt.Sprintf("must be at most %d characters", domain.MaxReviewText)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	self, err := a.store.SelfWorkerID(r.Context())
	if err != nil {
		a.failStore(w, r, "resolve own worker", err)
		return
	}
	review, changed, err := a.store.SubmitSelfAssessment(r.Context(), id, self, achievements, development)
	if err != nil {
		a.failStore(w, r, "submit self-assessment", err)
		return
	}
	if changed {
		a.reviewsSelfSubmitted.Add(1)
	}
	httpx.JSON(w, http.StatusOK, review)
}

func (a *API) signOffReview(w http.ResponseWriter, r *http.Request, id string) {
	var req signOffRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	comments, ok := crsText(req.ManagerComments, domain.MaxReviewText)
	if !ok {
		fields["managerComments"] = fmt.Sprintf("must be at most %d characters", domain.MaxReviewText)
	}
	if req.OverallRating != nil && (*req.OverallRating < domain.MinRating || *req.OverallRating > domain.MaxRating) {
		fields["overallRating"] = fmt.Sprintf("must be between %d and %d", domain.MinRating, domain.MaxRating)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !a.confineByOwner(w, r, func(id string) (string, error) { return a.store.ReviewWorker(r.Context(), id) }, id, store.CodeReviewNotFound, "review not found") {
		return
	}
	signer, err := a.store.SelfWorkerID(r.Context())
	var p *httpx.Problem
	if errors.As(err, &p) && p.Code == store.CodeNoLinkedWorker {
		signer, err = "", nil
	}
	if err != nil {
		a.failStore(w, r, "resolve own worker", err)
		return
	}
	review, changed, err := a.store.SignOffReview(r.Context(), id, signer, comments, req.OverallRating)
	if err != nil {
		a.failStore(w, r, "sign off review", err)
		return
	}
	if changed {
		a.reviewsSignedOff.Add(1)
	}
	httpx.JSON(w, http.StatusOK, review)
}
