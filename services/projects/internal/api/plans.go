package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
	"github.com/BenjaminSRussell/passage/services/projects/internal/store"
)

// planCreateRequest is the wire shape of POST /v1/projects/{id}/plans.
type planCreateRequest struct {
	Kind              string `json:"kind"`
	CopyFromVersionID string `json:"copyFromVersionId,omitempty"`
}

// wbsRequest is the wire shape of PUT /v1/projects/{id}/plans/{ver}/wbs: the
// whole node and dependency set of one plan version, keyed by WBS code.
type wbsRequest struct {
	Nodes        []domain.WBSNodeInput       `json:"nodes"`
	Dependencies []domain.WBSDependencyInput `json:"dependencies"`
}

// wbsView is a plan version with its nodes and dependencies.
type wbsView struct {
	PlanVersion  domain.PlanVersion `json:"planVersion"`
	Nodes        []domain.WBSNode   `json:"nodes"`
	Dependencies []domain.WBSLink   `json:"dependencies"`
}

// scheduleView is the result of a scheduler run.
type scheduleView struct {
	wbsView
	FinishDate   string   `json:"finishDate,omitempty"`
	CriticalPath []string `json:"criticalPath"`
}

type planList struct {
	Items []domain.PlanVersion `json:"items"`
}

func toWBSView(w store.WBS) wbsView {
	v := wbsView{PlanVersion: w.Plan, Nodes: w.Nodes, Dependencies: w.Links}
	if v.Nodes == nil {
		v.Nodes = []domain.WBSNode{}
	}
	if v.Dependencies == nil {
		v.Dependencies = []domain.WBSLink{}
	}
	return v
}

// planID reads {ver}; a value that is not a UUID cannot name a plan version (404).
func planID(w http.ResponseWriter, r *http.Request) (string, bool) {
	ver := chi.URLParam(r, "ver")
	if !ids.Valid(ver) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "plan version not found"))
		return "", false
	}
	return ver, true
}

func (a *API) listPlans(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	items, err := a.store.ListPlanVersions(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if items == nil {
		items = []domain.PlanVersion{}
	}
	httpx.JSON(w, http.StatusOK, planList{Items: items})
}

// createPlan adds a plan version to a project: an empty or copied WORKING plan,
// a BASELINE copied from WORKING (locked), or a SIMULATION copied from WORKING
// or BASELINE. Idempotency-Key is required; a replay returns the stored version
// with 200 and Idempotent-Replayed: true.
func (a *API) createPlan(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	var req planCreateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	key := strings.TrimSpace(r.Header.Get(idempotencyHeader))
	if key == "" || len(key) > maxIdempotencyKey {
		fields[idempotencyHeader] = fmt.Sprintf("header is required, at most %d characters", maxIdempotencyKey)
	}
	kind := domain.PlanKind(req.Kind)
	if !domain.ValidPlanKind(kind) {
		fields["kind"] = "must be BASELINE, WORKING or SIMULATION"
	}
	if req.CopyFromVersionID != "" && !ids.Valid(req.CopyFromVersionID) {
		fields["copyFromVersionId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	out, created, err := a.store.CreatePlanVersion(r.Context(), chi.URLParam(r, "id"), store.NewPlan{Kind: kind, CopyFromVersionID: req.CopyFromVersionID}, key)
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

func (a *API) getWBS(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	ver, ok := planID(w, r)
	if !ok {
		return
	}
	out, err := a.store.LoadWBS(r.Context(), chi.URLParam(r, "id"), ver)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.Plan.Version)
	httpx.JSON(w, http.StatusOK, toWBSView(out))
}

// putWBS replaces every node and dependency of a plan version. If-Match carries
// the plan version (428 when absent, 412 when stale). Validation rejects
// malformed nodes (400), duplicate codes (PROJ-WBS-002) and dependency cycles
// (PROJ-WBS-001); a locked plan version is PROJ-PLAN-002.
func (a *API) putWBS(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	ver, ok := planID(w, r)
	if !ok {
		return
	}
	expected, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	var req wbsRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	domain.NormalizeWBS(req.Nodes)
	fields, err := domain.ValidateWBS(req.Nodes, req.Dependencies)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	out, err := a.store.ReplaceWBS(r.Context(), chi.URLParam(r, "id"), ver, expected, req.Nodes, req.Dependencies)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.Plan.Version)
	httpx.JSON(w, http.StatusOK, toWBSView(out))
}

// scheduleWBS runs the CPM scheduler, stores early and late dates, slack and
// the critical flag on every node, and emits dev.passage.projects.wbs.scheduled.v1.
// Rerunning on an unchanged WBS gives the same dates, so it takes no key.
func (a *API) scheduleWBS(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	ver, ok := planID(w, r)
	if !ok {
		return
	}
	out, err := a.store.SchedulePlan(r.Context(), chi.URLParam(r, "id"), ver)
	if errors.Is(err, domain.ErrInvalidInput) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeValidation, "the stored WBS cannot be scheduled: "+err.Error()))
		return
	}
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.Plan.Version)
	critical := out.CriticalPath
	if critical == nil {
		critical = []string{}
	}
	httpx.JSON(w, http.StatusOK, scheduleView{wbsView: toWBSView(out.WBS), FinishDate: out.FinishDate, CriticalPath: critical})
}

