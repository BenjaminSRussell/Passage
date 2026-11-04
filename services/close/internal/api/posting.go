package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
	"github.com/BenjaminSRussell/passage/services/close/internal/store"
)

func (a *API) putAllocRule(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	if a.allocs == nil {
		fail(w, r, missingSlice("allocations"))
		return
	}
	id := chi.URLParam(r, "ruleId")
	if !ids.Valid(id) {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "ruleId must be a UUID"))
		return
	}
	var rule domain.AllocationRule
	if err := httpx.Decode(r, &rule); err != nil {
		fail(w, r, err)
		return
	}
	rule.ID, rule.LegalEntityID = id, p.LegalEntityID
	if rule.Method == "" {
		rule.Method = "fixed_weight"
	}
	if !ids.Valid(p.LegalEntityID) || !ids.Valid(rule.LedgerID) || rule.Name == "" || len(rule.Name) > 60 || rule.OffsetAccount == "" {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "ledgerId, name (up to 60 characters) and offsetAccount are required"))
		return
	}
	if err := validateAllocationRule(rule); err != nil {
		fail(w, r, err)
		return
	}
	if !a.ledgerBelongsToCaller(w, r, p.LegalEntityID, rule.LedgerID) {
		return
	}
	out, err := a.allocs.PutAllocationRule(r.Context(), rule)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func validateAllocationRule(rule domain.AllocationRule) error {
	if len(rule.SourceAccounts) == 0 || len(rule.Destinations) == 0 {
		return &domain.Error{Code: domain.CodeValidation, Detail: "sourceAccounts and at least one destination are required"}
	}
	seen := map[string]bool{}
	for _, d := range rule.Destinations {
		if d.Key == "" || d.Account == "" || seen[d.Key] {
			return &domain.Error{Code: domain.CodeValidation, Detail: "destinations need a unique key and an account"}
		}
		seen[d.Key] = true
	}
	return nil
}

func notFound(detail string) error { return &domain.Error{Code: domain.CodeNoRun, Detail: detail} }

func unprocessable(detail string) error {
	return &domain.Error{Code: domain.CodeUnprocessable, Detail: detail}
}

func hashKey(parts ...string) []byte { return []byte(strings.Join(parts, "|")) }

