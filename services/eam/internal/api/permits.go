package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	"github.com/BenjaminSRussell/passage/services/eam/internal/store"
)

// permitStatuses are the valid permit transition targets (REQUESTED is set on creation).
var permitStatuses = map[domain.PermitStatus]bool{domain.PermitApproved: true, domain.PermitIssued: true,
	domain.PermitSuspended: true, domain.PermitClosed: true, domain.PermitCanceled: true}

var energyTypes = map[string]bool{"ELECTRICAL": true, "PNEUMATIC": true, "HYDRAULIC": true,
	"MECHANICAL": true, "THERMAL": true, "CHEMICAL": true}

// permitStates are the statuses the permit register filters on.
var permitStates = map[string]bool{"REQUESTED": true, "APPROVED": true, "ISSUED": true, "SUSPENDED": true,
	"CLOSED": true, "CANCELED": true}

var lockActions = map[domain.LockAction]bool{domain.LockApply: true, domain.LockVerify: true, domain.LockRemove: true}

func (h *Handler) upsertPermitType(w http.ResponseWriter, r *http.Request) {
	if _, err := idempotencyKey(r); err != nil {
		fail(w, r, err)
		return
	}
	var in store.PermitType
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	switch {
	case !assetType.MatchString(in.Code):
		fail(w, r, invalid("code must match ^[A-Z0-9_]{2,20}$"))
		return
	case in.Name == "" || len(in.Name) > 120:
		fail(w, r, invalid("name must be 1-120 characters"))
		return
	case in.ApproverRole == "" || len(in.ApproverRole) > 60:
		fail(w, r, invalid("approverRole must be 1-60 characters"))
		return
	case in.MaxValidityHours < 1 || in.MaxValidityHours > 168:
		fail(w, r, invalid("maxValidityHours must be 1-168"))
		return
	}
	out, err := h.store.UpsertPermitType(r.Context(), in)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) listPermitTypes(w http.ResponseWriter, r *http.Request) {
	out, err := h.store.ListPermitTypes(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) setJobPermits(w http.ResponseWriter, r *http.Request) {
	jobType := chi.URLParam(r, "jobType")
	if !assetType.MatchString(jobType) {
		fail(w, r, invalid("jobType must match ^[A-Z0-9_]{2,20}$"))
		return
	}
	var body struct {
		PermitTypeCodes []string `json:"permitTypeCodes"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if body.PermitTypeCodes == nil {
		fail(w, r, invalid("permitTypeCodes is required (an empty list clears the requirement)"))
		return
	}
	if len(body.PermitTypeCodes) > 10 {
		fail(w, r, invalid("a job type can require at most 10 permit types"))
		return
	}
	for _, c := range body.PermitTypeCodes {
		if !assetType.MatchString(c) {
			fail(w, r, invalid("permitTypeCodes must match ^[A-Z0-9_]{2,20}$"))
			return
		}
	}
	out, err := h.store.SetJobPermits(r.Context(), jobType, body.PermitTypeCodes)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) getJobPermits(w http.ResponseWriter, r *http.Request) {
	jobType := chi.URLParam(r, "jobType")
	if !assetType.MatchString(jobType) {
		fail(w, r, invalid("jobType must match ^[A-Z0-9_]{2,20}$"))
		return
	}
	out, err := h.store.GetJobPermits(r.Context(), jobType)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) createIsolationPoint(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var in store.IsolationPoint
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	switch {
	case !energyTypes[in.EnergyType]:
		fail(w, r, invalid("energyType must be ELECTRICAL, PNEUMATIC, HYDRAULIC, MECHANICAL, THERMAL or CHEMICAL"))
		return
	case in.LocationText == "" || in.Method == "" || in.VerificationStep == "" || in.TagCode == "":
		fail(w, r, invalid("location, method, verificationStep and tagCode are required"))
		return
	case len(in.LocationText) > 200 || len(in.Method) > 200 || len(in.VerificationStep) > 200 || len(in.TagCode) > 40:
		fail(w, r, invalid("isolation point text fields are too long"))
		return
	}
	out, err := h.store.CreateIsolationPoint(r.Context(), id, in, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) listIsolationPoints(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.ListIsolationPoints(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

type permitIn struct {
	WorkOrderID    string    `json:"workOrderId"`
	PermitTypeCode string    `json:"permitTypeCode"`
	ValidFrom      time.Time `json:"validFrom"`
	ValidTo        time.Time `json:"validTo"`
}

func (h *Handler) requestPermit(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body permitIn
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	switch {
	case !ids.Valid(body.WorkOrderID):
		fail(w, r, invalid("workOrderId must be a UUID"))
		return
	case !assetType.MatchString(body.PermitTypeCode):
		fail(w, r, invalid("permitTypeCode must match ^[A-Z0-9_]{2,20}$"))
		return
	case body.ValidFrom.IsZero() || body.ValidTo.IsZero():
		fail(w, r, invalid("validFrom and validTo are required RFC 3339 timestamps"))
		return
	case !body.ValidTo.After(body.ValidFrom):
		fail(w, r, invalid("validTo must be after validFrom"))
		return
	}
	out, err := h.store.RequestPermit(r.Context(), store.PermitRequest{WorkOrderID: body.WorkOrderID,
		PermitTypeCode: body.PermitTypeCode, ValidFrom: body.ValidFrom.UTC(), ValidTo: body.ValidTo.UTC()}, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) listPermits(w http.ResponseWriter, r *http.Request) {
	opt, err := listOptions(r, permitStates)
	if err != nil {
		fail(w, r, err)
		return
	}
	if wo := r.URL.Query().Get("workOrder"); wo != "" {
		if !ids.Valid(wo) {
			fail(w, r, invalid("workOrder must be a UUID"))
			return
		}
		opt.WorkOrderID = wo
	}
	out, err := h.store.ListPermits(r.Context(), opt)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// expirePermits runs the eam-permit-expiry job (8.44.20) for the caller's
// tenant: ISSUED permits past valid_to are suspended and listed with EAM-PMT-004.
func (h *Handler) expirePermits(w http.ResponseWriter, r *http.Request) {
	if _, err := idempotencyKey(r); err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.ExpirePermits(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) getPermit(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.GetPermit(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", `"`+strconv.Itoa(out.Permit.Version)+`"`)
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) transitionPermit(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body struct {
		To string `json:"to"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	to := domain.PermitStatus(body.To)
	if !permitStatuses[to] {
		fail(w, r, invalid("to must be APPROVED, ISSUED, SUSPENDED, CLOSED or CANCELED"))
		return
	}
	out, err := h.store.TransitionPermit(r.Context(), id, to, version, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type lockIn struct {
	Action           string `json:"action"`
	IsolationPointID string `json:"isolationPointId"`
}

func (h *Handler) recordLock(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body lockIn
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	action := domain.LockAction(body.Action)
	if !lockActions[action] {
		fail(w, r, invalid("action must be APPLY, VERIFY or REMOVE"))
		return
	}
	if !ids.Valid(body.IsolationPointID) {
		fail(w, r, invalid("isolationPointId must be a UUID"))
		return
	}
	// A LOTO lock is personal (29 CFR 1910.147): it is always the caller's own,
	// never applied or removed on someone else's behalf.
	p, _ := tenant.FromContext(r.Context())
	if p.UserID == "" {
		fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "a personal lock needs a signed-in user"))
		return
	}
	out, err := h.store.RecordLock(r.Context(), id, action, body.IsolationPointID, p.UserID, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	status := http.StatusOK
	if action == domain.LockApply {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, out)
}
