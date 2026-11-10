package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

const (
	dutyReconView    = "treasury.reconciliation.view"
	dutyReconClose   = "treasury.reconciliation.close"
	dutyReconReopen  = "treasury.reconciliation.reopen"
	dutyReconPost    = "treasury.reconciliation.post"
	dutyReconAbandon = "treasury.reconciliation.abandon"

	CodeReconNotEnabled = "TRS-0514"
	CodeLedgerNoProfile = "TRS-0515"
	CodeLedgerRefused   = "TRS-0518"
	CodeLedgerDown      = "TRS-0519"
	CodeLedgerHeld      = "TRS-0522"
)

type Reconciliations interface {
	CreateReconciliation(ctx context.Context, in store.NewReconciliation, key string) (store.Reconciliation, error)
	GetReconciliation(ctx context.Context, id string) (store.Reconciliation, error)
	ListReconciliations(ctx context.Context, q store.ReconciliationQuery) (store.ReconciliationPage, error)
	SetReconciliationBalances(ctx context.Context, id string, version int, book, outstanding decimal.Decimal) (store.Reconciliation, error)
	CloseReconciliation(ctx context.Context, id string, version int) (store.Reconciliation, error)
	ReopenReconciliation(ctx context.Context, id string, version int, reason string) (store.Reconciliation, error)
	ExcludeLine(ctx context.Context, reconciliationID string, lineNo int, reason string) (store.StatementLine, error)
	IncludeLine(ctx context.Context, reconciliationID string, lineNo int) (store.StatementLine, error)
	BeginLineTransaction(ctx context.Context, in store.NewLineTransaction) (store.LineTransaction, error)
	FinishLineTransaction(ctx context.Context, id, ledgerEntryID string) (store.LineTransaction, error)
	RefuseLineTransaction(ctx context.Context, id, code string) error
	HoldLineTransaction(ctx context.Context, id, code string) error
	MarkLineAttempt(ctx context.Context, id string) error
	AbandonLineTransaction(ctx context.Context, reconciliationID string, lineNo int, reason string) (store.LineTransaction, error)
}

type reconCounters struct {
	reconCreated   atomic.Int64
	reconClosed    atomic.Int64
	reconReopened  atomic.Int64
	linesExcluded  atomic.Int64
	lineTxPosted   atomic.Int64
	lineTxRefused  atomic.Int64
	lineTxNoAnswer atomic.Int64
}

func (a *API) WithReconciliations(r Reconciliations) *API {
	a.recons = r
	return a
}

func (a *API) registerBankReconciliation(r chi.Router) {
	r.With(authz.RequireDuty(dutyReconcile)).Post("/v1/treasury/bank-reconciliations", a.createBankRecon)
	r.With(authz.RequireDuty(dutyReconView)).Get("/v1/treasury/bank-reconciliations", a.listBankRecons)
	r.With(authz.RequireDuty(dutyReconView)).Get("/v1/treasury/bank-reconciliations/{id}", a.getBankRecon)
	r.With(authz.RequireDuty(dutyReconcile)).Post("/v1/treasury/bank-reconciliations/{id}:set-balances", a.setReconBalances)
	r.With(authz.RequireDuty(dutyReconClose)).Post("/v1/treasury/bank-reconciliations/{id}:close", a.closeBankRecon)
	r.With(authz.RequireDuty(dutyReconReopen)).Post("/v1/treasury/bank-reconciliations/{id}:reopen", a.reopenBankRecon)
	r.With(authz.RequireDuty(dutyReconcile)).Post("/v1/treasury/bank-reconciliations/{id}/lines/{lineNo}:exclude", a.excludeLine)
	r.With(authz.RequireDuty(dutyReconcile)).Post("/v1/treasury/bank-reconciliations/{id}/lines/{lineNo}:include", a.includeLine)
	r.With(authz.RequireDuty(dutyReconPost)).Post("/v1/treasury/bank-reconciliations/{id}/lines/{lineNo}:create-transaction", a.createLineTransaction)
	r.With(authz.RequireDuty(dutyReconAbandon)).Post("/v1/treasury/bank-reconciliations/{id}/lines/{lineNo}:abandon", a.abandonLineTransaction)
}

