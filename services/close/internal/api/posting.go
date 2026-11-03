package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/money"
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
	if !ids.Valid(p.LegalEntityID) || !ids.Valid(rule.LedgerID) || rule.Name == "" || rule.OffsetAccount == "" {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "ledgerId, name and offsetAccount are required"))
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

func (a *API) getAllocRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.principal(w, r); !ok {
		return
	}
	if a.allocs == nil {
		fail(w, r, missingSlice("allocations"))
		return
	}
	out, err := a.allocs.AllocationRule(r.Context(), chi.URLParam(r, "ruleId"))
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
	info, err := a.yearLedger.Ledger(r.Context(), p.LegalEntityID, req.LedgerID)
	if err != nil {
		a.yearEndFailed(w, r, "allocation ledger lookup failed", err)
		return
	}
	amount, err := a.sourceAmount(r, info, rule, req.PostingDate)
	if err != nil {
		fail(w, r, err)
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
		if rule.AllocateFromZero && amount.IsZero() {
			fail(w, r, err)
			return
		}
		fail(w, r, err)
		return
	}
	run := domain.AllocationRun{
		LegalEntityID: p.LegalEntityID, LedgerID: req.LedgerID, FiscalPeriodID: req.FiscalPeriodID, RuleID: req.RuleID,
		PostingDate: req.PostingDate, Currency: info.Currency, SourceAmount: amount.String(), Mode: req.Mode, Status: "proposed",
		Proposal: proposal, CreatedBy: p.UserID,
	}
	hash := store.HashRequest(http.MethodPost, "/v1/close/allocation-runs", []byte(req.RuleID+req.FiscalPeriodID+req.PostingDate+req.Mode))
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

func (a *API) sourceAmount(r *http.Request, info ledgerclient.Info, rule domain.AllocationRule, postingDate string) (decimal.Decimal, error) {
	period := postingDate[:len("2006-01")]
	tb, err := a.yearLedger.TrialBalance(r.Context(), ledgerclient.TrialBalanceQuery{
		LegalEntityID: rule.LegalEntityID, LedgerID: rule.LedgerID, PeriodTo: period, GroupBy: info.DimensionAttributes,
	})
	if err != nil {
		return decimal.Zero, err
	}
	want := map[string]bool{}
	for _, acct := range rule.SourceAccounts {
		want[acct] = true
	}
	total := decimal.Zero
	for _, row := range tb.Rows {
		if len(want) > 0 && !want[row.Account] {
			continue
		}
		total = total.Add(row.Net)
	}
	return total, nil
}

