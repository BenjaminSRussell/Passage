package api

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

var changeStatuses = []string{domain.ChangeApplied, domain.ChangePending, domain.ChangeApproved, domain.ChangeRejected, domain.ChangeCanceled}

// registerProfileChanges mounts the ESS change requests and their two
// approval queues: a manager's (hr.team.approve, direct reports' address
// changes) and HR's (hr.worker.maintain, every change in the tenant).
func (a *API) registerProfileChanges(r chi.Router) {
	r.With(authz.RequireDuty(dutyEssBase)).Post("/v1/hr/me/profile-changes", a.requestProfileChange)
	r.With(authz.RequireDuty(dutyEssBase)).Get("/v1/hr/me/profile-changes", a.myProfileChanges)
	r.With(authz.RequireDuty(dutyEssBase)).Post("/v1/hr/me/profile-changes/{id}/cancel", changeRoute(a.cancelProfileChange))
	r.With(authz.RequireDuty(dutyMssApprove)).Get("/v1/hr/me/team/profile-changes", a.listProfileChanges(true))
	r.With(authz.RequireDuty(dutyMssApprove)).Post("/v1/hr/me/team/profile-changes/{id}/decide", changeRoute(a.decideProfileChange(true)))
	r.With(authz.RequireDuty(dutyWorkerMaintain)).Get("/v1/hr/profile-changes", a.listProfileChanges(false))
	r.With(authz.RequireDuty(dutyWorkerMaintain)).Post("/v1/hr/profile-changes/{id}/decide", changeRoute(a.decideProfileChange(false)))
}

func changeRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return absenceRoute(store.CodeProfileChangeNotFound, "profile change not found", next)
}

type profileChangeRequest struct {
	Kind          string          `json:"kind"`
	PreferredName *string         `json:"preferredName,omitempty"`
	Address       *domain.Address `json:"address,omitempty"`
	BankAccount   *string         `json:"bankAccount,omitempty"`
}

