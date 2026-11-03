package api

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// assignmentsPath serves procedure determination (PRD 5.26.13, reference_procedure): which
// pricing procedure a legal entity and sales channel resolve with. Reading takes pricing.view;
// changing takes pricing.admin (PricingAdmin, with the procedures).
const assignmentsPath = "/v1/pricing/procedure-assignments"

type assignmentRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	SalesChannel  string `json:"salesChannel"`
	ProcedureName string `json:"procedureName"`
	// Version is the version read (0 when the channel has no assignment).
	Version int `json:"version"`
}

type assignmentView struct {
	LegalEntityID string     `json:"legalEntityId"`
	SalesChannel  string     `json:"salesChannel"`
	ProcedureName string     `json:"procedureName"`
	Version       int        `json:"version"`
	UpdatedBy     string     `json:"updatedBy,omitempty"`
	UpdatedAt     *time.Time `json:"updatedAt,omitempty"`
}

func assignmentResponse(a store.ProcedureAssignment) assignmentView {
	return assignmentView{LegalEntityID: a.LegalEntityID, SalesChannel: a.SalesChannel, ProcedureName: a.ProcedureName,
		Version: a.Version, UpdatedBy: a.UpdatedBy, UpdatedAt: a.UpdatedAt}
}

func (a *API) listAssignments(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	if !legalEntityInScope(w, r, le) {
		return
	}
	rows, err := a.store.ListProcedureAssignments(r.Context(), le)
	if err != nil {
		slog.Error("list procedure assignments failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	out := make([]assignmentView, 0, len(rows))
	for _, row := range rows {
		out = append(out, assignmentResponse(row))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) setAssignment(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req assignmentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if msg := domain.ValidateSalesChannel(req.SalesChannel); msg != "" {
		fields["salesChannel"] = msg
	}
	if msg := domain.ValidateProcedureName(req.ProcedureName); msg != "" {
		fields["procedureName"] = msg
	}
	if req.Version < 0 {
		fields["version"] = "must be the version read, or 0 when none was stored"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !legalEntityInScope(w, r, req.LegalEntityID) {
		return
	}
	row, err := a.store.SetProcedureAssignment(r.Context(), store.ProcedureAssignment{LegalEntityID: req.LegalEntityID,
		SalesChannel: req.SalesChannel, ProcedureName: req.ProcedureName}, req.Version, r.Header.Get(idempotencyKey))
	if err != nil {
		slog.Error("set procedure assignment failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, assignmentResponse(row))
}

func (a *API) clearAssignment(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	version, verr := strconv.Atoi(q.Get("version"))
	fields := map[string]string{}
	if !ids.Valid(q.Get("legalEntityId")) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if msg := domain.ValidateSalesChannel(q.Get("salesChannel")); msg != "" {
		fields["salesChannel"] = msg
	}
	if verr != nil || version < 1 {
		fields["version"] = "must be the version read, 1 or more"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !legalEntityInScope(w, r, q.Get("legalEntityId")) {
		return
	}
	if err := a.store.ClearProcedureAssignment(r.Context(), q.Get("legalEntityId"), q.Get("salesChannel"), version, r.Header.Get(idempotencyKey)); err != nil {
		slog.Error("clear procedure assignment failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// registerAssignmentRoutes mounts procedure determination.
func (a *API) registerAssignmentRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyView)).Get(assignmentsPath, a.listAssignments)
	r.With(authz.RequireDuty(dutyAdmin)).Put(assignmentsPath, a.setAssignment)
	r.With(authz.RequireDuty(dutyAdmin)).Delete(assignmentsPath, a.clearAssignment)
}
