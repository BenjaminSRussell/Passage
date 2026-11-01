package api

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

const dutyRealign = store.DutyRealign

// registerRealignment mounts dimension realignment runs (FR-11.24): simulate, post, reverse, cancel.
func (a *API) registerRealignment(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{ledger}/realignments", a.listRealignments)
	r.With(authz.RequireDuty(dutyRead)).Get("/ledgers/{ledger}/realignments/{id}", a.getRealignment)
	r.With(authz.RequireDuty(dutyRealign)).Post("/ledgers/{ledger}/realignments", a.createRealignment)
	r.With(authz.RequireDuty(dutyRealign)).Post("/ledgers/{ledger}/realignments/{id}:post", a.postRealignment)
	r.With(authz.RequireDuty(dutyRealign)).Post("/ledgers/{ledger}/realignments/{id}:reverse", a.reverseRealignment)
	r.With(authz.RequireDuty(dutyRealign)).Post("/ledgers/{ledger}/realignments/{id}:cancel", a.cancelRealignment)
}

type realignmentRequest struct {
	Filter  domain.RealignmentFilter  `json:"filter"`
	Mapping domain.RealignmentMapping `json:"mapping"`
}

type realignmentActionRequest struct {
	ReasonCode    string `json:"reasonCode"`
	ReasonComment string `json:"reasonComment"`
	// PostingDate is read by :reverse only (YYYY-MM-DD).
	PostingDate *string `json:"postingDate"`
}

func realignmentID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRealignmentNotFound, "realignment run not found"))
		return "", false
	}
	return id, true
}

func (a *API) listRealignments(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListRealignments(r.Context(), ledgerID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getRealignment(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	id, ok := realignmentID(w, r)
	if !ok {
		return
	}
	run, err := a.store.GetRealignment(r.Context(), ledgerID, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, run)
}

// createRealignment simulates a run (201 proposed; a replayed key answers 200). Nothing is posted.
func (a *API) createRealignment(w http.ResponseWriter, r *http.Request) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req realignmentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	key := idempotencyKey(r, fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	body, _ := json.Marshal(req)
	hash := sha256.Sum256(append([]byte(ledgerID+"|"), body...))
	run, replayed, err := a.store.CreateRealignment(r.Context(), store.RealignmentInput{LedgerID: ledgerID, Filter: req.Filter,
		Mapping: req.Mapping, Key: key, Hash: hash[:], Actor: p.UserID})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		a.replayed.Add(1)
		status = http.StatusOK
	}
	httpx.JSON(w, status, run)
}

type realignmentAct func(*http.Request, store.RealignmentAction) (store.RealignmentRun, error)

// realignmentAction decodes the reason and runs one state transition (post, reverse or cancel).
func (a *API) realignmentAction(w http.ResponseWriter, r *http.Request, needReason, datable bool, act realignmentAct) {
	ledgerID, ok := pathLedger(w, r)
	if !ok {
		return
	}
	id, ok := realignmentID(w, r)
	if !ok {
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req realignmentActionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	key := ""
	if needReason {
		key = idempotencyKey(r, fields)
	}
	if needReason && req.ReasonCode == "" {
		fields["reasonCode"] = "is required"
	}
	var postingDate *time.Time
	if datable && req.PostingDate != nil {
		d, ok := parseDate(*req.PostingDate)
		if !ok {
			fields["postingDate"] = "must be a date as YYYY-MM-DD"
		}
		postingDate = &d
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	run, err := act(r, store.RealignmentAction{LedgerID: ledgerID, RunID: id, ReasonCode: req.ReasonCode, ReasonComment: req.ReasonComment,
		Key: key, Actor: p.UserID, TraceID: traceID(r), Today: a.today(), PostingDate: postingDate, CanPostLedgerSpecific: p.HasDuty(store.DutyPostLedgerSpecific)})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, run)
}

func (a *API) postRealignment(w http.ResponseWriter, r *http.Request) {
	a.realignmentAction(w, r, true, false, func(r *http.Request, in store.RealignmentAction) (store.RealignmentRun, error) {
		return a.store.PostRealignment(r.Context(), in)
	})
}

func (a *API) reverseRealignment(w http.ResponseWriter, r *http.Request) {
	a.realignmentAction(w, r, true, true, func(r *http.Request, in store.RealignmentAction) (store.RealignmentRun, error) {
		return a.store.ReverseRealignment(r.Context(), in)
	})
}

func (a *API) cancelRealignment(w http.ResponseWriter, r *http.Request) {
	a.realignmentAction(w, r, false, false, func(r *http.Request, in store.RealignmentAction) (store.RealignmentRun, error) {
		return a.store.CancelRealignment(r.Context(), in)
	})
}