func (a *API) dispatchAllocation(r *http.Request, run domain.AllocationRun) (domain.AllocationRun, error) {
	if err := a.requireOpenPeriod(r, run.LegalEntityID, run.LedgerID, run.PostingDate); err != nil {
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
	run, err := a.allocs.AllocationRun(r.Context(), chi.URLParam(r, "runId"))
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
	run, err := a.allocs.AllocationRun(r.Context(), chi.URLParam(r, "runId"))
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
	scheme.Code, scheme.LegalEntityID = chi.URLParam(r, "code"), p.LegalEntityID
	if scheme.Frequency == "" {
		scheme.Frequency = "monthly"
	}
	if scheme.Spread == "" {
		scheme.Spread = domain.SpreadEqual
	}
	if scheme.Occurrences == 0 {
		scheme.Occurrences = 12
	}
	if scheme.Code == "" || !ids.Valid(scheme.LedgerID) || scheme.DebitAccount == "" || scheme.CreditAccount == "" {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "code, ledgerId, debitAccount and creditAccount are required"))
		return
	}
	if !a.ledgerBelongsToCaller(w, r, p.LegalEntityID, scheme.LedgerID) {
		return
	}
	out, err := a.accruals.PutAccrualScheme(r.Context(), scheme)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
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
	out, err := a.accruals.AccrualScheme(r.Context(), ledgerID, chi.URLParam(r, "code"))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type applyAccrualRequest struct {
	LedgerID  string `json:"ledgerId"`
	Amount    string `json:"amount"`
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
	amount, err := decimal.NewFromString(req.Amount)
	if err != nil || !amount.IsPositive() {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "amount must be a positive decimal"))
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
	currency := req.Currency
	if currency == "" {
		currency = info.Currency
	}
	custom := make([]decimal.Decimal, len(scheme.Custom))
	for i, c := range scheme.Custom {
		custom[i], _ = decimal.NewFromString(c)
	}
	occ, err := domain.SpreadAccrual(amount, start, domain.AccrualScheme{Occurrences: scheme.Occurrences, Spread: scheme.Spread, Custom: custom, AutoReverse: scheme.AutoReverse}, money.MinorUnits(currency))
	if err != nil {
		fail(w, r, err)
		return
	}
	hash := store.HashRequest(http.MethodPost, r.URL.Path, []byte(req.Amount+req.StartDate))
	key := r.Header.Get(idempotencyHeader)
	run := domain.AccrualRun{
		LegalEntityID: p.LegalEntityID, LedgerID: req.LedgerID, SchemeCode: scheme.Code, Amount: amount.String(), Currency: currency,
		StartDate: req.StartDate, Status: "scheduled", Occurrences: occ, EntryIDs: map[string]string{}, CreatedBy: p.UserID,
	}
	saved, err := a.accruals.SaveAccrualRun(r.Context(), run, key, hash)
	if err != nil {
		fail(w, r, err)
		return
	}
	if saved.Status == "posted" {
		httpx.JSON(w, http.StatusOK, saved)
		return
	}
	if saved.EntryIDs == nil {
		saved.EntryIDs = map[string]string{}
	}
	for _, o := range saved.Occurrences {
		occKey := "occ-" + o.Date.Format(dateLayout)
		if saved.EntryIDs[occKey] != "" {
			continue
		}
		if err := a.requireOpenPeriod(r, p.LegalEntityID, req.LedgerID, o.Date.Format(dateLayout)); err != nil {
			fail(w, r, err)
			return
		}
		doc := uuid.NewSHA1(uuid.MustParse(saved.ID), []byte("accrual-"+o.Date.Format(dateLayout))).String()
		entry := ledgerclient.Entry{
			LegalEntityID: p.LegalEntityID, LedgerID: req.LedgerID, PostingDate: o.Date.Format(dateLayout), Currency: saved.Currency,
			DocumentID: doc, IdempotencyKey: doc, SourceType: "accrual", PostClosed: false, Lines: []ledgerclient.Line{
				{Account: scheme.DebitAccount, Amount: o.Amount},
				{Account: scheme.CreditAccount, Credit: true, Amount: o.Amount},
			},
		}
		id, err := a.yearLedger.Post(r.Context(), entry)
		if err != nil {
			a.yearEndFailed(w, r, "accrual post failed", err)
			return
		}
		saved.EntryIDs[occKey] = id
		if scheme.AutoReverse {
			rev, err := a.yearLedger.Reverse(r.Context(), ledgerclient.Reversal{
				LegalEntityID: p.LegalEntityID, EntryID: id, PostingDate: o.ReverseOn.Format(dateLayout),
				IdempotencyKey: uuid.NewSHA1(uuid.MustParse(saved.ID), []byte("accrual-rev-"+o.Date.Format(dateLayout))).String(),
				Description:    "Accrual reversal", PostClosed: false,
			})
			if err != nil {
				a.yearEndFailed(w, r, "accrual reversal failed", err)
				return
			}
			saved.EntryIDs["rev-"+o.ReverseOn.Format(dateLayout)] = rev
		}
	}
	saved.Status = "posted"
	saved, err = a.accruals.SaveAccrualRun(r.Context(), saved, key, hash)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, saved)
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
	if rule.Code == "" || !ids.Valid(rule.LedgerID) {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "code and ledgerId are required"))
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
	out, err := a.regroups.RegroupingRule(r.Context(), r.URL.Query().Get("ledgerId"), chi.URLParam(r, "code"))
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