// transferRequest is the wire shape of POST /v1/projects/{id}/plans/{ver}/transfer.
// Resolutions maps a conflicting WBS code to KEEP_WORKING or TAKE_SIMULATION;
// workingVersion, when given, is the WORKING plan version the diff was read at.
type transferRequest struct {
	Resolutions    map[string]string `json:"resolutions"`
	WorkingVersion int               `json:"workingVersion,omitempty"`
}

// transferView is a simulation transfer: the preview before it, the outcome after it.
type transferView struct {
	Simulation domain.PlanVersion  `json:"simulation"`
	Working    domain.PlanVersion  `json:"working"`
	Applied    []domain.NodeChange `json:"applied"`
	Conflicts  []domain.NodeChange `json:"conflicts"`
}

func toTransferView(t store.Transfer) transferView {
	v := transferView{Simulation: t.Simulation, Working: t.Working, Applied: t.Plan.Applied, Conflicts: t.Plan.Conflicts}
	if v.Applied == nil {
		v.Applied = []domain.NodeChange{}
	}
	if v.Conflicts == nil {
		v.Conflicts = []domain.NodeChange{}
	}
	return v
}

// previewTransfer returns the three-way diff of a SIMULATION against WORKING
// (BR-41.14): the nodes a transfer would apply and the conflicts to resolve. The
// ETag is the simulation's version, to send back as If-Match on the transfer.
func (a *API) previewTransfer(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	ver, ok := planID(w, r)
	if !ok {
		return
	}
	out, err := a.store.PreviewTransfer(r.Context(), chi.URLParam(r, "id"), ver)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.Simulation.Version)
	httpx.JSON(w, http.StatusOK, toTransferView(out))
}

// transferPlan transfers a SIMULATION into WORKING (FR-41.17, BR-41.14).
// If-Match carries the simulation's version (428 when absent, 412 when stale).
// Unresolved conflicts, or a merge that is not a valid WBS, are 422
// PROJ-SIM-001 and nothing changes; a transferred simulation is locked
// (PROJ-PLAN-002 on a second transfer). The ETag is the WORKING plan's new version.
func (a *API) transferPlan(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	ver, ok := planID(w, r)
	if !ok {
		return
	}
	expected, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	var req transferRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	for code, choice := range req.Resolutions {
		if choice != domain.ResolveKeepWorking && choice != domain.ResolveTakeSimulation {
			fields["resolutions."+code] = "must be KEEP_WORKING or TAKE_SIMULATION"
		}
	}
	if req.WorkingVersion < 0 {
		fields["workingVersion"] = "must be a plan version number"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	out, err := a.store.TransferSimulation(r.Context(), chi.URLParam(r, "id"), ver, expected, req.WorkingVersion, req.Resolutions)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.Working.Version)
	httpx.JSON(w, http.StatusOK, toTransferView(out))
}