func (a *API) getAllocRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	if a.allocs == nil {
		fail(w, r, missingSlice("allocations"))
		return
	}
	id := chi.URLParam(r, "ruleId")
	if !ids.Valid(id) {
		fail(w, r, notFound("allocation rule not found"))
		return
	}
	out, err := a.allocs.AllocationRule(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type allocationRunRequest struct {
	LedgerID       string `json:"ledgerId"`
	RuleID         string `json:"ruleId"`
	FiscalPeriodID string `json:"fiscalPeriodId"`
	PostingDate    string `json:"postingDate"`
	Mode           string `json:"mode"`
}

func (a *API) runAllocation(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	if a.allocs == nil || a.yearLedger == nil {
		fail(w, r, missingSlice("allocations"))
		return
	}
	if err := requireKey(r.Header.Get(idempotencyHeader)); err != nil {
		fail(w, r, err)
		return
	}
	var req allocationRunRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	if req.Mode == "" {
		req.Mode = "simulate"
	}
	if req.Mode != "simulate" && req.Mode != "post" {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "mode must be simulate or post"))
		return
	}
	if !ids.Valid(p.LegalEntityID) || !ids.Valid(req.LedgerID) || !ids.Valid(req.RuleID) || !ids.Valid(req.FiscalPeriodID) {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "ledgerId, ruleId and fiscalPeriodId must be UUIDs"))
		return
	}
	if _, err := time.Parse(dateLayout, req.PostingDate); err != nil {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "postingDate must be an ISO date"))
		return
	}
	if !a.ledgerBelongsToCaller(w, r, p.LegalEntityID, req.LedgerID) {
		return
	}
	rule, err := a.allocs.AllocationRule(r.Context(), req.RuleID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if rule.LedgerID != req.LedgerID {
		fail(w, r, unprocessable("the allocation rule belongs to a different ledger"))
		return
	}
	span, err := a.periodSpan(r.Context(), p.LegalEntityID, req.LedgerID, req.PostingDate)
	if err != nil {
		fail(w, r, err)
		return
	}
	want, _ := ids.Canonical(req.FiscalPeriodID)
	if got, _ := ids.Canonical(span.FiscalPeriodID); got != want {
		fail(w, r, unprocessable("fiscalPeriodId does not cover postingDate"))
		return
	}
	info, err := a.yearLedger.Ledger(r.Context(), p.LegalEntityID, req.LedgerID)
	if err != nil {
		a.yearEndFailed(w, r, "allocation ledger lookup failed", err)
		return
	}
	amount, err := a.sourceAmount(r.Context(), info, rule, req.PostingDate)
	if err != nil {
		a.yearEndFailed(w, r, "allocation source read failed", err)
		return
	}
	dests := append([]domain.AllocationDest(nil), rule.Destinations...)
	if rule.Method == "equally" {
		for i := range dests {
			dests[i].Weight = decimal.NewFromInt(1)
		}
	}
	scale := money.MinorUnits(info.Currency)
	proposal, err := domain.ProposeAllocation(amount, dests, rule.OffsetAccount, rule.OffsetDimensions, scale)
	if err != nil {
		fail(w, r, err)
		return
	}
	run := domain.AllocationRun{
		LegalEntityID: p.LegalEntityID, LedgerID: req.LedgerID, FiscalPeriodID: req.FiscalPeriodID, RuleID: req.RuleID,
		PostingDate: req.PostingDate, Currency: info.Currency, SourceAmount: amount.String(), Mode: req.Mode, Status: "proposed",
		Proposal: proposal, CreatedBy: p.UserID,
	}
	hash := store.HashRequest(http.MethodPost, "/v1/close/allocation-runs", hashKey(req.LedgerID, info.Currency, req.RuleID, req.FiscalPeriodID, req.PostingDate, req.Mode))
	saved, err := a.allocs.SaveAllocationRun(r.Context(), run, r.Header.Get(idempotencyHeader), hash)
	if err != nil {
		fail(w, r, err)
		return
	}
	if req.Mode == "post" && saved.Status != "posted" {
		saved, err = a.dispatchAllocation(r, saved)
		if err != nil {
			a.yearEndFailed(w, r, "allocation post failed", err)
			return
		}
	}
	httpx.JSON(w, http.StatusCreated, saved)
}

func (a *API) sourceAmount(ctx context.Context, info ledgerclient.Info, rule domain.AllocationRule, postingDate string) (decimal.Decimal, error) {
	period := postingDate[:len("2006-01")]
	tb, err := a.yearLedger.TrialBalance(ctx, ledgerclient.TrialBalanceQuery{
		LegalEntityID: rule.LegalEntityID, LedgerID: rule.LedgerID, PeriodTo: period, GroupBy: info.DimensionAttributes,
	})
	if err != nil {
		return decimal.Zero, err
	}
	if tb.Currency != "" && tb.Currency != info.Currency {
		return decimal.Zero, unprocessable("the trial balance currency differs from the ledger currency")
	}
	if len(rule.SourceAccounts) == 0 {
		return decimal.Zero, unprocessable("the allocation rule names no source accounts")
	}
	want := map[string]bool{}
	for _, acct := range rule.SourceAccounts {
		want[acct] = true
	}
	total := decimal.Zero
	for _, row := range tb.Rows {
		if want[row.Account] {
			total = total.Add(row.Net)
		}
	}
	return total, nil
}

func (a *API) dispatchAllocation(r *http.Request, run domain.AllocationRun) (domain.AllocationRun, error) {
	if err := a.requireOpenPeriod(r.Context(), run.LegalEntityID, run.LedgerID, run.PostingDate); err != nil {
		return run, err
	}
	id, err := a.yearLedger.Post(r.Context(), allocationEntry(run))
	if err != nil {
		return run, err
	}
	return a.allocs.MarkAllocationPosted(r.Context(), run.ID, id)
}

func allocationEntry(run domain.AllocationRun) ledgerclient.Entry {
	lines := make([]ledgerclient.Line, 0, len(run.Proposal.Destinations)+1)
	for _, d := range run.Proposal.Destinations {
		if d.Amount.IsZero() {
			continue
		}
		lines = append(lines, ledgerclient.Line{Account: d.Account, Credit: d.Amount.IsNegative(), Amount: d.Amount.Abs(), Dimensions: domain.ParseDimensions(d.Dimensions)})
	}
	off := run.Proposal.Offset
	lines = append(lines, ledgerclient.Line{Account: off.Account, Credit: !off.Amount.IsNegative(), Amount: off.Amount.Abs(), Dimensions: domain.ParseDimensions(off.Dimensions)})
	doc := uuid.NewSHA1(uuid.MustParse(run.ID), []byte("allocation")).String()
	return ledgerclient.Entry{
		LegalEntityID: run.LegalEntityID, LedgerID: run.LedgerID, DocumentID: doc, PostingDate: run.PostingDate, Currency: run.Currency,
		IdempotencyKey: doc, SourceType: "allocation", PostClosed: false, Lines: lines,
	}
}

