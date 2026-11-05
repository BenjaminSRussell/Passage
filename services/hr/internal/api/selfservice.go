package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

const (
	dutyEssBase = "hr.profile.self"
	dutyMssBase = "hr.team.manage"
)

type myProfile struct {
	workerDetail
	AsOf store.AsOfView `json:"asOf"`
}

type teamWorker struct {
	ID              string    `json:"id"`
	PersonnelNumber string    `json:"personnelNumber"`
	WorkerType      string    `json:"workerType"`
	LegalName       string    `json:"legalName"`
	PreferredName   *string   `json:"preferredName,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

func teamWorkerOf(w store.Worker) teamWorker {
	return teamWorker{ID: w.ID, PersonnelNumber: w.PersonnelNumber, WorkerType: w.WorkerType, LegalName: w.LegalName,
		PreferredName: w.PreferredName, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt}
}

type teamMemberView struct {
	teamWorker
	AsOf store.AsOfView `json:"asOf"`
}

func (a *API) registerSelfService(r chi.Router) {
	r.With(authz.RequireDuty(dutyEssBase)).Get("/v1/hr/me", a.myProfile)
	r.With(authz.RequireDuty(dutyMssBase)).Get("/v1/hr/me/team", a.myTeam)
	r.With(authz.RequireDuty(dutyMssBase)).Get("/v1/hr/me/team/{id}", workerRoute(a.teamMember))
}

func (a *API) failStore(w http.ResponseWriter, r *http.Request, op string, err error) {
	logFailure(r, op, err)
	httpx.Fail(w, r, err)
}

// myProfile is GET /v1/hr/me (hr.profile.self). The worker is the one linked to the
// caller's sign-in; nothing in the request names a worker, so there is no id
// to tamper with. PII is masked and the read is audited like any masked read.
func (a *API) myProfile(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	self, err := a.store.SelfWorkerID(r.Context())
	if err != nil {
		a.failStore(w, r, "resolve own worker", err)
		return
	}
	worker, err := a.store.GetWorker(r.Context(), self)
	if err != nil {
		a.failStore(w, r, "get own worker", err)
		return
	}
	pii, err := a.store.ReadPII(r.Context(), self, false, "", middleware.GetReqID(r.Context()))
	if err != nil {
		a.failStore(w, r, "own masked pii read", err)
		return
	}
	a.piiMaskedReads.Add(1)
	at := today()
	view, err := a.store.WorkerAsOf(r.Context(), self, at, at.Format(dateLayout))
	if err != nil {
		a.failStore(w, r, "own worker as-of", err)
		return
	}
	httpx.JSON(w, http.StatusOK, myProfile{workerDetail: workerDetail{Worker: worker, PII: pii}, AsOf: view})
}

// myTeam is GET /v1/hr/me/team (hr.team.manage): the caller's direct reports today.
func (a *API) myTeam(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, ok := pageParams(r)
	if !ok {
		badPage(w, r)
		return
	}
	self, err := a.store.SelfWorkerID(r.Context())
	if err != nil {
		a.failStore(w, r, "resolve own worker", err)
		return
	}
	page, err := a.store.Team(r.Context(), self, today(), after, limit)
	if err != nil {
		a.failStore(w, r, "list team", err)
		return
	}
	items := make([]teamWorker, len(page.Items))
	for i, worker := range page.Items {
		items[i] = teamWorkerOf(worker)
	}
	httpx.JSON(w, http.StatusOK, listResponse[teamWorker]{Items: items, NextCursor: cursor(page.NextAfter)})
}

// teamMember is GET /v1/hr/me/team/{id} (hr.team.manage): a direct report's record
// without PII. Any other worker, including one that does not exist, is the
// same 404, so the route cannot be used to probe ids.
func (a *API) teamMember(w http.ResponseWriter, r *http.Request, id string) {
	self, err := a.store.SelfWorkerID(r.Context())
	if err != nil {
		a.failStore(w, r, "resolve own worker", err)
		return
	}
	at := today()
	reports, err := a.store.IsDirectReport(r.Context(), self, id, at)
	if err != nil {
		a.failStore(w, r, "check direct report", err)
		return
	}
	if !reports {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeWorkerNotFound, "worker not found"))
		return
	}
	worker, err := a.store.GetWorker(r.Context(), id)
	if err != nil {
		a.failStore(w, r, "get team member", err)
		return
	}
	view, err := a.store.WorkerAsOf(r.Context(), id, at, at.Format(dateLayout))
	if err != nil {
		a.failStore(w, r, "team member as-of", err)
		return
	}
	httpx.JSON(w, http.StatusOK, teamMemberView{teamWorker: teamWorkerOf(worker), AsOf: view})
}

// listTeamAbsenceRequests answers the absence queue for a manager who is not
// an absence administrator: their direct reports' requests and their own.
func (a *API) listTeamAbsenceRequests(w http.ResponseWriter, r *http.Request, f store.RequestFilter, after string, limit int) {
	self, err := a.store.SelfWorkerID(r.Context())
	if err != nil {
		a.failStore(w, r, "resolve own worker", err)
		return
	}
	page, err := a.store.TeamAbsenceRequests(r.Context(), self, f, after, limit)
	if err != nil {
		a.failStore(w, r, "list team absence requests", err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.AbsenceRequest]{Items: page.Items, NextCursor: cursor(page.NextAfter)})
}

// allowTeamWorker limits a manager (hr.team.approve without hr.absence.admin) to their own
// worker and their direct reports. A worker outside the team answers
// notFoundCode as 404, the same as one that does not exist. Callers without
// hr.team.approve pass through to the handler's own-data check.
func (a *API) allowTeamWorker(w http.ResponseWriter, r *http.Request, workerID, notFoundCode, notFoundDetail string) bool {
	return a.confineToTeam(w, r, caller(r).HasDuty(dutyAbsenceAdmin), workerID, notFoundCode, notFoundDetail)
}

func (a *API) confineToTeam(w http.ResponseWriter, r *http.Request, tenantWide bool, workerID, notFoundCode, notFoundDetail string) bool {
	if !caller(r).HasDuty(dutyMssApprove) || tenantWide {
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
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, notFoundCode, notFoundDetail))
	}
	return reports
}

func (a *API) allowTeamRequest(w http.ResponseWriter, r *http.Request, requestID string) bool {
	p := caller(r)
	if !p.HasDuty(dutyMssApprove) || p.HasDuty(dutyAbsenceAdmin) {
		return true
	}
	workerID, err := a.store.AbsenceRequestWorker(r.Context(), requestID)
	if err != nil {
		a.failStore(w, r, "resolve request worker", err)
		return false
	}
	return a.allowTeamWorker(w, r, workerID, store.CodeAbsenceRequestNotFound, "absence request not found")
}
