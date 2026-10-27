package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// reconciliationsPath is the inventory-to-ledger reconciliation (FR-16.12).
// Running it is the InventoryAdmin duty ("reconciliation re-run", 3.16.17);
// the report is read with inventory.read (the workspace reconciliation tile).
const reconciliationsPath = "/v1/inventory/reconciliations"

type reconcileRequest struct {
	LegalEntityID string  `json:"legalEntityId"`
	Currency      string  `json:"currency"`
	LedgerBalance *string `json:"ledgerBalance"`
}

type reconciliationResponse struct {
	ID             string    `json:"id"`
	LegalEntityID  string    `json:"legalEntityId"`
	Account        string    `json:"account"`
	Currency       string    `json:"currency"`
	RunDate        string    `json:"runDate"`
	InventoryValue string    `json:"inventoryValue"`
	LedgerBalance  string    `json:"ledgerBalance"`
	Diff           string    `json:"diff"`
	Status         string    `json:"status"`
	LedgerSource   string    `json:"ledgerSource"`
	NegativeRows   int       `json:"negativeRows"`
	CreatedAt      time.Time `json:"createdAt"`
}

// registerReconciliation mounts the run, the run list and one run.
func (a *API) registerReconciliation(r chi.Router) {
	r.With(authz.RequireDuty(dutyAdmin)).Post(reconciliationsPath, a.reconcile)
	r.With(authz.RequireDuty(dutyRead)).Get(reconciliationsPath, a.listReconciliations)
	r.With(authz.RequireDuty(dutyRead)).Get(reconciliationsPath+"/{id}", a.getReconciliation)
}

// reconcile runs the check now for one legal entity and currency against the
// configured inventory account. Without ledgerBalance the ledger side is the net
// of the inventory account over the POSTED journals. The run date is today
// (UTC); a re-run on the same day recomputes and overwrites that day's row (the
// result is a function of the data, so no Idempotency-Key is stored), and a
// non-zero difference raises reconciliation.diff.v1.
func (a *API) reconcile(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req reconcileRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	in := store.ReconcileInput{LegalEntityID: req.LegalEntityID, Currency: req.Currency, Account: a.post.InventoryAccount,
		RunDate: time.Now().UTC().Truncate(24 * time.Hour)}
	if req.LedgerBalance != nil {
		d, err := decimal.NewFromString(strings.TrimSpace(*req.LedgerBalance))
		if err != nil || d.Exponent() < -6 {
			fields["ledgerBalance"] = "must be a decimal number with at most 6 decimals"
		} else {
			in.LedgerBalance = &d
		}
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	if in.Account == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeAccountMissing, "no inventory account is configured (INV_INVENTORY_ACCOUNT)"))
		return
	}
	res, err := a.store.Reconcile(r.Context(), in)
	if err != nil {
		a.failed(w, r, "reconcile inventory", err)
		return
	}
	httpx.JSON(w, http.StatusOK, toReconciliationResponse(res))
}

// listReconciliations lists runs newest first, by legal entity and status.
func (a *API) listReconciliations(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.ReconciliationFilter{LegalEntityID: strings.TrimSpace(q.Get("legalEntityId")), Status: strings.TrimSpace(q.Get("status"))}
	fields := map[string]string{}
	if f.LegalEntityID != "" && !ids.Valid(f.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if f.Status != "" && f.Status != domain.ReconciliationMatched && f.Status != domain.ReconciliationDifference {
		fields["status"] = "must be MATCHED or DIFFERENCE"
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	runs, err := a.store.ListReconciliations(r.Context(), f)
	if err != nil {
		a.failed(w, r, "list reconciliations", err)
		return
	}
	items := make([]reconciliationResponse, 0, len(runs))
	for _, run := range runs {
		items = append(items, toReconciliationResponse(run))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getReconciliation(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeReconciliationNotFound, "reconciliation run not found"))
		return
	}
	run, err := a.store.GetReconciliation(r.Context(), id)
	if err != nil {
		a.failed(w, r, "get reconciliation", err)
		return
	}
	httpx.JSON(w, http.StatusOK, toReconciliationResponse(run))
}

func toReconciliationResponse(run domain.Reconciliation) reconciliationResponse {
	return reconciliationResponse{ID: run.ID, LegalEntityID: run.LegalEntityID, Account: run.Account, Currency: run.Currency,
		RunDate: run.RunDate.Format(dateFormat), InventoryValue: run.InventoryValue.String(), LedgerBalance: run.LedgerBalance.String(),
		Diff: run.Diff.String(), Status: run.Status, LedgerSource: run.LedgerSource, NegativeRows: run.NegativeRows, CreatedAt: run.CreatedAt}
}
