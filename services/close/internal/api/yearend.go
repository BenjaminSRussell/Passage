package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

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
	yearEndPath     = "/v1/close/year-end-runs"
	periodLayout    = "2006-01"
	equityType      = "equity"
)

var fiscalYearPattern = regexp.MustCompile(`^[0-9]{4}$`)

type YearEndStore interface {
	YearPeriods(ctx context.Context, legalEntityID, ledgerID, year string) ([]domain.Period, error)
	BeginYearEnd(ctx context.Context, in domain.YearEndRun) (domain.YearEndRun, bool, error)
	SetYearEndProposals(ctx context.Context, runID string, proposals domain.YearEnd) (domain.YearEndRun, error)
	SetYearEndJournal(ctx context.Context, runID, name, journalID string) (domain.YearEndRun, error)
	AbandonYearEnd(ctx context.Context, runID string) (domain.YearEndRun, error)
	CompleteYearEnd(ctx context.Context, runID string) (domain.YearEndRun, error)
	YearEndRun(ctx context.Context, runID string) (domain.YearEndRun, error)
}

type YearEndLedger interface {
	ledgerclient.Ledger
	ledgerclient.YearEndSource
}

func (a *API) WithYearEnd(s YearEndStore, l YearEndLedger) *API {
	a.yearEnds = s
	a.yearLedger = l
	return a
}

