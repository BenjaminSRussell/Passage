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
	if err == nil {
		err = a.checkRetainedEarnings(ctx, p.LegalEntityID, info, req.RetainedEarnings)
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
		if run, err = a.driveYearEnd(ctx, run, info); err != nil {
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

func (a *API) checkRetainedEarnings(ctx context.Context, legalEntityID string, info ledgerclient.Info, account string) error {
	accounts, err := a.yearLedger.Accounts(ctx, legalEntityID, info.ChartCode)
	if err != nil {
		return err
	}
	if re, ok := accounts[account]; !ok || re.Type != equityType || !re.Postable {
		return &domain.Error{Code: domain.CodeValidation, Detail: "retainedEarningsAccount must be a postable equity account of the ledger's chart"}
	}
	return nil
}

func (a *API) driveYearEnd(ctx context.Context, run domain.YearEndRun, info ledgerclient.Info) (domain.YearEndRun, error) {
	var err error
	if run.SupersedesRunID != "" {
		if run, err = a.reversePrior(ctx, run); err != nil {
			return run, err
		}
	}
	if run.Proposals == nil {
		proposals, err := a.deriveProposals(ctx, run, info)
		if err != nil {
			return run, err
		}
		if run, err = a.yearEnds.SetYearEndProposals(ctx, run.ID, proposals); err != nil {
			return run, err
		}
	}
	for _, step := range []struct {
		name, date string
		entries    []domain.Entry
	}{
		{domain.JournalClosing, run.ClosingDate, run.Proposals.Closing},
		{domain.JournalOpening, run.OpeningDate, run.Proposals.Opening},
	} {
		if run.Journals[step.name] != "" || len(step.entries) == 0 {
			continue
		}
		id, err := a.yearLedger.Post(ctx, a.yearEndEntry(run, step.name, step.date, step.entries))
		if err != nil {
			return run, err
		}
		if run, err = a.yearEnds.SetYearEndJournal(ctx, run.ID, step.name, id); err != nil {
			return run, err
		}
	}
	return a.yearEnds.CompleteYearEnd(ctx, run.ID)
}

func (a *API) reversePrior(ctx context.Context, run domain.YearEndRun) (domain.YearEndRun, error) {
	prior, err := a.yearEnds.YearEndRun(ctx, run.SupersedesRunID)
	if err != nil {
		return run, err
	}
	for _, step := range []struct{ posted, reversed, date string }{
		{domain.JournalClosing, domain.JournalReverseClosing, prior.ClosingDate},
		{domain.JournalOpening, domain.JournalReverseOpening, prior.OpeningDate},
	} {
		entry := prior.Journals[step.posted]
		if entry == "" || run.Journals[step.reversed] != "" {
			continue
		}
		key := documentID(run.ID, step.reversed)
		id, err := a.yearLedger.Reverse(ctx, ledgerclient.Reversal{
			LegalEntityID: run.LegalEntityID, EntryID: entry, PostingDate: step.date, IdempotencyKey: key,
			Description: "Year-end re-run " + run.FiscalYear + ": reversal of run " + prior.ID,
		})
		if err != nil {
			return run, err
		}
		if run, err = a.yearEnds.SetYearEndJournal(ctx, run.ID, step.reversed, id); err != nil {
			return run, err
		}
	}
	return run, nil
}

func (a *API) deriveProposals(ctx context.Context, run domain.YearEndRun, info ledgerclient.Info) (domain.YearEnd, error) {
	if err := domain.CheckDimensionAttributes(info.DimensionAttributes); err != nil {
		return domain.YearEnd{}, err
	}
	accounts, err := a.yearLedger.Accounts(ctx, run.LegalEntityID, info.ChartCode)
	if err != nil {
		return domain.YearEnd{}, err
	}
	closing, err := time.Parse(dateLayout, run.ClosingDate)
	if err != nil {
		return domain.YearEnd{}, err
	}
	query := ledgerclient.TrialBalanceQuery{LegalEntityID: run.LegalEntityID, LedgerID: run.LedgerID, PeriodTo: closing.Format(periodLayout), GroupBy: info.DimensionAttributes}
	cumulative, err := a.yearLedger.TrialBalance(ctx, query)
	if err != nil {
		return domain.YearEnd{}, err
	}
	query.PeriodFrom = run.FiscalYear + "-01"
	thisYear, err := a.yearLedger.TrialBalance(ctx, query)
	if err != nil {
		return domain.YearEnd{}, err
	}
	var balances []domain.Balance
	for want, rows := range map[domain.Class][]ledgerclient.BalanceRow{domain.ClassBalanceSheet: cumulative.Rows, domain.ClassProfitLoss: thisYear.Rows} {
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

func (a *API) yearEndEntry(run domain.YearEndRun, name, date string, entries []domain.Entry) ledgerclient.Entry {
	lines := make([]ledgerclient.Line, len(entries))
	for i, e := range entries {
		lines[i] = ledgerclient.Line{Account: e.Account, Credit: e.Amount.IsNegative(), Amount: e.Amount.Abs(), Dimensions: domain.ParseDimensions(e.Dimensions)}
	}
	document := documentID(run.ID, name)
	return ledgerclient.Entry{
		LegalEntityID: run.LegalEntityID, LedgerID: run.LedgerID, DocumentID: document, PostingDate: date, Currency: run.Currency,
		IdempotencyKey: document, SourceType: "year_end_" + name, Lines: lines,
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
	case errors.As(err, &le) && (ledgerclient.Rejected(err) || le.Status == http.StatusConflict):
		slog.Warn(message, "err", err, "ledgerCode", le.Code, "correlationId", correlationID)
		fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeLedgerRejected, "the ledger refused the request: "+le.Code))
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