func (a *API) postAllocation(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	if a.allocs == nil || a.yearLedger == nil {
		fail(w, r, missingSlice("allocations"))
		return
	}
	runID := chi.URLParam(r, "runId")
	if !ids.Valid(runID) {
		fail(w, r, notFound("allocation run not found"))
		return
	}
	run, err := a.allocs.AllocationRun(r.Context(), runID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if run.Status == "posted" {
		httpx.JSON(w, http.StatusOK, run)
		return
	}
	saved, err := a.dispatchAllocation(r, run)
	if err != nil {
		a.yearEndFailed(w, r, "allocation post failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, saved)
}

func (a *API) reverseAllocation(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	if a.allocs == nil || a.yearLedger == nil {
		fail(w, r, missingSlice("allocations"))
		return
	}
	runID := chi.URLParam(r, "runId")
	if !ids.Valid(runID) {
		fail(w, r, notFound("allocation run not found"))
		return
	}
	run, err := a.allocs.AllocationRun(r.Context(), runID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if run.Status != "posted" || run.EntryID == "" {
		fail(w, r, &domain.Error{Code: domain.CodeInvalidTransition, Detail: "only a posted allocation run can be reversed"})
		return
	}
	if run.ReversalEntryID != "" {
		httpx.JSON(w, http.StatusOK, run)
		return
	}
	key := uuid.NewSHA1(uuid.MustParse(run.ID), []byte("allocation-reverse")).String()
	id, err := a.yearLedger.Reverse(r.Context(), ledgerclient.Reversal{
		LegalEntityID: run.LegalEntityID, EntryID: run.EntryID, PostingDate: run.PostingDate, IdempotencyKey: key,
		Description: "Allocation reversal " + run.ID,
	})
	if err != nil {
		a.yearEndFailed(w, r, "allocation reverse failed", err)
		return
	}
	saved, err := a.allocs.MarkAllocationReversed(r.Context(), run.ID, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, saved)
}

func (a *API) putAccrual(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	if a.accruals == nil {
		fail(w, r, missingSlice("accruals"))
		return
	}
	var scheme domain.AccrualDefinition
	if err := httpx.Decode(r, &scheme); err != nil {
		fail(w, r, err)
		return
	}
	scheme.Code, scheme.LegalEntityID, scheme.UpdatedBy = chi.URLParam(r, "code"), p.LegalEntityID, p.UserID
	if scheme.Frequency == "" {
		scheme.Frequency = "monthly"
	}
	if scheme.Spread == "" {
		scheme.Spread = domain.SpreadEqual
	}
	if scheme.Occurrences == 0 {
		scheme.Occurrences = 12
	}
	if scheme.Code == "" || len(scheme.Code) > 20 || !ids.Valid(scheme.LedgerID) || scheme.DebitAccount == "" || scheme.CreditAccount == "" {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "code (up to 20 characters), ledgerId, debitAccount and creditAccount are required"))
		return
	}
	if amount, err := decimal.NewFromString(scheme.Amount); err != nil || !amount.IsPositive() {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "amount must be a positive decimal"))
		return
	}
	if err := validateAccrualScheme(scheme); err != nil {
		fail(w, r, err)
		return
	}
	if !a.ledgerBelongsToCaller(w, r, p.LegalEntityID, scheme.LedgerID) {
		return
	}
	if a.yearLedger != nil {
		info, err := a.yearLedger.Ledger(r.Context(), p.LegalEntityID, scheme.LedgerID)
		if err != nil {
			a.yearEndFailed(w, r, "accrual ledger lookup failed", err)
			return
		}
		amount, _ := decimal.NewFromString(scheme.Amount)
		if !amount.Equal(amount.Round(money.MinorUnits(info.Currency))) {
			fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "amount has more decimals than the ledger currency "+info.Currency+" allows"))
			return
		}
	}
	out, err := a.accruals.PutAccrualScheme(r.Context(), scheme)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func validateAccrualScheme(scheme domain.AccrualDefinition) error {
	switch {
	case scheme.Occurrences < 1 || scheme.Occurrences > 120:
		return &domain.Error{Code: domain.CodeValidation, Detail: "occurrences must be between 1 and 120"}
	case scheme.Frequency != "monthly" && scheme.Frequency != "quarterly":
		return &domain.Error{Code: domain.CodeValidation, Detail: "frequency must be monthly or quarterly"}
	case scheme.Spread != domain.SpreadEqual && scheme.Spread != domain.SpreadByDays && scheme.Spread != domain.SpreadCustom:
		return &domain.Error{Code: domain.CodeValidation, Detail: "spread must be equal, by_days or custom"}
	}
	return nil
}