// requestProfileChange is POST /v1/hr/me/profile-changes (hr.profile.self). The
// worker is the caller's own; nothing in the body names one.
func (a *API) requestProfileChange(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req profileChangeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := store.ProfileChangeInput{Kind: req.Kind}
	fields := map[string]string{}
	switch req.Kind {
	case domain.ChangePreferredName:
		if req.PreferredName == nil {
			fields["preferredName"] = "is required; send an empty string to clear it"
		} else if name, ok := domain.NormalizePreferredName(*req.PreferredName); !ok {
			fields["preferredName"] = "must be at most 100 characters"
		} else {
			in.PreferredName = name
		}
	case domain.ChangeHomeAddress:
		if req.Address == nil {
			fields["address"] = "is required"
		} else {
			in.Address = req.Address.Normalize()
			for k, v := range in.Address.Validate() {
				fields[k] = v
			}
		}
	case domain.ChangeBankAccount:
		account, ok := "", false
		if req.BankAccount != nil {
			account, ok = domain.NormalizeBankAccount(*req.BankAccount)
		}
		if !ok {
			fields["bankAccount"] = "must be 8 to 34 letters and digits"
		}
		in.BankAccount = account
	default:
		fields["kind"] = "must be PREFERRED_NAME, HOME_ADDRESS or BANK_ACCOUNT"
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
	change, err := a.store.RequestProfileChange(r.Context(), self, in)
	if err != nil {
		a.failStore(w, r, "request profile change", err)
		return
	}
	a.profileChanges.Add(1)
	httpx.JSON(w, http.StatusCreated, change)
}

func changeFilter(w http.ResponseWriter, r *http.Request) (store.ProfileChangeFilter, int, string, bool) {
	limit, after, ok := pageParams(r)
	if !ok || (after != "" && !ids.Valid(after)) {
		badPage(w, r)
		return store.ProfileChangeFilter{}, 0, "", false
	}
	q := r.URL.Query()
	f := store.ProfileChangeFilter{Status: q.Get("status"), WorkerID: q.Get("workerId")}
	fields := map[string]string{}
	if f.Status != "" && !contains(changeStatuses, f.Status) {
		fields["status"] = "must be APPLIED, PENDING, APPROVED, REJECTED or CANCELED"
	}
	if f.WorkerID != "" && !ids.Valid(f.WorkerID) {
		fields["workerId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return f, 0, "", false
	}
	return f, limit, after, true
}

func (a *API) writeChangePage(w http.ResponseWriter, r *http.Request, f store.ProfileChangeFilter, after string, limit int) {
	page, err := a.store.ListProfileChanges(r.Context(), f, after, limit)
	if err != nil {
		a.failStore(w, r, "list profile changes", err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.ProfileChange]{Items: page.Items, NextCursor: cursor(page.NextAfter)})
}

// myProfileChanges is GET /v1/hr/me/profile-changes (hr.profile.self), newest first.
func (a *API) myProfileChanges(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	f, limit, after, ok := changeFilter(w, r)
	if !ok {
		return
	}
	self, err := a.store.SelfWorkerID(r.Context())
	if err != nil {
		a.failStore(w, r, "resolve own worker", err)
		return
	}
	f.WorkerID = self
	a.writeChangePage(w, r, f, after, limit)
}

// listProfileChanges is the approval queue: GET /v1/hr/me/team/profile-changes
// (hr.team.approve, the caller's direct reports' address changes) or GET
// /v1/hr/profile-changes (hr.worker.maintain, the tenant's).
func (a *API) listProfileChanges(team bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		f, limit, after, ok := changeFilter(w, r)
		if !ok {
			return
		}
		if team {
			self, err := a.store.SelfWorkerID(r.Context())
			if err != nil {
				a.failStore(w, r, "resolve own worker", err)
				return
			}
			f.ManagerWorkerID = self
		}
		a.writeChangePage(w, r, f, after, limit)
	}
}

type changeDecisionRequest struct {
	Decision string `json:"decision"`
	Note     string `json:"note"`
}

// decideProfileChange approves or rejects a PENDING change. The team route
// answers 404 for anyone but a direct report and HCM-ESS-005 for a bank
// account; nobody decides their own (HCM-ESS-006).
func (a *API) decideProfileChange(team bool) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, r *http.Request, id string) {
		var req changeDecisionRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		note := strings.TrimSpace(req.Note)
		fields := map[string]string{}
		if !contains(absDecisions, req.Decision) {
			fields["decision"] = "must be APPROVE or REJECT"
		}
		if utf8.RuneCountInString(note) > maxReasonLength {
			fields["note"] = "must be at most 500 characters"
		}
		if len(fields) > 0 {
			validationFailed(w, r, fields)
			return
		}
		manager := ""
		if team {
			self, err := a.store.SelfWorkerID(r.Context())
			if err != nil {
				a.failStore(w, r, "resolve own worker", err)
				return
			}
			manager = self
		}
		change, moved, err := a.store.DecideProfileChange(r.Context(), id, req.Decision, note, manager)
		if err != nil {
			a.failStore(w, r, "decide profile change", err)
			return
		}
		if moved {
			a.profileDecisions.Add(1)
		}
		httpx.JSON(w, http.StatusOK, change)
	}
}

// cancelProfileChange is POST /v1/hr/me/profile-changes/{id}/cancel (hr.profile.self):
// the employee withdraws their own PENDING change.
func (a *API) cancelProfileChange(w http.ResponseWriter, r *http.Request, id string) {
	self, err := a.store.SelfWorkerID(r.Context())
	if err != nil {
		a.failStore(w, r, "resolve own worker", err)
		return
	}
	change, err := a.store.CancelProfileChange(r.Context(), id, self)
	if err != nil {
		a.failStore(w, r, "cancel profile change", err)
		return
	}
	httpx.JSON(w, http.StatusOK, change)
}
