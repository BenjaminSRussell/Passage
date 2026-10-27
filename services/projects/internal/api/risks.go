package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
	"github.com/BenjaminSRussell/passage/services/projects/internal/store"
)

// riskRequest is the wire shape of the risk register writes (FR-41.20). Status
// is required on PUT and must be omitted on POST (new entries start OPEN).
type riskRequest struct {
	Kind          string  `json:"kind"`
	Title         string  `json:"title"`
	Description   string  `json:"description,omitempty"`
	Probability   int     `json:"probability"`
	Impact        int     `json:"impact"`
	OwnerWorkerID *string `json:"ownerWorkerId,omitempty"`
	Mitigation    string  `json:"mitigation,omitempty"`
	DueDate       *string `json:"dueDate,omitempty"`
	Status        string  `json:"status,omitempty"`
}

type riskList struct {
	Items []domain.RiskIssue `json:"items"`
}

// registerRisks mounts the risk and issue register of a project. Reads need
// ProjInquire, writes ProjPlan (§8.41.17).
func (a *API) registerRisks(r chi.Router) {
	r.With(authz.RequireDuty(DutyInquire)).Get("/{id}/risks", a.listRisks)
	r.With(authz.RequireDuty(DutyPlan)).Post("/{id}/risks", a.createRisk)
	r.With(authz.RequireDuty(DutyPlan)).Put("/{id}/risks/{riskId}", a.updateRisk)
}

// toRisk maps the request to a register entry and returns its field errors.
func (req riskRequest) toRisk(projectID string) (domain.RiskIssue, map[string]string) {
	in := domain.RiskIssue{
		ProjectID: projectID, Kind: domain.RiskKind(req.Kind), Title: strings.TrimSpace(req.Title), Description: req.Description,
		Probability: req.Probability, Impact: req.Impact, OwnerWorkerID: req.OwnerWorkerID, Mitigation: req.Mitigation,
		DueDate: req.DueDate, Status: domain.RiskStatus(req.Status),
	}
	return in, domain.ValidateRiskIssue(in)
}

func (a *API) listRisks(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	q := r.URL.Query()
	f := store.RiskFilter{ProjectID: chi.URLParam(r, "id"), Kind: domain.RiskKind(q.Get("kind")), Status: domain.RiskStatus(q.Get("status"))}
	fields := map[string]string{}
	if f.Kind != "" && f.Kind != domain.KindRisk && f.Kind != domain.KindIssue {
		fields["kind"] = "must be RISK or ISSUE"
	}
	if f.Status != "" && f.Status != domain.RiskOpen && f.Status != domain.RiskMitigated && f.Status != domain.RiskClosed {
		fields["status"] = "must be OPEN, MITIGATED or CLOSED"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	items, err := a.store.ListRisks(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if items == nil {
		items = []domain.RiskIssue{}
	}
	httpx.JSON(w, http.StatusOK, riskList{Items: items})
}

// createRisk adds an OPEN risk or issue to the project's register.
// Idempotency-Key is required; a replay returns the stored entry with 200.
func (a *API) createRisk(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	var req riskRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	key, keyErr := idempotencyKey(r)
	in, fields := req.toRisk(chi.URLParam(r, "id"))
	for k, v := range keyErr {
		fields[k] = v
	}
	if req.Status != "" {
		fields["status"] = "is OPEN on create; omit it"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	in.Status = domain.RiskOpen
	out, created, err := a.store.CreateRisk(r.Context(), in, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.Version)
	if !created {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, createdStatus(created), out)
}

// updateRisk replaces an entry's fields and status. If-Match carries the entry
// version (428 when absent, 412 when stale); a status move the lifecycle does
// not allow, or any change to a CLOSED entry, is PROJ-RISK-001.
func (a *API) updateRisk(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	riskID := chi.URLParam(r, "riskId")
	if !ids.Valid(riskID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "risk or issue not found"))
		return
	}
	version, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	var req riskRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := req.toRisk(chi.URLParam(r, "id"))
	if req.Status == "" {
		fields["status"] = "is required on update"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	in.ID = riskID
	out, err := a.store.UpdateRisk(r.Context(), version, in)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusOK, out)
}