func (a *API) getAccrual(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	if a.accruals == nil {
		fail(w, r, missingSlice("accruals"))
		return
	}
	ledgerID := r.URL.Query().Get("ledgerId")
	if !ids.Valid(ledgerID) {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "ledgerId must be a UUID"))
		return
	}
	out, err := a.accruals.AccrualScheme(r.Context(), ledgerID, chi.URLParam(r, "code"))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type applyAccrualRequest struct {
	LedgerID  string `json:"ledgerId"`
	Currency  string `json:"currency"`
	StartDate string `json:"startDate"`
}

func (a *API) applyAccrual(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	if a.accruals == nil || a.yearLedger == nil {
		fail(w, r, missingSlice("accruals"))
		return
	}
	if err := requireKey(r.Header.Get(idempotencyHeader)); err != nil {
		fail(w, r, err)
		return
	}
	var req applyAccrualRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	start, err := time.Parse(dateLayout, req.StartDate)
	if err != nil || !ids.Valid(req.LedgerID) {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "ledgerId and startDate are required"))
		return
	}
	if !a.ledgerBelongsToCaller(w, r, p.LegalEntityID, req.LedgerID) {
		return
	}
	scheme, err := a.accruals.AccrualScheme(r.Context(), req.LedgerID, chi.URLParam(r, "code"))
	if err != nil {
		fail(w, r, err)
		return
	}
	info, err := a.yearLedger.Ledger(r.Context(), p.LegalEntityID, req.LedgerID)
	if err != nil {
		a.yearEndFailed(w, r, "accrual ledger lookup failed", err)
		return
	}
	hash := store.HashRequest(http.MethodPost, r.URL.Path, hashKey(req.LedgerID, info.Currency, req.StartDate))
	if stored, replayed, err := a.accruals.AccrualRunByKey(r.Context(), r.Header.Get(idempotencyHeader), hash); err != nil {
		fail(w, r, err)
		return
	} else if replayed {
		httpx.JSON(w, http.StatusOK, stored)
		return
	}
	if req.Currency != "" && req.Currency != info.Currency {
		fail(w, r, unprocessable("currency must be the ledger currency "+info.Currency))
		return
	}
	if scheme.UpdatedBy != "" && scheme.UpdatedBy == p.UserID {
		fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the user who last changed the accrual scheme cannot apply it"))
		return
	}
	amount, err := decimal.NewFromString(scheme.Amount)
	if err != nil {
		fail(w, r, unprocessable("the accrual scheme holds no valid amount"))
		return
	}
	stepMonths := 1
	if scheme.Frequency == "quarterly" {
		stepMonths = 3
	}
	custom := make([]decimal.Decimal, len(scheme.Custom))
	for i, c := range scheme.Custom {
		custom[i], _ = decimal.NewFromString(c)
	}
	occ, err := domain.SpreadAccrual(amount, start, domain.AccrualScheme{Occurrences: scheme.Occurrences, StepMonths: stepMonths, Spread: scheme.Spread, Custom: custom, AutoReverse: scheme.AutoReverse}, money.MinorUnits(info.Currency))
	if err != nil {
		fail(w, r, err)
		return
	}
	today := a.today()
	run := domain.AccrualRun{
		LegalEntityID: p.LegalEntityID, LedgerID: req.LedgerID, SchemeCode: scheme.Code, Amount: amount.String(), Currency: info.Currency,
		DebitAccount: scheme.DebitAccount, CreditAccount: scheme.CreditAccount, AutoReverse: scheme.AutoReverse,
		StartDate: req.StartDate, Status: "scheduled", Occurrences: occ, EntryIDs: map[string]string{}, CreatedBy: p.UserID, NextDue: &today,
	}
	for _, o := range occ {
		if o.Date.After(today) {
			break
		}
		if err := a.requireOpenPeriod(r.Context(), p.LegalEntityID, req.LedgerID, o.Date.Format(dateLayout)); err != nil {
			a.yearEndFailed(w, r, "accrual period check failed", err)
			return
		}
		break
	}
	saved, err := a.accruals.SaveAccrualRun(r.Context(), run, r.Header.Get(idempotencyHeader), hash)
	if err != nil {
		fail(w, r, err)
		return
	}
	if saved.Status == "posted" {
		httpx.JSON(w, http.StatusOK, saved)
		return
	}
	saved, err = a.advanceAccrual(r.Context(), saved, today)
	if err != nil {
		a.yearEndFailed(w, r, "accrual post failed", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, saved)
}