func (a *API) runRegroup(w http.ResponseWriter, r *http.Request) {
	p, ok := a.principal(w, r)
	if !ok {
		return
	}
	if a.regroups == nil {
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
	keyDate, err := time.Parse(dateLayout, req.KeyDate)
	if err != nil || !ids.Valid(req.LedgerID) {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "ledgerId and keyDate are required"))
		return
	}
	if !a.ledgerBelongsToCaller(w, r, p.LegalEntityID, req.LedgerID) {
		return
	}
	stored, err := a.regroups.ListRegroupingRules(r.Context(), req.LedgerID, req.RuleCodes)
	if err != nil {
		fail(w, r, err)
		return
	}
	rules := make([]domain.RegroupingRule, len(stored))
	for i, s := range stored {
		rules[i] = domain.RegroupingRule{Code: s.Code, SortMethod: s.SortMethod, Sequence: s.Sequence, SourceAccount: s.SourceAccount,
			AdjustmentAccount: s.AdjustmentAccount, TargetAccount: s.TargetAccount, NormalBalance: s.NormalBalance, Buckets: s.Buckets}
	}
	var items []domain.OpenItem
	if a.openItems != nil {
		for _, rule := range rules {
			if rule.SortMethod == domain.SortAccountSign {
				continue
			}
			found, err := a.openItems.List(r.Context(), p.LegalEntityID, req.LedgerID, rule.SourceAccount)
			if err != nil {
				fail(w, r, err)
				return
			}
			items = append(items, found...)
		}
	}
	var balances []domain.Balance
	if a.yearLedger != nil {
		info, err := a.yearLedger.Ledger(r.Context(), p.LegalEntityID, req.LedgerID)
		if err != nil {
			a.yearEndFailed(w, r, "regrouping ledger lookup failed", err)
			return
		}
		tb, err := a.yearLedger.TrialBalance(r.Context(), ledgerclient.TrialBalanceQuery{
			LegalEntityID: p.LegalEntityID, LedgerID: req.LedgerID, PeriodTo: req.KeyDate[:len("2006-01")], GroupBy: info.DimensionAttributes,
		})
		if err != nil {
			fail(w, r, err)
			return
		}
		for _, row := range tb.Rows {
			balances = append(balances, domain.Balance{Account: row.Account, Amount: row.Net, Dimensions: domain.DimensionKey(row.Dimensions)})
		}
	}
	proposal, err := domain.Regroup(items, balances, rules, keyDate)
	if err != nil {
		fail(w, r, err)
		return
	}
	run := domain.RegroupingRun{
		LegalEntityID: p.LegalEntityID, LedgerID: req.LedgerID, KeyDate: req.KeyDate, RuleCodes: req.RuleCodes,
		Mode: req.Mode, Status: "proposed", Proposal: proposal, CreatedBy: p.UserID,
	}
	hash := store.HashRequest(http.MethodPost, "/v1/close/regroupings", []byte(req.LedgerID+req.KeyDate+req.Mode))
	saved, err := a.regroups.SaveRegroupingRun(r.Context(), run, r.Header.Get(idempotencyHeader), hash)
	if err != nil {
		fail(w, r, err)
		return
	}
	if req.Mode == "post" && saved.Status != "posted" {
		saved, err = a.dispatchRegroup(r, saved)
		if err != nil {
			a.yearEndFailed(w, r, "regrouping post failed", err)
			return
		}
	}
	httpx.JSON(w, http.StatusCreated, saved)
}

func (a *API) dispatchRegroup(r *http.Request, run domain.RegroupingRun) (domain.RegroupingRun, error) {
	if a.yearLedger == nil {
		return run, missingSlice("regrouping")
	}
	if err := a.requireOpenPeriod(r, run.LegalEntityID, run.LedgerID, run.KeyDate); err != nil {
		return run, err
	}
	if len(run.Proposal) == 0 {
		return a.regroups.MarkRegroupingPosted(r.Context(), run.ID, "")
	}
	info, err := a.yearLedger.Ledger(r.Context(), run.LegalEntityID, run.LedgerID)
	if err != nil {
		return run, err
	}
	id, err := a.yearLedger.Post(r.Context(), regroupEntry(run, info.Currency))
	if err != nil {
		return run, err
	}
	return a.regroups.MarkRegroupingPosted(r.Context(), run.ID, id)
}

func regroupEntry(run domain.RegroupingRun, currency string) ledgerclient.Entry {
	var lines []ledgerclient.Line
	for _, p := range run.Proposal {
		if p.Amount.IsZero() {
			continue
		}
		lines = append(lines,
			ledgerclient.Line{Account: p.AdjustmentAccount, Amount: p.Amount, Dimensions: domain.ParseDimensions(p.Dimensions)},
			ledgerclient.Line{Account: p.TargetAccount, Credit: true, Amount: p.Amount, Dimensions: domain.ParseDimensions(p.Dimensions)},
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
	run, err := a.regroups.RegroupingRun(r.Context(), chi.URLParam(r, "runId"))
	if err != nil {
		fail(w, r, err)
		return
	}
	if run.Status == "posted" {
		httpx.JSON(w, http.StatusOK, run)
		return
	}
	saved, err := a.dispatchRegroup(r, run)
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
	run, err := a.regroups.RegroupingRun(r.Context(), chi.URLParam(r, "runId"))
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
