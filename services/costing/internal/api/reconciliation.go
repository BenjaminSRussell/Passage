package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/costing/internal/store"
)

// dutyReconcile runs and stores reconciliations: the costing.reconcile job's
// service duty, also given to cost accountants who run one by hand.
const dutyReconcile = "costing.reconcile"

// createReconciliation runs the value report tie-out against the ledger
// balances in the request and stores it as a reconciliation run. Every group
// that differs writes one reconciliation.diff event in the same transaction.
// Until the ledger balance read exists, the caller (the daily job or a user)
// supplies the balances, as for the read-only tie-out.
func (a *API) createReconciliation(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req tieOutRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	key := strings.TrimSpace(r.Header.Get(idempotencyKey))
	fields := map[string]string{}
	if key == "" || len(key) > maxIdempotencyKey {
		fields[idempotencyKey] = fmt.Sprintf("is required, at most %d characters", maxIdempotencyKey)
	}
	gl := parseTieOut(fields, &req)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	res, err := a.tieOut(r, req.reportParams, gl)
	if err != nil {
		a.fail(w, r, "reconciliation", err)
		return
	}
	run := store.ReconciliationRun{LegalEntityID: res.LegalEntityID, AsOf: res.AsOf, ValuationLedgerID: res.ValuationLedgerID,
		ValuationLedgerCode: res.ValuationLedgerCode, SiteID: res.SiteID, Currency: res.Currency, GroupBy: res.GroupBy,
		Differences: res.Differences, TotalDiff: res.TotalDiff, Ties: res.Ties, Rows: make([]store.ReconciliationRow, 0, len(res.Rows))}
	for _, row := range res.Rows {
		run.Rows = append(run.Rows, store.ReconciliationRow{Key: row.Key, Inventory: row.Inventory, Ledger: row.Ledger, Diff: row.Diff})
	}
	stored, created, err := a.store.CreateReconciliationRun(r.Context(), run, key, requestHash(req))
	if err != nil {
		a.fail(w, r, "store reconciliation", err)
		return
	}
	if !created {
		httpx.JSON(w, http.StatusOK, stored)
		return
	}
	a.reconciliations.Add(1)
	a.reconciliationDiffs.Add(int64(stored.Differences))
	httpx.JSON(w, http.StatusCreated, stored)
}

type reconciliationListResponse struct {
	Items      []store.ReconciliationRun `json:"items"`
	NextCursor string                    `json:"nextCursor,omitempty"`
}

func (a *API) listReconciliations(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, fields := pageParams(r, false)
	if date, _, _ := strings.Cut(after, "|"); after != "" {
		parseDate(fields, "cursor", date)
	}
	le, ok := legalEntityFilter(w, r, fields)
	if !ok {
		return
	}
	page, err := a.store.ListReconciliationRuns(r.Context(), le, after, limit)
	if err != nil {
		a.fail(w, r, "list reconciliations", err)
		return
	}
	httpx.JSON(w, http.StatusOK, reconciliationListResponse{Items: page.Items, NextCursor: cursor(page.Next)})
}

func (a *API) getReconciliation(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeReconciliationNotFound, "reconciliation run not found"))
		return
	}
	run, err := a.store.GetReconciliationRun(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get reconciliation", err)
		return
	}
	httpx.JSON(w, http.StatusOK, run)
}