func (a *API) advanceAccrual(ctx context.Context, run domain.AccrualRun, today time.Time) (domain.AccrualRun, error) {
	if run.EntryIDs == nil {
		run.EntryIDs = map[string]string{}
	}
	persist := func() error {
		saved, err := a.accruals.SaveAccrualRun(ctx, run, "", "")
		if err != nil {
			return err
		}
		run = saved
		return nil
	}
	for _, o := range run.Occurrences {
		date, reverseOn := o.Date.Format(dateLayout), o.ReverseOn.Format(dateLayout)
		occKey, revKey := "occ-"+date, "rev-"+reverseOn
		if run.EntryIDs[occKey] == "" && !o.Date.After(today) && run.Status != "cancelled" {
			if err := a.requireOpenPeriod(ctx, run.LegalEntityID, run.LedgerID, date); err != nil {
				return run, err
			}
			doc := uuid.NewSHA1(uuid.MustParse(run.ID), []byte("accrual-"+date)).String()
			id, err := a.yearLedger.Post(ctx, ledgerclient.Entry{
				LegalEntityID: run.LegalEntityID, LedgerID: run.LedgerID, PostingDate: date, Currency: run.Currency,
				DocumentID: doc, IdempotencyKey: doc, SourceType: "accrual", PostClosed: false, Lines: []ledgerclient.Line{
					{Account: run.DebitAccount, Amount: o.Amount},
					{Account: run.CreditAccount, Credit: true, Amount: o.Amount},
				},
			})
			if err != nil {
				return run, err
			}
			run.EntryIDs[occKey] = id
			if err := persist(); err != nil {
				return run, err
			}
		}
		if !run.AutoReverse || run.EntryIDs[occKey] == "" || run.EntryIDs[revKey] != "" || o.ReverseOn.After(today) {
			continue
		}
		if err := a.requireOpenPeriod(ctx, run.LegalEntityID, run.LedgerID, reverseOn); err != nil {
			return run, err
		}
		rev, err := a.yearLedger.Reverse(ctx, ledgerclient.Reversal{
			LegalEntityID: run.LegalEntityID, EntryID: run.EntryIDs[occKey], PostingDate: reverseOn,
			IdempotencyKey: uuid.NewSHA1(uuid.MustParse(run.ID), []byte("accrual-rev-"+date)).String(),
			Description:    "Accrual reversal", PostClosed: false,
		})
		if err != nil {
			return run, err
		}
		run.EntryIDs[revKey] = rev
		if err := persist(); err != nil {
			return run, err
		}
	}
	var next *time.Time
	consider := func(day time.Time) {
		if next == nil || day.Before(*next) {
			d := day
			next = &d
		}
	}
	cancelled := run.Status == "cancelled"
	for _, o := range run.Occurrences {
		posted := run.EntryIDs["occ-"+o.Date.Format(dateLayout)] != ""
		if !posted && !cancelled {
			consider(o.Date)
		}
		if run.AutoReverse && (posted || !cancelled) && run.EntryIDs["rev-"+o.ReverseOn.Format(dateLayout)] == "" {
			consider(o.ReverseOn)
		}
	}
	run.NextDue = next
	if cancelled {
		run.ReversalsPending = next != nil
	} else {
		run.Status = "scheduled"
		if next == nil {
			run.Status = "posted"
		}
	}
	return run, persist()
}

