package api

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/budget"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// Budget control problem codes (FR-38.4, FR-38.5).
const (
	// CodeRequisitionOverBudget is the 422 for the last approval of a requisition that budget control refused: it is not
	// approved and nothing is reserved.
	CodeRequisitionOverBudget = "PRC-REQ-BUDGET"
	// CodeBudgetUnavailable is the 503 when budget control did not answer: the requisition stays IN_REVIEW, and the approval
	// can be repeated.
	CodeBudgetUnavailable = "PRC-REQ-BUDGET-DOWN"
)

// WithBudget turns on budget control for requisitions: the last approval runs the budget check and reserves
// pre-encumbrances, a cancelled approved requisition gives them back, and the calls that failed are repeated (FR-38.5).
func (a *API) WithBudget(p budget.Port, q budget.Queue) *API {
	a.budget, a.budgetQueue = p, q
	return a
}

// checkRequisitionBudget runs the budget check for the requisition before its last approval. A refusal is 422, an
// unanswered call 503 (budget control is fail-closed here: an unchecked requisition must not become approved).
func (a *API) checkRequisitionBudget(r *http.Request, cur store.Requisition) error {
	if a.budget == nil {
		return nil
	}
	res, err := a.budget.CheckRequisition(r.Context(), cur)
	if err != nil {
		slog.ErrorContext(r.Context(), "budget check of a requisition failed", "err", err, "requisitionId", cur.ID, "correlationId", middleware.GetReqID(r.Context()))
		return httpx.NewProblem(http.StatusServiceUnavailable, CodeBudgetUnavailable, "budget control did not answer, so the requisition was not approved; try the approval again")
	}
	if !res.Failed() {
		return nil
	}
	p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeRequisitionOverBudget,
		"budget control refused the requisition: its lines exceed the available budget, so it was not approved and nothing was reserved. Lower the amounts, move the lines to another account or cost center, or ask for an override")
	lines := map[string]string{}
	byID := map[string]store.RequisitionLine{}
	for _, l := range cur.Lines {
		byID[l.ID] = l
	}
	for _, l := range res.Lines {
		if l.Result == "FAIL" || l.Result == "fail" {
			if rl, ok := byID[l.LineID]; ok {
				lines[fmt.Sprintf("lines[%d]", rl.LineNo-1)] = fmt.Sprintf("over budget by %s%%", l.OverPct.StringFixed(2))
			}
		}
	}
	if len(lines) > 0 {
		p.Errors = lines
	}
	return p
}

func (a *API) registerBudgetCalls(r chi.Router) {
	r.With(authz.RequireDuty(dutyPurchaseOrderInquire)).Get("/budget-calls", a.listBudgetCalls)
	r.With(authz.RequireDuty(dutyPurchaseOrderMaintain)).Post("/budget-calls:retry", a.retryBudgetCalls)
}

// listBudgetCalls handles GET /budget-calls?status=pending|done: the calls to budget control that were owed, oldest first.
func (a *API) listBudgetCalls(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && status != store.BudgetCallPending && status != store.BudgetCallDone {
		fail(w, r, invalid("status", "must be pending or done"))
		return
	}
	limit, _, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	items, err := a.store.ListBudgetCalls(r.Context(), status, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// retryBudgetCalls handles POST /budget-calls:retry: it repeats the pending calls (up to 100, oldest first) and reports how
// many were accepted. Repeating is safe: every call has a stable idempotency key.
func (a *API) retryBudgetCalls(w http.ResponseWriter, r *http.Request) {
	if a.budget == nil {
		fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "PRC-BUDGET-OFF", "budget control is not configured for this service"))
		return
	}
	res, err := budget.Retry(r.Context(), a.budget, a.budgetQueue)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}
