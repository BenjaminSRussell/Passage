package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payables/internal/ledger"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
)

const (
	dutyAgingRead      = "aging.read"
	dutyAgingReconcile = "aging.reconcile"
	pathAging          = "/v1/payables/aging"
	pathAgingReconcile = "/v1/payables/aging:reconcile"
	dateLayout         = "2006-01-02"
	periodLayout       = "2006-01"
	groupByVendor      = "vendor"
	groupByVendorGroup = "vendorGroup"
	DefaultControlAcct = "2000"
)

type AgingStore interface {
	AgingRows(ctx context.Context, legalEntityID, asOf string) ([]domain.AgingRow, error)
}

type LedgerBalances interface {
	AccountBalance(ctx context.Context, legalEntityID, ledgerID, account, periodTo string) (ledger.Balance, error)
	LegalEntity(ctx context.Context, legalEntityID, ledgerID string) (string, error)
}

type agingDeps struct {
	store          AgingStore
	ledger         LedgerBalances
	controlAccount string
	reconciled     atomic.Int64
	mismatched     atomic.Int64
	ledgerFaults   atomic.Int64
}

func (a *API) WithAging(store AgingStore, balances LedgerBalances, controlAccount string) *API {
	if controlAccount == "" {
		controlAccount = DefaultControlAcct
	}
	a.aging = &agingDeps{store: store, ledger: balances, controlAccount: controlAccount}
	return a
}

func (a *API) registerAging(r chi.Router) {
	if a.aging == nil {
		return
	}
	r.With(authz.RequireDuty(dutyAgingRead)).Get(pathAging, a.agingReport)
	r.With(authz.RequireDuty(dutyAgingReconcile)).Get(pathAgingReconcile, a.agingReconcile)
}

type agingQuery struct {
	asOf          time.Time
	legalEntityID string
	byVendor      bool
	byVendorGroup bool
}

func (a *API) parseAgingQuery(w http.ResponseWriter, r *http.Request) (agingQuery, bool) {
	if !a.validTenant(w, r) {
		return agingQuery{}, false
	}
	p, _ := tenant.FromContext(r.Context())
	q := r.URL.Query()
	out := agingQuery{asOf: a.now().UTC().Truncate(24 * time.Hour), legalEntityID: q.Get("legal_entity"), byVendor: q.Get("group_by") == groupByVendor, byVendorGroup: q.Get("group_by") == groupByVendorGroup}
	if v := q.Get("as_of"); v != "" {
		t, err := time.Parse(dateLayout, v)
		if err != nil {
			a.reject(w, r, &domain.Error{Code: domain.CodeValidation, Detail: "as_of must be a date as YYYY-MM-DD"})
			return agingQuery{}, false
		}
		out.asOf = t
	}
	if g := q.Get("group_by"); g != "" && g != groupByVendor && g != groupByVendorGroup && g != "none" {
		a.reject(w, r, &domain.Error{Code: domain.CodeValidation, Detail: "group_by must be vendor, vendorGroup or none"})
		return agingQuery{}, false
	}
	if out.legalEntityID != "" && !tenant.InLegalEntityScope(p, out.legalEntityID) {
		a.countClientError(r)
		fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal_entity is outside the caller's legal-entity scope"))
		return agingQuery{}, false
	}
	if out.legalEntityID == "" {
		out.legalEntityID = p.LegalEntityID
	}
	if !ids.Valid(out.legalEntityID) {
		a.reject(w, r, &domain.Error{Code: domain.CodeValidation, Detail: "legal_entity is required and must be a UUID"})
		return agingQuery{}, false
	}
	return out, true
}

type agingResponse struct {
	AsOf          string              `json:"asOf"`
	LegalEntityID string              `json:"legalEntityId"`
	GroupBy       string              `json:"groupBy"`
	Buckets       []domain.AgingTotal `json:"buckets"`
}

func (a *API) agingReport(w http.ResponseWriter, r *http.Request) {
	q, ok := a.parseAgingQuery(w, r)
	if !ok {
		return
	}
	if q.byVendorGroup {
		a.reject(w, r, &domain.Error{Code: domain.CodeValidation, Detail: "group_by=vendorGroup applies to aging:reconcile only"})
		return
	}
	rows, err := a.aging.store.AgingRows(r.Context(), q.legalEntityID, q.asOf.Format(dateLayout))
	if err != nil {
		a.fault(w, r, "aging query failed", err)
		return
	}
	groupBy := "none"
	if q.byVendor {
		groupBy = groupByVendor
	}
	httpx.JSON(w, http.StatusOK, agingResponse{AsOf: q.asOf.Format(dateLayout), LegalEntityID: q.legalEntityID, GroupBy: groupBy, Buckets: domain.Age(q.asOf, rows, q.byVendor)})
}