func permanentPeriodRefusal(err error) bool {
	var de *domain.Error
	return errors.As(err, &de) && (de.Code == domain.CodePeriodClosed || de.Code == domain.CodeNoPeriod)
}

func (a *API) cancelAccrual(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principalWithUser(w, r)
	if !ok {
		return
	}
	if a.accruals == nil {
		fail(w, r, missingSlice("accruals"))
		return
	}
	id := chi.URLParam(r, "runId")
	if !ids.Valid(id) {
		fail(w, r, notFound("accrual run not found"))
		return
	}
	out, err := a.accruals.CancelAccrualRun(r.Context(), id, p.UserID)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) PostDueAccruals(ctx context.Context) (int, error) {
	if a.accruals == nil || a.yearLedger == nil {
		return 0, nil
	}
	runs, listErr := a.accruals.DueAccrualRuns(ctx)
	completed := 0
	for _, run := range runs {
		runCtx := tenant.With(ctx, tenant.Principal{TenantID: run.TenantID, LegalEntityID: run.LegalEntityID, UserID: run.CreatedBy})
		if err := a.accruals.RenewAccrualLease(runCtx, run.ID); err != nil {
			slog.Error("accrual lease could not be renewed", "runId", run.ID, "err", err)
			continue
		}
		updated, err := a.advanceAccrual(runCtx, run, a.today())
		if err != nil {
			park := ledgerclient.Rejected(err) || permanentPeriodRefusal(err)
			slog.Error("accrual run could not post", "runId", run.ID, "tenantId", run.TenantID, "parked", park, "err", err)
			if recordErr := a.accruals.RecordAccrualFailure(runCtx, run.ID, err.Error(), park); recordErr != nil {
				slog.Error("accrual failure could not be recorded", "runId", run.ID, "err", recordErr)
			}
			continue
		}
		if err := a.accruals.ClearAccrualFailure(runCtx, run.ID); err != nil {
			slog.Error("accrual failure counter could not be cleared", "runId", run.ID, "err", err)
		}
		if updated.Status == "posted" {
			completed++
		}
	}
	return completed, listErr
}

func (a *API) putRegroup(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	if a.regroups == nil {
		fail(w, r, missingSlice("regrouping"))
		return
	}
	var rule domain.StoredRegroupingRule
	if err := httpx.Decode(r, &rule); err != nil {
		fail(w, r, err)
		return
	}
	rule.Code, rule.LegalEntityID = chi.URLParam(r, "code"), p.LegalEntityID
	if rule.Code == "" || len(rule.Code) > 20 || !ids.Valid(rule.LedgerID) {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "code (up to 20 characters) and ledgerId are required"))
		return
	}
	switch rule.SortMethod {
	case domain.SortRemainingTerm, domain.SortDebitCredit, domain.SortAccountSign:
	default:
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "sortMethod must be remaining_term, debit_credit_balance or account_balance_sign"))
		return
	}
	if !a.ledgerBelongsToCaller(w, r, p.LegalEntityID, rule.LedgerID) {
		return
	}
	out, err := a.regroups.PutRegroupingRule(r.Context(), rule)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getRegroup(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	if a.regroups == nil {
		fail(w, r, missingSlice("regrouping"))
		return
	}
	ledgerID := r.URL.Query().Get("ledgerId")
	if !ids.Valid(ledgerID) {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "ledgerId must be a UUID"))
		return
	}
	out, err := a.regroups.RegroupingRule(r.Context(), ledgerID, chi.URLParam(r, "code"))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type regroupRunRequest struct {
	LedgerID  string   `json:"ledgerId"`
	KeyDate   string   `json:"keyDate"`
	RuleCodes []string `json:"ruleCodes"`
	Mode      string   `json:"mode"`
}

func uniqueSorted(codes []string) ([]string, bool) {
	out := append([]string(nil), codes...)
	sort.Strings(out)
	for i := 1; i < len(out); i++ {
		if out[i] == out[i-1] {
			return nil, false
		}
	}
	return out, len(out) > 0
}