type yearEndRequest struct {
	LedgerID         string `json:"ledgerId"`
	FiscalYear       string `json:"fiscalYear"`
	RetainedEarnings string `json:"retainedEarningsAccount"`
	RetainDimensions bool   `json:"retainDimensions"`
	SupersedesRunID  string `json:"supersedesRunId"`
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
		errs["fiscalYear"] = "must be a four digit year"
	}
	if r.RetainedEarnings == "" || len(r.RetainedEarnings) > 20 {
		errs["retainedEarningsAccount"] = "is required, at most 20 characters"
	}
	if r.SupersedesRunID != "" && !ids.Valid(r.SupersedesRunID) {
		errs["supersedesRunId"] = "must be a UUID"
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
	if a.yearEnds == nil || a.yearLedger == nil {
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
	ctx := r.Context()
	info, err := a.yearLedger.Ledger(ctx, p.LegalEntityID, req.LedgerID)
	if err == nil {
		err = sameEntity(info.LegalEntityID, p.LegalEntityID)
	}
	var accounts map[string]ledgerclient.ChartAccount
	if err == nil {
		accounts, err = a.yearLedger.Accounts(ctx, p.LegalEntityID, info.ChartCode)
	}
	if err == nil {
		err = checkRetainedEarnings(accounts, req.RetainedEarnings)
	}
	var plan domain.YearPlan
	if err == nil {
		var periods []domain.Period
		if periods, err = a.yearEnds.YearPeriods(ctx, p.LegalEntityID, req.LedgerID, req.FiscalYear); err == nil {
			plan, err = domain.PlanYear(req.FiscalYear, periods)
		}
	}
	if err != nil {
		a.yearEndFailed(w, r, "year-end preconditions failed", err)
		return
	}
	run, created, err := a.yearEnds.BeginYearEnd(ctx, domain.YearEndRun{
		LegalEntityID: p.LegalEntityID, LedgerID: req.LedgerID, FiscalYear: req.FiscalYear, SupersedesRunID: req.SupersedesRunID,
		Currency: info.Currency, ClosingDate: plan.ClosingDate.Format(dateLayout), OpeningDate: plan.OpeningDate.Format(dateLayout),
		RetainedEarnings: req.RetainedEarnings, RetainDimensions: req.RetainDimensions, CreatedBy: p.UserID,
		RequestHash: store.HashRequest(http.MethodPost, yearEndPath, []byte(fmt.Sprint(req.LedgerID, req.FiscalYear, req.RetainedEarnings, req.RetainDimensions))),
	})
	if err != nil {
		a.yearEndFailed(w, r, "year-end run could not be recorded", err)
		return
	}
	if run.Status != domain.RunCompleted {
		if run, err = a.driveYearEnd(ctx, run, info, accounts); err != nil {
			a.abandonUnrecoverable(ctx, run, err)
			a.yearEndFailed(w, r, "year-end run failed", err)
			return
		}
		a.yearEndRuns.Add(1)
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	w.Header().Set("Location", yearEndPath+"/"+run.ID)
	httpx.JSON(w, status, run)
}

func sameEntity(owner, caller string) error {
	want, _ := ids.Canonical(caller)
	if got, _ := ids.Canonical(owner); got != want {
		return ledgerclient.ErrNotVisible
	}
	return nil
}

func checkRetainedEarnings(accounts map[string]ledgerclient.ChartAccount, account string) error {
	if re, ok := accounts[account]; !ok || re.Type != equityType || !re.Postable {
		return &domain.Error{Code: domain.CodeValidation, Detail: "retainedEarningsAccount must be a postable equity account of the ledger's chart"}
	}
	return nil
}

func (a *API) driveYearEnd(ctx context.Context, run domain.YearEndRun, info ledgerclient.Info, accounts map[string]ledgerclient.ChartAccount) (domain.YearEndRun, error) {
	if run.SupersedesRunID != "" {
		reversed, err := a.reversePrior(ctx, run)
		if err != nil {
			return run, err
		}
		run = reversed
	}
	if run.Journals[domain.JournalClosing] == "" {
		proposals, err := a.deriveProposals(ctx, run, info, accounts)
		if err != nil {
			return run, err
		}
		stored, err := a.yearEnds.SetYearEndProposals(ctx, run.ID, proposals)
		if err != nil {
			return run, err
		}
		run = stored
		if len(run.Proposals.Closing) > 0 {
			id, err := a.yearLedger.Post(ctx, yearEndEntry(run, run.Proposals.Closing))
			if err != nil {
				return run, err
			}
			posted, err := a.yearEnds.SetYearEndJournal(ctx, run.ID, domain.JournalClosing, id)
			if err != nil {
				return run, err
			}
			run = posted
		}
	}
	return a.yearEnds.CompleteYearEnd(ctx, run.ID)
}

func (a *API) abandonUnrecoverable(ctx context.Context, run domain.YearEndRun, err error) {
	if run.ID == "" || len(run.Journals) > 0 || !unrecoverable(err) {
		return
	}
	if _, abandonErr := a.yearEnds.AbandonYearEnd(ctx, run.ID); abandonErr != nil {
		slog.ErrorContext(ctx, "year-end run could not be abandoned", "err", abandonErr, "runId", run.ID, "correlationId", middleware.GetReqID(ctx))
	}
}

func unrecoverable(err error) bool {
	var de *domain.Error
	var le *ledgerclient.Error
	switch {
	case errors.As(err, &de):
		return true
	case ledgerclient.InProgress(err):
		return false
	case errors.As(err, &le):
		return ledgerclient.Rejected(err) || le.Status == http.StatusConflict
	}
	return false
}

func (a *API) reversePrior(ctx context.Context, run domain.YearEndRun) (domain.YearEndRun, error) {
	prior, err := a.yearEnds.YearEndRun(ctx, run.SupersedesRunID)
	if err != nil {
		return run, err
	}
	entry := prior.Journals[domain.JournalClosing]
	if entry == "" || run.Journals[domain.JournalReverseClosing] != "" {
		return run, nil
	}
	id, err := a.yearLedger.Reverse(ctx, ledgerclient.Reversal{
		LegalEntityID: run.LegalEntityID, EntryID: entry, PostingDate: prior.ClosingDate, PostClosed: true,
		IdempotencyKey: documentID(run.ID, domain.JournalReverseClosing),
		Description:    "Year-end re-run " + run.FiscalYear + ": reversal of run " + prior.ID,
	})
	if err != nil {
		return run, err
	}
	reversed, err := a.yearEnds.SetYearEndJournal(ctx, run.ID, domain.JournalReverseClosing, id)
	if err != nil {
		return run, err
	}
	return reversed, nil
}

func (a *API) trialBalance(ctx context.Context, info ledgerclient.Info, q ledgerclient.TrialBalanceQuery) ([]ledgerclient.BalanceRow, error) {
	tb, err := a.yearLedger.TrialBalance(ctx, q)
	if err != nil {
		return nil, err
	}
	if tb.Currency != info.Currency {
		return nil, &domain.Error{Code: domain.CodeLedgerRejected, Detail: "the ledger's trial balance is in " + tb.Currency + " but the ledger's currency is " + info.Currency}
	}
	return tb.Rows, nil
}

func (a *API) deriveProposals(ctx context.Context, run domain.YearEndRun, info ledgerclient.Info, accounts map[string]ledgerclient.ChartAccount) (domain.YearEnd, error) {
	if err := domain.CheckDimensionAttributes(info.DimensionAttributes); err != nil {
		return domain.YearEnd{}, err
	}
	closing, err := time.Parse(dateLayout, run.ClosingDate)
	if err != nil {
		return domain.YearEnd{}, err
	}
	query := ledgerclient.TrialBalanceQuery{LegalEntityID: run.LegalEntityID, LedgerID: run.LedgerID, PeriodTo: closing.Format(periodLayout), GroupBy: info.DimensionAttributes}
	cumulative, err := a.trialBalance(ctx, info, query)
	if err != nil {
		return domain.YearEnd{}, err
	}
	query.PeriodFrom = run.FiscalYear + "-01"
	thisYear, err := a.trialBalance(ctx, info, query)
	if err != nil {
		return domain.YearEnd{}, err
	}
	var balances []domain.Balance
	for want, rows := range map[domain.Class][]ledgerclient.BalanceRow{domain.ClassBalanceSheet: cumulative, domain.ClassProfitLoss: thisYear} {
		for _, row := range rows {
			account, ok := accounts[row.Account]
			if !ok {
				return domain.YearEnd{}, &domain.Error{Code: domain.CodeLedgerData, Detail: "account " + row.Account + " is not in the ledger's chart"}
			}
			class, err := domain.ClassOf(account.Type)
			if err != nil {
				if row.Net.IsZero() {
					continue
				}
				return domain.YearEnd{}, err
			}
			if class == want && !row.Net.IsZero() {
				balances = append(balances, domain.Balance{Account: row.Account, Class: class, Dimensions: domain.DimensionKey(row.Dimensions), Amount: row.Net})
			}
		}
	}
	proposals, err := domain.CloseYear(balances, run.RetainedEarnings, run.RetainDimensions)
	var de *domain.Error
	if errors.As(err, &de) && de.Code == domain.CodeValidation {
		return domain.YearEnd{}, &domain.Error{Code: domain.CodeLedgerData, Detail: de.Detail}
	}
	return proposals, err
}

func documentID(runID, name string) string {
	return uuid.NewSHA1(uuid.MustParse(runID), []byte(name)).String()
}

func yearEndEntry(run domain.YearEndRun, entries []domain.Entry) ledgerclient.Entry {
	lines := make([]ledgerclient.Line, len(entries))
	for i, e := range entries {
		lines[i] = ledgerclient.Line{Account: e.Account, Credit: e.Amount.IsNegative(), Amount: e.Amount.Abs(), Dimensions: domain.ParseDimensions(e.Dimensions)}
	}
	document := documentID(run.ID, domain.JournalClosing)
	return ledgerclient.Entry{
		LegalEntityID: run.LegalEntityID, LedgerID: run.LedgerID, DocumentID: document, PostingDate: run.ClosingDate, Currency: run.Currency,
		IdempotencyKey: document, SourceType: "year_end_" + domain.JournalClosing, PostClosed: true, Lines: lines,
	}
}

func (a *API) yearEndFailed(w http.ResponseWriter, r *http.Request, message string, err error) {
	correlationID := middleware.GetReqID(r.Context())
	var de *domain.Error
	var le *ledgerclient.Error
	switch {
	case errors.As(err, &de):
	case errors.Is(err, ledgerclient.ErrNotVisible):
		fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the ledger does not belong to the caller's legal entity"))
		return
	case ledgerclient.InProgress(err):
		w.Header().Set("Retry-After", "1")
		fail(w, r, httpx.NewProblem(http.StatusConflict, domain.CodeBusy, "the ledger is still processing the same posting, retry the same request"))
		return
	case errors.As(err, &le) && unrecoverable(err):
		slog.Warn(message, "err", err, "ledgerCode", le.Code, "ledgerStatus", le.Status, "correlationId", correlationID)
		fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeLedgerRejected, fmt.Sprintf("the ledger refused the request (status %d): %s", le.Status, le.Code)))
		return
	default:
		a.failures.Add(1)
		slog.Error(message, "err", err, "correlationId", correlationID)
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