type reconcileResponse struct {
	AsOf           string                        `json:"asOf"`
	LegalEntityID  string                        `json:"legalEntityId"`
	LedgerID       string                        `json:"ledgerId"`
	ControlAccount string                        `json:"controlAccount"`
	LedgerPeriod   string                        `json:"ledgerPeriod"`
	Currencies     []domain.Reconciliation       `json:"currencies"`
	VendorGroups   *[]domain.GroupReconciliation `json:"vendorGroups,omitempty"`
	Reconciled     bool                          `json:"reconciled"`
}

func (a *API) agingReconcile(w http.ResponseWriter, r *http.Request) {
	q, ok := a.parseAgingQuery(w, r)
	if !ok {
		return
	}
	if q.byVendor {
		a.reject(w, r, &domain.Error{Code: domain.CodeValidation, Detail: "group_by=vendor applies to aging only; reconcile accepts vendorGroup or none"})
		return
	}
	ledgerID := r.URL.Query().Get("ledger")
	if !ids.Valid(ledgerID) {
		a.reject(w, r, &domain.Error{Code: domain.CodeValidation, Detail: "ledger is required and must be a UUID"})
		return
	}
	if a.aging.ledger == nil {
		fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "ledger balance source is not configured"))
		return
	}
	owner, err := a.aging.ledger.LegalEntity(r.Context(), q.legalEntityID, ledgerID)
	if err != nil {
		a.ledgerFailure(w, r, ledgerID, err)
		return
	}
	if owner != q.legalEntityID {
		a.reject(w, r, &domain.Error{Code: domain.CodeValidation, Detail: "ledger does not belong to legal_entity"})
		return
	}
	rows, err := a.aging.store.AgingRows(r.Context(), q.legalEntityID, q.asOf.Format(dateLayout))
	if err != nil {
		a.fault(w, r, "reconciliation aging query failed", err)
		return
	}
	period := q.asOf.Format(periodLayout)
	bal, err := a.aging.ledger.AccountBalance(r.Context(), q.legalEntityID, ledgerID, a.aging.controlAccount, period)
	if err != nil {
		a.ledgerFailure(w, r, ledgerID, err)
		return
	}
	currencies := domain.Reconcile(domain.Age(q.asOf, rows, false), bal.Currency, apBalance(bal))
	all := true
	for _, c := range currencies {
		all = all && c.Reconciled
	}
	if all {
		a.aging.reconciled.Add(1)
	} else {
		a.aging.mismatched.Add(1)
		slog.Warn("ap subledger does not reconcile to the control account", "legalEntityId", q.legalEntityID, "ledgerId", ledgerID, "asOf", q.asOf.Format(dateLayout), "correlationId", middleware.GetReqID(r.Context()))
	}
	var groups *[]domain.GroupReconciliation
	if q.byVendorGroup {
		breakdown := append([]domain.GroupReconciliation{}, domain.GroupBreakdown(rows)...)
		groups = &breakdown
	}
	httpx.JSON(w, http.StatusOK, reconcileResponse{
		AsOf: q.asOf.Format(dateLayout), LegalEntityID: q.legalEntityID, LedgerID: ledgerID, ControlAccount: a.aging.controlAccount,
		LedgerPeriod: period, Currencies: currencies, VendorGroups: groups, Reconciled: all,
	})
}

func (a *API) ledgerFailure(w http.ResponseWriter, r *http.Request, ledgerID string, err error) {
	var se *ledger.StatusError
	if errors.As(err, &se) && se.Status == http.StatusForbidden {
		a.countClientError(r)
		fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "caller lacks ledger.balance.read and ledger.journal.read for the ledger"))
		return
	}
	a.aging.ledgerFaults.Add(1)
	slog.Error("ledger unavailable", "err", err, "ledgerId", ledgerID, "correlationId", middleware.GetReqID(r.Context()))
	fail(w, r, httpx.NewProblem(http.StatusBadGateway, "COM-0502", "ledger balance unavailable"))
}

func apBalance(b ledger.Balance) decimal.Decimal { return b.Credit.Sub(b.Debit) }

func (a *API) fault(w http.ResponseWriter, r *http.Request, msg string, err error) {
	a.serverFaults.Add(1)
	slog.Error(msg, "err", err, "correlationId", middleware.GetReqID(r.Context()))
	if errors.Is(err, store.ErrNoFieldCrypt) {
		a.problem(w, r, http.StatusServiceUnavailable, codeRatesUnconfigured, err.Error())
		return
	}
	fail(w, r, err)
}

func (a *API) writeAgingMetrics(w http.ResponseWriter) {
	if a.aging == nil {
		return
	}
	fmt.Fprintf(w, "# HELP payables_aging_reconciliations_total AP aging reconciliations by outcome.\n# TYPE payables_aging_reconciliations_total counter\npayables_aging_reconciliations_total{outcome=\"reconciled\"} %d\npayables_aging_reconciliations_total{outcome=\"mismatched\"} %d\n", a.aging.reconciled.Load(), a.aging.mismatched.Load())
	fmt.Fprintf(w, "# HELP payables_aging_ledger_faults_total Reconciliations that failed to read the ledger balance.\n# TYPE payables_aging_ledger_faults_total counter\npayables_aging_ledger_faults_total %d\n", a.aging.ledgerFaults.Load())
}