func (a *API) runRegroup(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	if a.regroups == nil || a.yearLedger == nil {
		fail(w, r, missingSlice("regrouping"))
		return
	}
	if err := requireKey(r.Header.Get(idempotencyHeader)); err != nil {
		fail(w, r, err)
		return
	}
	var req regroupRunRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	if req.Mode == "" {
		req.Mode = "simulate"
	}
	if req.Mode != "simulate" && req.Mode != "post" {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "mode must be simulate or post"))
		return
	}
	keyDate, err := time.Parse(dateLayout, req.KeyDate)
	if err != nil || !ids.Valid(req.LedgerID) {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "ledgerId and keyDate are required"))
		return
	}
	codes, ok := uniqueSorted(req.RuleCodes)
	if !ok {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "ruleCodes must list each rule once and at least one rule"))
		return
	}
	if !a.ledgerBelongsToCaller(w, r, p.LegalEntityID, req.LedgerID) {
		return
	}
	stored, err := a.regroups.ListRegroupingRules(r.Context(), req.LedgerID, codes)
	if err != nil {
		fail(w, r, err)
		return
	}
	rules := make([]domain.RegroupingRule, len(stored))
	needsItems := false
	for i, s := range stored {
		rules[i] = domain.RegroupingRule{Code: s.Code, SortMethod: s.SortMethod, Sequence: s.Sequence, SourceAccount: s.SourceAccount,
			AdjustmentAccount: s.AdjustmentAccount, TargetAccount: s.TargetAccount, NormalBalance: s.NormalBalance, Buckets: s.Buckets}
		needsItems = needsItems || s.SortMethod != domain.SortAccountSign
	}
	if needsItems && a.openItems == nil {
		fail(w, r, missingSlice("open items"))
		return
	}
	var items []domain.OpenItem
	for _, rule := range rules {
		if rule.SortMethod == domain.SortAccountSign {
			continue
		}
		found, err := a.openItems.List(r.Context(), p.LegalEntityID, req.LedgerID, rule.SourceAccount)
		if err != nil {
			a.yearEndFailed(w, r, "regrouping open items read failed", err)
			return
		}
		items = append(items, found...)
	}
	info, err := a.yearLedger.Ledger(r.Context(), p.LegalEntityID, req.LedgerID)
	if err != nil {
		a.yearEndFailed(w, r, "regrouping ledger lookup failed", err)
		return
	}
	tb, err := a.yearLedger.TrialBalance(r.Context(), ledgerclient.TrialBalanceQuery{
		LegalEntityID: p.LegalEntityID, LedgerID: req.LedgerID, PeriodTo: req.KeyDate[:len("2006-01")], GroupBy: info.DimensionAttributes,
	})
	if err != nil {
		a.yearEndFailed(w, r, "regrouping trial balance read failed", err)
		return
	}
	if tb.Currency != "" && tb.Currency != info.Currency {
		fail(w, r, unprocessable("the trial balance currency differs from the ledger currency"))
		return
	}
	balances := make([]domain.Balance, 0, len(tb.Rows))
	for _, row := range tb.Rows {
		balances = append(balances, domain.Balance{Account: row.Account, Amount: row.Net, Dimensions: domain.DimensionKey(row.Dimensions)})
	}
	proposal, err := domain.Regroup(items, balances, rules, keyDate)
	if err != nil {
		fail(w, r, err)
		return
	}
	if req.Mode == "post" && len(proposal) == 0 {
		fail(w, r, unprocessable("nothing to reclassify for these rules on this key date"))
		return
	}
	run := domain.RegroupingRun{
		LegalEntityID: p.LegalEntityID, LedgerID: req.LedgerID, KeyDate: req.KeyDate, RuleCodes: codes,
		Mode: req.Mode, Status: "proposed", Proposal: proposal, CreatedBy: p.UserID,
	}
	hash := store.HashRequest(http.MethodPost, "/v1/close/regroupings", hashKey(req.LedgerID, info.Currency, req.KeyDate, req.Mode, strings.Join(codes, ",")))
	saved, err := a.regroups.SaveRegroupingRun(r.Context(), run, r.Header.Get(idempotencyHeader), hash)
	if err != nil {
		fail(w, r, err)
		return
	}
	if req.Mode == "post" && saved.Status != "posted" {
		saved, err = a.dispatchRegroup(r.Context(), saved)
		if err != nil {
			a.yearEndFailed(w, r, "regrouping post failed", err)
			return
		}
	}
	httpx.JSON(w, http.StatusCreated, saved)
}

