package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

// maxSequence bounds a trigger point's sequence number.
const maxSequence = 9999

// triggerRequest defines a trigger point on an operation of an order (FR-21.17).
type triggerRequest struct {
	OperNumber int                       `json:"operNumber"`
	Sequence   int                       `json:"sequence"`
	Condition  string                    `json:"condition"`
	Event      domain.TriggerEvent       `json:"event"`
	Function   domain.TriggerFunction    `json:"function"`
	Repeatable bool                      `json:"repeatable"`
	Operations []domain.TriggerOperation `json:"operations"`
}

// createTriggerPoint serves POST /orders/{id}/trigger-points. Trigger points are
// production setup (PRD 4.21.17 "Maintain production setup"); one that creates rework
// orders also needs the rework duty, since it creates them on the definer's authority.
func (a *API) createTriggerPoint(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := orderID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req triggerRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	key, fields := idempotencyKey(r)
	validateTrigger(req, fields)
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	if req.Function == domain.FnCreateReworkOrder && !p.HasDuty(DutyRework) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "a trigger point that creates rework orders needs the duty "+DutyRework))
		return
	}
	cond, _ := domain.ParseCondition(req.Condition)
	tp := domain.TriggerPoint{OperNumber: req.OperNumber, Sequence: req.Sequence, Condition: cond.String(), Event: req.Event.Or(), Function: req.Function,
		Operations: req.Operations, Repeatable: req.Repeatable}
	if err := domain.CheckTriggerPoint(tp); err != nil {
		a.refuse(w, r, problemFrom(err))
		return
	}
	saved, created, err := a.store.CreateTriggerPoint(r.Context(), store.NewTriggerPoint{OrderID: id, OperNumber: tp.OperNumber,
		Sequence: tp.Sequence, Condition: tp.Condition, Event: tp.Event, Function: tp.Function, Operations: tp.Operations, Repeatable: tp.Repeatable,
		Actor: p.UserID, IdempotencyKey: key, RequestHash: hashOf(struct {
			Order string
			Req   triggerRequest
		}{id, req})})
	if err != nil {
		a.refuse(w, r, err)
		return
	}
	if !created {
		a.replayed.Add(1)
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, saved)
		return
	}
	httpx.JSON(w, http.StatusCreated, saved)
}

// validateTrigger checks the shape of a definition; domain.CheckTriggerPoint applies the
// function rules and the store the rules that need the order.
func validateTrigger(req triggerRequest, fields map[string]string) {
	if req.OperNumber < 1 {
		fields["operNumber"] = "must be a positive whole number"
	}
	if req.Sequence < 1 || req.Sequence > maxSequence {
		fields["sequence"] = fmt.Sprintf("must be a whole number from 1 to %d", maxSequence)
	}
	if strings.TrimSpace(req.Condition) == "" || len(req.Condition) > domain.MaxConditionLength {
		fields["condition"] = fmt.Sprintf("is required, at most %d characters, for example 'qty_error > 0' or 'error_pct >= 5 and hours > 2'", domain.MaxConditionLength)
	}
	if !req.Event.Valid() {
		fields["event"] = "must be feedback or operation-finished"
	}
	if !req.Function.Valid() {
		fields["function"] = "must be insert-operations, create-rework-order, release-next, start-workflow or notify"
	}
	if len(req.Operations) > domain.MaxTriggerOperations {
		fields["operations"] = fmt.Sprintf("at most %d operations", domain.MaxTriggerOperations)
	}
	for i, op := range req.Operations {
		at := fmt.Sprintf("operations[%d]", i)
		if !ids.Valid(op.ResourceID) {
			fields[at+".resourceId"] = "must be a UUID"
		}
		for name, v := range map[string]decimal.Decimal{"setupTimeH": op.SetupH, "runTimeHPer": op.RunHPer, "scrapPct": op.ScrapPct,
			"laborRate": op.LaborRate, "overheadRate": op.OverheadRate} {
			checkQuantity(at+"."+name, v, false, fields)
		}
	}
}

// listTriggerPoints serves GET /orders/{id}/trigger-points with each point's firings.
func (a *API) listTriggerPoints(w http.ResponseWriter, r *http.Request) {
	id, ok := orderID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	items, err := a.store.ListTriggerPoints(r.Context(), id)
	if err != nil {
		a.fail(w, r, "list production trigger points", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"orderId": id, "items": items})
}

// deleteTriggerPoint serves DELETE /trigger-points/{id}: only a trigger point that never fired.
func (a *API) deleteTriggerPoint(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeTriggerPointNotFound, "trigger point not found"))
		return
	}
	if err := a.store.DeleteTriggerPoint(r.Context(), id); err != nil {
		a.refuse(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
