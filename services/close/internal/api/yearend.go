package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
	"github.com/BenjaminSRussell/passage/services/close/internal/store"
)

const (
	dutyYearEndRun  = "close.yearend.run"
	dutyYearEndRead = "close.yearend.read"
	maxBalances     = 5000
	yearEndPath     = "/v1/close/year-end-runs"
)

var (
	fiscalYearPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,20}$`)
	currencyPattern   = regexp.MustCompile(`^[A-Z]{3}$`)
)

type YearEndStore interface {
	BeginYearEnd(ctx context.Context, in domain.YearEndRun) (domain.YearEndRun, bool, error)
	CompleteYearEnd(ctx context.Context, runID, closingJournalID, openingJournalID string) (domain.YearEndRun, error)
	YearEndRun(ctx context.Context, runID string) (domain.YearEndRun, error)
}

func (a *API) WithYearEnd(s YearEndStore, l ledgerclient.Ledger) *API {
	a.yearEnds = s
	a.postings = l
	return a
}

type balanceRequest struct {
	Account    string          `json:"account"`
	Class      domain.Class    `json:"class"`
	Dimensions string          `json:"dimensions"`
	Amount     decimal.Decimal `json:"amount"`
}

type yearEndRequest struct {
	LedgerID         string           `json:"ledgerId"`
	FiscalYear       string           `json:"fiscalYear"`
	Currency         string           `json:"currency"`
	ClosingDate      string           `json:"closingDate"`
	OpeningDate      string           `json:"openingDate"`
	RetainedEarnings string           `json:"retainedEarningsAccount"`
	RetainDimensions bool             `json:"retainDimensions"`
	Balances         []balanceRequest `json:"balances"`
}

func (r yearEndRequest) validate(legalEntityID string) error {
	errs := map[string]string{}
	if !ids.Valid(legalEntityID) {
		errs["legalEntityId"] = "a single legal entity is required"
	}
	if !ids.Valid(r.LedgerID) {
		errs["ledgerId"] = "must be a UUID"
	}
	if !fiscalYearPattern.MatchString(r.FiscalYear) {
		errs["fiscalYear"] = "must be 1 to 20 letters, digits, dashes or underscores"
	}
	if !currencyPattern.MatchString(r.Currency) {
		errs["currency"] = "must be an ISO 4217 code"
	}
	for field, value := range map[string]string{"closingDate": r.ClosingDate, "openingDate": r.OpeningDate} {
		if _, err := time.Parse(dateLayout, value); err != nil {
			errs[field] = "must be an ISO date (YYYY-MM-DD)"
		}
	}
	if len(r.Balances) > maxBalances {
		errs["balances"] = "too many balances"
	}
	for _, b := range r.Balances {
		if b.Account == "" {
			errs["balances"] = "every balance needs an account"
			break
		}
	}
	if len(errs) == 0 {
		return nil
	}
	p := httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "request failed validation")
	p.Errors = errs
	return p
}

func (a *API) runYearEnd(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return
	}
	if a.yearEnds == nil || a.postings == nil {
		fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "year-end close is not configured"))
		return
	}
	var req yearEndRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	if err := req.validate(p.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	if !a.ledgerBelongsToCaller(w, r, p.LegalEntityID, req.LedgerID) {
		return
	}
	balances := make([]domain.Balance, len(req.Balances))
	for i, b := range req.Balances {
		balances[i] = domain.Balance{Account: b.Account, Class: b.Class, Dimensions: b.Dimensions, Amount: b.Amount}
	}
	proposals, err := domain.CloseYear(balances, req.RetainedEarnings, req.RetainDimensions)
	if err != nil {
		fail(w, r, err)
		return
	}
	canonical, err := json.Marshal(req)
	if err != nil {
		fail(w, r, err)
		return
	}
	run, created, err := a.yearEnds.BeginYearEnd(r.Context(), domain.YearEndRun{
		LegalEntityID: p.LegalEntityID, LedgerID: req.LedgerID, FiscalYear: req.FiscalYear, Currency: req.Currency,
		ClosingDate: req.ClosingDate, OpeningDate: req.OpeningDate, Proposals: proposals, CreatedBy: p.UserID,
		RequestHash: store.HashRequest(http.MethodPost, yearEndPath, canonical),
	})
	if err != nil {
		a.yearEndFailed(w, r, "year-end run could not be recorded", err)
		return
	}
	if run.Status != domain.RunCompleted {
		if run, err = a.postYearEnd(r.Context(), run); err != nil {
			a.yearEndFailed(w, r, "year-end posting failed", err)
			return
		}
		a.yearEndRuns.Add(1)
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", yearEndPath+"/"+run.ID)
	}
	httpx.JSON(w, status, run)
}

func (a *API) postYearEnd(ctx context.Context, run domain.YearEndRun) (domain.YearEndRun, error) {
	closing, err := a.postEntries(ctx, run, domain.EntryClosing, run.ClosingDate, run.Proposals.Closing)
	if err != nil {
		return run, err
	}
	opening, err := a.postEntries(ctx, run, domain.EntryOpening, run.OpeningDate, run.Proposals.Opening)
	if err != nil {
		return run, err
	}
	return a.yearEnds.CompleteYearEnd(ctx, run.ID, closing, opening)
}

func (a *API) postEntries(ctx context.Context, run domain.YearEndRun, kind, date string, entries []domain.Entry) (string, error) {
	if len(entries) == 0 {
		return "", nil
	}
	lines := make([]ledgerclient.Line, len(entries))
	for i, e := range entries {
		lines[i] = ledgerclient.Line{Account: e.Account, Credit: e.Amount.IsNegative(), Amount: e.Amount.Abs(), Text: e.Dimensions}
	}
	document := "year-end-" + kind + "-" + run.ID
	return a.postings.Post(ctx, ledgerclient.Entry{
		LegalEntityID: run.LegalEntityID, LedgerID: run.LedgerID, DocumentID: document, PostingDate: date, Currency: run.Currency,
		IdempotencyKey: document, SourceType: "year_end_" + kind, Lines: lines,
	})
}

func (a *API) yearEndFailed(w http.ResponseWriter, r *http.Request, message string, err error) {
	var de *domain.Error
	switch {
	case errors.As(err, &de):
	case ledgerclient.Rejected(err):
		slog.Warn(message, "err", err, "ledgerCode", ledgerclient.CodeOf(err), "correlationId", middleware.GetReqID(r.Context()))
		fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeLedgerRejected, "the ledger rejected the year-end entry: "+ledgerclient.CodeOf(err)))
		return
	default:
		a.failures.Add(1)
		slog.Error(message, "err", err, "correlationId", middleware.GetReqID(r.Context()))
		fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "year-end close is unavailable, retry the same request"))
		return
	}
	fail(w, r, err)
}

func (a *API) getYearEnd(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	if a.yearEnds == nil {
		fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "year-end close is not configured"))
		return
	}
	id := chi.URLParam(r, "runId")
	if !ids.Valid(id) {
		fail(w, r, &domain.Error{Code: domain.CodeNoRun, Detail: "year-end run not found"})
		return
	}
	run, err := a.yearEnds.YearEndRun(r.Context(), id)
	if err != nil {
		a.yearEndFailed(w, r, "year-end run read failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, run)
}