func (a *API) reconsReady(w http.ResponseWriter, r *http.Request) bool {
	if a.recons == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeReconNotEnabled, "bank reconciliation is not enabled on this service"))
		return false
	}
	return validTenant(w, r)
}

type createBankReconRequest struct {
	StatementID       string `json:"statementId"`
	BookBalance       string `json:"bookBalance"`
	OutstandingAmount string `json:"outstandingAmount"`
}

type setBalancesRequest struct {
	BookBalance       string `json:"bookBalance"`
	OutstandingAmount string `json:"outstandingAmount"`
}

type reasonRequest struct {
	Reason string `json:"reason"`
}

type lineTransactionRequest struct {
	Kind string `json:"kind"`
}

func signedAmount(raw string) (decimal.Decimal, bool) {
	d, err := decimal.NewFromString(strings.TrimSpace(raw))
	return d, err == nil && d.Abs().LessThan(maxAmount)
}

func (a *API) createBankRecon(w http.ResponseWriter, r *http.Request) {
	if !a.reconsReady(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req createBankReconRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.StatementID) {
		fields["statementId"] = "must be a UUID"
	}
	book, bookOK := signedAmount(req.BookBalance)
	if !bookOK {
		fields["bookBalance"] = "must be a decimal string"
	}
	outstanding := decimal.Zero
	if strings.TrimSpace(req.OutstandingAmount) != "" {
		var outOK bool
		if outstanding, outOK = signedAmount(req.OutstandingAmount); !outOK {
			fields["outstandingAmount"] = "must be a decimal string"
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	created, err := a.recons.CreateReconciliation(r.Context(), store.NewReconciliation{
		StatementID: strings.ToLower(req.StatementID), BookBalance: book, OutstandingAmount: outstanding}, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.reconCreated.Add(1)
	setETag(w, created.Version)
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *API) listBankRecons(w http.ResponseWriter, r *http.Request) {
	if !a.reconsReady(w, r) {
		return
	}
	query := r.URL.Query()
	q := store.ReconciliationQuery{LegalEntityID: query.Get("legalEntityId"), BankAccountID: query.Get("bankAccountId"),
		State: query.Get("state"), PeriodFrom: query.Get("periodFrom"), PeriodTo: query.Get("periodTo"), Cursor: query.Get("cursor")}
	fields := map[string]string{}
	for name, id := range map[string]*string{"legalEntityId": &q.LegalEntityID, "bankAccountId": &q.BankAccountID} {
		if *id == "" {
			continue
		}
		canonical, ok := ids.Canonical(*id)
		if !ok {
			fields[name] = "must be a UUID"
		}
		*id = canonical
	}
	if q.State != "" && q.State != store.StateOpen && q.State != store.StateClosed && q.State != store.StateReopened {
		fields["state"] = "open, closed or reopened"
	}
	for name, day := range map[string]string{"periodFrom": q.PeriodFrom, "periodTo": q.PeriodTo} {
		if day != "" && !validDay(day) {
			fields[name] = "must be a YYYY-MM-DD date"
		}
	}
	limit, ok := intQuery(w, r, "limit", store.MaxReconciliationsListed)
	if !ok {
		return
	}
	if limit < 1 || limit > store.MaxReconciliationsListed {
		fields["limit"] = "1 to " + strconv.Itoa(store.MaxReconciliationsListed)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	q.Limit = limit
	page, err := a.recons.ListReconciliations(r.Context(), q)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *API) getBankRecon(w http.ResponseWriter, r *http.Request) {
	if !a.reconsReady(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeReconNotFound, "reconciliation not found")
	if !ok {
		return
	}
	rec, err := a.recons.GetReconciliation(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, rec.Version)
	httpx.JSON(w, http.StatusOK, rec)
}

func (a *API) setReconBalances(w http.ResponseWriter, r *http.Request) {
	if !a.reconsReady(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeReconNotFound, "reconciliation not found")
	if !ok {
		return
	}
	version, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	var req setBalancesRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	book, bookOK := signedAmount(req.BookBalance)
	if !bookOK {
		fields["bookBalance"] = "must be a decimal string"
	}
	outstanding, outOK := signedAmount(req.OutstandingAmount)
	if !outOK {
		fields["outstandingAmount"] = "must be a decimal string"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	rec, err := a.recons.SetReconciliationBalances(r.Context(), id, version, book, outstanding)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, rec.Version)
	httpx.JSON(w, http.StatusOK, rec)
}

func (a *API) closeBankRecon(w http.ResponseWriter, r *http.Request) {
	if !a.reconsReady(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeReconNotFound, "reconciliation not found")
	if !ok {
		return
	}
	version, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	rec, err := a.recons.CloseReconciliation(r.Context(), id, version)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if !rec.Replayed {
		a.reconClosed.Add(1)
	}
	setETag(w, rec.Version)
	httpx.JSON(w, http.StatusOK, rec)
}

func (a *API) reopenBankRecon(w http.ResponseWriter, r *http.Request) {
	if !a.reconsReady(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeReconNotFound, "reconciliation not found")
	if !ok {
		return
	}
	version, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	reason, ok := requireReason(w, r)
	if !ok {
		return
	}
	rec, err := a.recons.ReopenReconciliation(r.Context(), id, version, reason)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.reconReopened.Add(1)
	setETag(w, rec.Version)
	httpx.JSON(w, http.StatusOK, rec)
}

func requireReason(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req reasonRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return "", false
	}
	reason := strings.TrimSpace(req.Reason)
	if !domain.ValidVoidReason(reason) {
		validationFailed(w, r, map[string]string{"reason": "required, 1 to 200 characters"})
		return "", false
	}
	return reason, true
}

func reconLine(w http.ResponseWriter, r *http.Request) (id string, lineNo int, ok bool) {
	if id, ok = pathID(w, r, store.CodeReconNotFound, "reconciliation not found"); !ok {
		return "", 0, false
	}
	lineNo, err := strconv.Atoi(chi.URLParam(r, "lineNo"))
	if err != nil || lineNo < 1 {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeLineNotFound, "the statement line is not on this reconciliation"))
		return "", 0, false
	}
	return id, lineNo, true
}

func (a *API) excludeLine(w http.ResponseWriter, r *http.Request) {
	if !a.reconsReady(w, r) {
		return
	}
	id, lineNo, ok := reconLine(w, r)
	if !ok {
		return
	}
	reason, ok := requireReason(w, r)
	if !ok {
		return
	}
	line, err := a.recons.ExcludeLine(r.Context(), id, lineNo, reason)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.linesExcluded.Add(1)
	httpx.JSON(w, http.StatusOK, line)
}

func (a *API) abandonLineTransaction(w http.ResponseWriter, r *http.Request) {
	if !a.reconsReady(w, r) {
		return
	}
	id, lineNo, ok := reconLine(w, r)
	if !ok {
		return
	}
	reason, ok := requireReason(w, r)
	if !ok {
		return
	}
	t, err := a.recons.AbandonLineTransaction(r.Context(), id, lineNo, reason)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusOK, t)
}

func (a *API) includeLine(w http.ResponseWriter, r *http.Request) {
	if !a.reconsReady(w, r) {
		return
	}
	id, lineNo, ok := reconLine(w, r)
	if !ok {
		return
	}
	line, err := a.recons.IncludeLine(r.Context(), id, lineNo)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	httpx.JSON(w, http.StatusOK, line)
}