func (a *API) dispatchRegroup(ctx context.Context, run domain.RegroupingRun) (domain.RegroupingRun, error) {
	if a.yearLedger == nil {
		return run, missingSlice("regrouping")
	}
	if len(run.Proposal) == 0 {
		return run, unprocessable("a regrouping with nothing to reclassify cannot be posted")
	}
	if err := a.requireOpenPeriod(ctx, run.LegalEntityID, run.LedgerID, run.KeyDate); err != nil {
		return run, err
	}
	info, err := a.yearLedger.Ledger(ctx, run.LegalEntityID, run.LedgerID)
	if err != nil {
		return run, err
	}
	for _, line := range run.Proposal {
		if line.Currency != "" && line.Currency != info.Currency {
			return run, unprocessable("an open item is in " + line.Currency + " but the ledger currency is " + info.Currency)
		}
	}
	if err := a.regroups.ClaimRegroupingRules(ctx, run.ID); err != nil {
		return run, err
	}
	id, err := a.yearLedger.Post(ctx, regroupEntry(run, info.Currency))
	if err != nil {
		if ledgerclient.Rejected(err) {
			if releaseErr := a.regroups.ReleaseRegroupingRules(ctx, run.ID); releaseErr != nil {
				slog.Error("regrouping claim could not be released", "runId", run.ID, "err", releaseErr)
			}
		}
		return run, err
	}
	return a.regroups.MarkRegroupingPosted(ctx, run.ID, id)
}

func regroupEntry(run domain.RegroupingRun, currency string) ledgerclient.Entry {
	var lines []ledgerclient.Line
	for _, p := range run.Proposal {
		if p.Amount.IsZero() {
			continue
		}
		debit, credit := p.AdjustmentAccount, p.TargetAccount
		if p.SourceCredit {
			debit, credit = credit, debit
		}
		dims := domain.ParseDimensions(p.Dimensions)
		lines = append(lines,
			ledgerclient.Line{Account: debit, Amount: p.Amount, Dimensions: dims},
			ledgerclient.Line{Account: credit, Credit: true, Amount: p.Amount, Dimensions: dims},
		)
	}
	doc := uuid.NewSHA1(uuid.MustParse(run.ID), []byte("regrouping")).String()
	return ledgerclient.Entry{
		LegalEntityID: run.LegalEntityID, LedgerID: run.LedgerID, DocumentID: doc, PostingDate: run.KeyDate, Currency: currency,
		IdempotencyKey: doc, SourceType: "regrouping", PostClosed: false, Lines: lines,
	}
}

func (a *API) postRegroup(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	if a.regroups == nil {
		fail(w, r, missingSlice("regrouping"))
		return
	}
	runID := chi.URLParam(r, "runId")
	if !ids.Valid(runID) {
		fail(w, r, notFound("regrouping run not found"))
		return
	}
	run, err := a.regroups.RegroupingRun(r.Context(), runID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if run.Status == "posted" {
		httpx.JSON(w, http.StatusOK, run)
		return
	}
	saved, err := a.dispatchRegroup(r.Context(), run)
	if err != nil {
		a.yearEndFailed(w, r, "regrouping post failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, saved)
}

func (a *API) reverseRegroup(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	if a.regroups == nil || a.yearLedger == nil {
		fail(w, r, missingSlice("regrouping"))
		return
	}
	runID := chi.URLParam(r, "runId")
	if !ids.Valid(runID) {
		fail(w, r, notFound("regrouping run not found"))
		return
	}
	run, err := a.regroups.RegroupingRun(r.Context(), runID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if run.Status != "posted" || run.EntryID == "" {
		fail(w, r, &domain.Error{Code: domain.CodeInvalidTransition, Detail: "only a posted regrouping run can be reversed"})
		return
	}
	revDate := run.KeyDate
	if len(run.Proposal) > 0 && run.Proposal[0].ReverseOn != "" {
		revDate = run.Proposal[0].ReverseOn
	}
	key := uuid.NewSHA1(uuid.MustParse(run.ID), []byte("regrouping-reverse")).String()
	id, err := a.yearLedger.Reverse(r.Context(), ledgerclient.Reversal{
		LegalEntityID: run.LegalEntityID, EntryID: run.EntryID, PostingDate: revDate, IdempotencyKey: key, Description: "Regrouping reversal",
	})
	if err != nil {
		a.yearEndFailed(w, r, "regrouping reverse failed", err)
		return
	}
	saved, err := a.regroups.MarkRegroupingReversed(r.Context(), run.ID, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, saved)
}
