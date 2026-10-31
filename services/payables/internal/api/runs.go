package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payables/internal/ledger"
	"github.com/BenjaminSRussell/passage/services/payables/internal/party"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
)

const (
	dutyRunExecute = "payment-run.execute"
	dutyRunRead    = "payment-run.read"
	pathRuns       = "/v1/payables/payment-runs"
	maxAccountLen  = 20
	maxReasonLen   = 500
	runSourceType  = "payment_run"
	runKeyPrefix   = "payables:payment-run:"
)

type RunStore interface {
	GetProposal(ctx context.Context, id string) (store.Proposal, error)
	ProposalParties(ctx context.Context, id string) (store.ProposalParties, error)
	StartRun(ctx context.Context, in store.NewRun) (store.PaymentRun, bool, error)
	VerifyRunLines(ctx context.Context, id string) (store.PaymentRun, error)
	BeginJournal(ctx context.Context, id string) (store.PaymentRun, error)
	RejectJournal(ctx context.Context, id string) (store.PaymentRun, error)
	GetRun(ctx context.Context, id string) (store.PaymentRun, error)
	RunFileContent(ctx context.Context, id string) (string, error)
	RunFileConfig(ctx context.Context, id string) ([]byte, error)
	InvoiceNumbers(ctx context.Context, proposalID string) (map[string]string, error)
	SaveRunFile(ctx context.Context, id string, f store.RunFile) (store.PaymentRun, error)
	RecordJournal(ctx context.Context, id, entryID string) (store.PaymentRun, error)
	SettleRun(ctx context.Context, id string) (store.PaymentRun, error)
	CompleteRun(ctx context.Context, id string) (store.PaymentRun, error)
	FailRun(ctx context.Context, id, reason string) (store.PaymentRun, error)
}

type LedgerPoster interface {
	Post(ctx context.Context, e ledger.Entry) (string, error)
	LegalEntity(ctx context.Context, legalEntityID, ledgerID string) (string, error)
	EntryForDocument(ctx context.Context, e ledger.Entry) (string, error)
}

type VendorBanks interface {
	BankAccounts(ctx context.Context, legalEntityID, vendorID string) ([]party.BankAccount, error)
}

type runDeps struct {
	store        RunStore
	ledger       LedgerPoster
	banks        VendorBanks
	profiles     PostingProfiles
	partyFaults  atomic.Int64
	started      atomic.Int64
	replayed     atomic.Int64
	completed    atomic.Int64
	failed       atomic.Int64
	ledgerFaults atomic.Int64
	stuck        atomic.Int64
	fileReads    atomic.Int64
}

func (a *API) WithPaymentRuns(s RunStore, l LedgerPoster, profiles PostingProfiles) *API {
	a.runs = &runDeps{store: s, ledger: l, profiles: profiles}
	return a
}

func (a *API) WithVendorBanks(b VendorBanks) *API {
	if a.runs != nil {
		a.runs.banks = b
	}
	return a
}

func (a *API) registerRuns(r chi.Router) {
	if a.runs == nil {
		return
	}
	run := r.With(authz.RequireDuty(dutyRunExecute))
	run.Post(pathRuns, a.startRun)
	run.Post(pathRuns+"/{id}:generate-file", a.generateRunFile)
	run.Post(pathRuns+"/{id}:post-journal", a.postRunJournal)
	run.Post(pathRuns+"/{id}:settle", a.runStep("settle", func(ctx context.Context, s RunStore, id string) (store.PaymentRun, error) {
		return s.SettleRun(ctx, id)
	}))
	run.Post(pathRuns+"/{id}:complete", a.completeRun)
	run.Post(pathRuns+"/{id}:fail", a.failRun)
	r.With(authz.RequireDuty(dutyRunRead)).Get(pathRuns+"/{id}", a.getRun)
	run.Get(pathRuns+"/{id}/file", a.getRunFile)
}

type runRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	ProposalID    string `json:"proposalId"`
}

func (a *API) runLog(r *http.Request, msg string, run store.PaymentRun) {
	slog.Info(msg, "runId", run.ID, "proposalId", run.ProposalID, "status", run.Status, "correlationId", middleware.GetReqID(r.Context()))
}

func (a *API) startRun(w http.ResponseWriter, r *http.Request) {
	if !a.validTenant(w, r) {
		return
	}
	caller, _ := tenant.FromContext(r.Context())
	if caller.UserID == "" {
		a.problem(w, r, http.StatusForbidden, domain.CodeRunIdentity, "a payment run needs an identified user")
		return
	}
	var req runRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.reject(w, r, err)
		return
	}
	le, ok := a.resolveEntity(w, r, req.LegalEntityID)
	if !ok {
		return
	}
	if !ids.Valid(req.ProposalID) {
		a.invalid(w, r, map[string]string{"proposalId": "is required and must be a UUID"})
		return
	}
	ctx := r.Context()
	profile, configured := a.runs.profiles.For(le)
	if !configured || a.runs.ledger == nil || a.runs.banks == nil {
		a.problem(w, r, http.StatusUnprocessableEntity, domain.CodeRunConfig, "the legal entity has no payment run posting profile, or the ledger or the party service is not configured")
		return
	}
	ledgerEntity, err := a.runs.ledger.LegalEntity(ctx, le, profile.LedgerID)
	if err != nil {
		a.runLedgerFailure(w, r, err)
		return
	}
	if want, _ := ids.Canonical(ledgerEntity); want != le {
		a.problem(w, r, http.StatusUnprocessableEntity, domain.CodeRunConfig, "the configured ledger belongs to a different legal entity")
		return
	}
	proposal, err := a.runs.store.GetProposal(ctx, req.ProposalID)
	if errors.Is(err, store.ErrProposalNotFound) || (err == nil && proposal.LegalEntityID != le) {
		a.problem(w, r, http.StatusNotFound, codeNotFound, "payment proposal not found")
		return
	}
	if err != nil {
		a.fault(w, r, "payment proposal get failed", err)
		return
	}
	if err := domain.CheckRunnable(proposal.LegalEntityCurrency, proposal.Groups); err != nil {
		a.reject(w, r, err)
		return
	}
	if domain.HasDiscount(proposal.Groups) && profile.DiscountAccount == "" {
		a.problem(w, r, http.StatusUnprocessableEntity, domain.CodeRunConfig, "the proposal takes cash discounts and the posting profile has no discount account")
		return
	}
	if domain.HasWithholding(proposal.Groups) && profile.WithholdingAccount == "" {
		a.problem(w, r, http.StatusUnprocessableEntity, domain.CodeRunConfig, "the proposal withholds tax and the posting profile has no withholding account")
		return
	}
	fileConfig, err := json.Marshal(profile.File)
	if err != nil {
		a.fault(w, r, "posting profile unreadable", err)
		return
	}
	run, created, err := a.runs.store.StartRun(ctx, store.NewRun{FileConfig: fileConfig,
		LegalEntityID: le, ProposalID: req.ProposalID, RequestedBy: caller.UserID, Today: a.today().Format(time.DateOnly), LedgerID: profile.LedgerID,
		BankAccount: profile.BankAccount, ControlAccount: profile.ControlAccount, DiscountAccount: profile.DiscountAccount, WithholdingAccount: profile.WithholdingAccount,
	})
	switch {
	case errors.Is(err, store.ErrProposalNotFound):
		a.problem(w, r, http.StatusNotFound, codeNotFound, "payment proposal not found")
	case errors.Is(err, store.ErrNotApproved):
		a.problem(w, r, http.StatusConflict, domain.CodeProposalState, "payment proposal is not approved")
	case errors.Is(err, store.ErrRunDateElapsed):
		a.problem(w, r, http.StatusConflict, domain.CodeRunDatePassed, "run date has passed; create a new proposal")
	case errors.Is(err, store.ErrExecutorConflict):
		slog.Warn("payment run refused: executor created or approved the proposal", "proposalId", req.ProposalID, "userId", caller.UserID, "correlationId", middleware.GetReqID(ctx))
		a.problem(w, r, http.StatusForbidden, domain.CodeSegregation, err.Error())
	case errors.Is(err, store.ErrUnsupportedMethod), errors.Is(err, store.ErrDiscountAccount), errors.Is(err, store.ErrWithholdingAccount):
		a.problem(w, r, http.StatusUnprocessableEntity, domain.CodeRunConfig, err.Error())
	case err != nil:
		a.fault(w, r, "payment run start failed", err)
	case !created:
		a.runs.replayed.Add(1)
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, run)
	default:
		a.runs.started.Add(1)
		if run.Status == domain.RunFailed {
			a.runs.failed.Add(1)
		}
		a.runLog(r, "payment run started", run)
		w.Header().Set("Location", pathRuns+"/"+run.ID)
		httpx.JSON(w, http.StatusCreated, run)
	}
}

func (a *API) runID(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !a.validTenant(w, r) {
		return "", false
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		a.invalid(w, r, map[string]string{"id": "must be a UUID"})
		return "", false
	}
	return id, true
}

func subject(user string) string { return strings.ToLower(strings.TrimSpace(user)) }

func (a *API) executorRunID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, ok := a.runID(w, r)
	if !ok {
		return "", false
	}
	caller, _ := tenant.FromContext(r.Context())
	run, err := a.runs.store.GetRun(r.Context(), id)
	if err != nil {
		a.runResult(w, r, "authorize", run, err)
		return "", false
	}
	parties, err := a.runs.store.ProposalParties(r.Context(), run.ProposalID)
	if err != nil {
		a.fault(w, r, "payment proposal parties failed", err)
		return "", false
	}
	actor := subject(caller.UserID)
	if actor == "" || actor == subject(parties.CreatedBy) || (parties.ApprovedBy != "" && actor == subject(parties.ApprovedBy)) {
		slog.Warn("payment run step refused: the actor created or approved the proposal", "runId", id, "userId", caller.UserID, "correlationId", middleware.GetReqID(r.Context()))
		a.problem(w, r, http.StatusForbidden, domain.CodeSegregation, "a payment run is executed by a user who neither created nor approved the proposal")
		return "", false
	}
	return id, true
}

func (a *API) runResult(w http.ResponseWriter, r *http.Request, msg string, run store.PaymentRun, err error) {
	var changed *store.LinesChangedError
	switch {
	case errors.Is(err, store.ErrRunNotFound):
		a.problem(w, r, http.StatusNotFound, codeNotFound, "payment run not found")
	case errors.Is(err, store.ErrRunState), errors.Is(err, store.ErrJournalRequested):
		a.problem(w, r, http.StatusConflict, domain.CodeRunState, err.Error())
	case errors.Is(err, store.ErrRunOutOfOrder):
		a.problem(w, r, http.StatusConflict, domain.CodeRunOutOfOrder, err.Error())
	case errors.Is(err, store.ErrVersionConflict), errors.Is(err, store.ErrOverSettled), errors.Is(err, store.ErrNotFound):
		a.runs.stuck.Add(1)
		slog.Error("payment run stuck: an open item changed after the journal posted; manual action needed", "runId", chi.URLParam(r, "id"), "step", msg, "err", err, "correlationId", middleware.GetReqID(r.Context()))
		a.problem(w, r, http.StatusConflict, domain.CodeRunItemChanged, "an open item changed since the proposal was approved: "+err.Error())
	case errors.As(err, &changed):
		a.problem(w, r, http.StatusConflict, domain.CodeRunItemChanged, changed.Error())
	case err != nil:
		a.fault(w, r, "payment run "+msg+" failed", err)
	default:
		a.runLog(r, "payment run "+msg, run)
		httpx.JSON(w, http.StatusOK, run)
	}
}

func (a *API) runStep(name string, do func(ctx context.Context, s RunStore, id string) (store.PaymentRun, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := a.executorRunID(w, r)
		if !ok {
			return
		}
		run, err := do(r.Context(), a.runs.store, id)
		a.runResult(w, r, name, run, err)
	}
}

func (a *API) completeRun(w http.ResponseWriter, r *http.Request) {
	id, ok := a.executorRunID(w, r)
	if !ok {
		return
	}
	run, err := a.runs.store.CompleteRun(r.Context(), id)
	if err == nil && run.Status == domain.RunCompleted {
		a.runs.completed.Add(1)
	}
	a.runResult(w, r, "complete", run, err)
}

type failRequest struct {
	Reason string `json:"reason"`
}

func (a *API) failRun(w http.ResponseWriter, r *http.Request) {
	id, ok := a.executorRunID(w, r)
	if !ok {
		return
	}
	var req failRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.reject(w, r, err)
		return
	}
	if req.Reason == "" || len(req.Reason) > maxReasonLen {
		a.invalid(w, r, map[string]string{"reason": fmt.Sprintf("is required, at most %d characters", maxReasonLen)})
		return
	}
	run, err := a.runs.store.FailRun(r.Context(), id, req.Reason)
	if err == nil && run.Status == domain.RunFailed {
		a.runs.failed.Add(1)
	}
	a.runResult(w, r, "fail", run, err)
}

func (a *API) getRun(w http.ResponseWriter, r *http.Request) {
	id, ok := a.runID(w, r)
	if !ok {
		return
	}
	run, err := a.runs.store.GetRun(r.Context(), id)
	if errors.Is(err, store.ErrRunNotFound) {
		a.problem(w, r, http.StatusNotFound, codeNotFound, "payment run not found")
		return
	}
	if err != nil {
		a.fault(w, r, "payment run get failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, run)
}

func (a *API) getRunFile(w http.ResponseWriter, r *http.Request) {
	id, ok := a.executorRunID(w, r)
	if !ok {
		return
	}
	content, err := a.runs.store.RunFileContent(r.Context(), id)
	caller, _ := tenant.FromContext(r.Context())
	switch {
	case errors.Is(err, store.ErrRunNotFound), err == nil && content == "":
		a.problem(w, r, http.StatusNotFound, codeNotFound, "payment file not found")
	case err != nil:
		a.fault(w, r, "payment file get failed", err)
	default:
		a.runs.fileReads.Add(1)
		slog.Info("audit: payment file read", "audit", true, "runId", id, "userId", caller.UserID, "service", caller.Service, "tenantId", caller.TenantID, "correlationId", middleware.GetReqID(r.Context()))
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(content))
	}
}

func (a *API) generateRunFile(w http.ResponseWriter, r *http.Request) {
	id, ok := a.executorRunID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	run, err := a.runs.store.GetRun(ctx, id)
	switch {
	case err != nil:
		a.runResult(w, r, "generate-file", run, err)
		return
	case run.Status == domain.RunFailed:
		a.runResult(w, r, "generate-file", run, store.ErrRunState)
		return
	case run.FileGeneratedAt != nil:
		httpx.JSON(w, http.StatusOK, run)
		return
	}
	if _, err := a.runs.store.VerifyRunLines(ctx, id); err != nil {
		a.runResult(w, r, "generate-file", run, err)
		return
	}
	rawConfig, err := a.runs.store.RunFileConfig(ctx, id)
	var config domain.RunFileConfig
	if err == nil {
		err = json.Unmarshal(rawConfig, &config)
	}
	if err != nil {
		a.fault(w, r, "run file configuration unreadable", err)
		return
	}
	proposal, err := a.runs.store.GetProposal(ctx, run.ProposalID)
	if err != nil {
		a.fault(w, r, "payment proposal get failed", err)
		return
	}
	if a.runs.banks == nil {
		a.problem(w, r, http.StatusServiceUnavailable, codeNotConfigured, "party service is not configured")
		return
	}
	numbers, err := a.runs.store.InvoiceNumbers(ctx, run.ProposalID)
	if err != nil {
		a.fault(w, r, "invoice numbers failed", err)
		return
	}
	runDate, err := time.Parse(time.DateOnly, proposal.RunDate)
	if err != nil {
		a.fault(w, r, "proposal run date unreadable", err)
		return
	}
	payees, ok := a.vendorCreditors(w, r, run.LegalEntityID, proposal.Groups)
	if !ok {
		return
	}
	file, payments, err := domain.BuildRunFile(config, payees, run.ID, runDate, a.now(), proposal.Groups, numbers)
	if err != nil {
		a.reject(w, r, err)
		return
	}
	found, err := a.files.Validate(ctx, file.Content)
	if err != nil {
		a.failDomain(w, r, err)
		return
	}
	if len(found) > 0 {
		a.problem(w, r, http.StatusUnprocessableEntity, domain.CodeFileXSD, "the generated pain.001 failed XSD validation: "+found[0].Path+": "+found[0].Reason)
		return
	}
	saved, err := a.runs.store.SaveRunFile(ctx, id, store.RunFile{Number: file.FileNumber, SHA256: file.SHA256, Content: string(file.Content), PaymentCount: file.PaymentCount, Payments: payments})
	a.runResult(w, r, "generate-file", saved, err)
}

func (a *API) postRunJournal(w http.ResponseWriter, r *http.Request) {
	id, ok := a.executorRunID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	run, err := a.runs.store.GetRun(ctx, id)
	switch {
	case err != nil:
		a.runResult(w, r, "post-journal", run, err)
		return
	case run.JournalPostedAt != nil:
		httpx.JSON(w, http.StatusOK, run)
		return
	case run.Status != domain.RunRunning:
		a.runResult(w, r, "post-journal", run, store.ErrRunState)
		return
	case run.FileGeneratedAt == nil:
		a.runResult(w, r, "post-journal", run, store.ErrRunOutOfOrder)
		return
	}
	if a.runs.ledger == nil {
		a.problem(w, r, http.StatusServiceUnavailable, codeNotConfigured, "ledger is not configured")
		return
	}
	if _, err := a.runs.store.BeginJournal(ctx, id); err != nil {
		a.runResult(w, r, "post-journal", run, err)
		return
	}
	proposal, err := a.runs.store.GetProposal(ctx, run.ProposalID)
	if err != nil {
		a.fault(w, r, "payment proposal get failed", err)
		return
	}
	discountAccount := ""
	if run.DiscountAccount != nil {
		discountAccount = *run.DiscountAccount
	}
	withholdingAccount := ""
	if run.WithholdingAccount != nil {
		withholdingAccount = *run.WithholdingAccount
	}
	entry := ledger.Entry{
		LegalEntityID: run.LegalEntityID, LedgerID: run.LedgerID, DocumentID: run.ID, PostingDate: proposal.RunDate, Currency: proposal.LegalEntityCurrency,
		IdempotencyKey: runKeyPrefix + run.ID, SourceType: runSourceType,
		Lines: domain.RunJournal(proposal.LegalEntityCurrency, proposal.Groups, run.ControlAccount, run.BankAccount, discountAccount, withholdingAccount),
	}
	entryID, err := a.runs.ledger.Post(ctx, entry)
	if err != nil {
		if ledgerRejected(err) {
			existing, lookupErr := a.runs.ledger.EntryForDocument(ctx, entry)
			if lookupErr != nil {
				a.ledgerUnavailable(w, r, lookupErr)
				return
			}
			if existing != "" {
				recorded, recordErr := a.runs.store.RecordJournal(ctx, id, existing)
				a.runResult(w, r, "post-journal", recorded, recordErr)
				return
			}
			if _, rerr := a.runs.store.RejectJournal(ctx, id); rerr != nil {
				a.fault(w, r, "payment run journal rejection not recorded", rerr)
				return
			}
		}
		a.runLedgerFailure(w, r, err)
		return
	}
	recorded, err := a.runs.store.RecordJournal(ctx, id, entryID)
	a.runResult(w, r, "post-journal", recorded, err)
}

func (a *API) vendorCreditors(w http.ResponseWriter, r *http.Request, legalEntityID string, groups []domain.ProposalGroup) (map[string]domain.CreditorDTO, bool) {
	creditors := map[string]domain.CreditorDTO{}
	accounts := map[string][]party.BankAccount{}
	for _, g := range groups {
		list, fetched := accounts[g.VendorID]
		if !fetched {
			var err error
			if list, err = a.runs.banks.BankAccounts(r.Context(), legalEntityID, g.VendorID); errors.Is(err, party.ErrVendorNotFound) {
				a.reject(w, r, &domain.Error{Code: domain.CodeRunPayee, Detail: "vendor " + g.VendorID + " is not in the party vendor master"})
				return nil, false
			} else if err != nil {
				a.partyUnavailable(w, r, err)
				return nil, false
			}
			accounts[g.VendorID] = list
		}
		creditor, err := creditorFor(g, list)
		if err != nil {
			a.reject(w, r, err)
			return nil, false
		}
		creditors[g.VendorID] = creditor
	}
	return creditors, true
}

func creditorFor(g domain.ProposalGroup, accounts []party.BankAccount) (domain.CreditorDTO, error) {
	var match []party.BankAccount
	for _, b := range accounts {
		if strings.EqualFold(b.CurrencyCode, g.Currency) {
			match = append(match, b)
		}
	}
	switch {
	case len(match) == 0:
		return domain.CreditorDTO{}, &domain.Error{Code: domain.CodeRunPayee, Detail: "vendor " + g.VendorID + " has no " + g.Currency + " bank account in the party vendor master"}
	case len(match) > 1:
		return domain.CreditorDTO{}, &domain.Error{Code: domain.CodeRunPayee, Detail: "vendor " + g.VendorID + " has more than one " + g.Currency + " bank account in the party vendor master; the payee is ambiguous"}
	}
	creditor := match[0].Creditor()
	if creditor.Name == "" || creditor.Address.Country == "" || (creditor.IBAN == "" && creditor.OtherID == "") {
		return domain.CreditorDTO{}, &domain.Error{Code: domain.CodeRunPayee, Detail: "the " + g.Currency + " bank account of vendor " + g.VendorID + " has no account holder, bank country or account number"}
	}
	return creditor, nil
}

func (a *API) partyUnavailable(w http.ResponseWriter, r *http.Request, err error) {
	a.runs.partyFaults.Add(1)
	slog.Error("party unavailable", "err", err, "correlationId", middleware.GetReqID(r.Context()))
	w.Header().Set("Retry-After", "1")
	fail(w, r, httpx.NewProblem(http.StatusBadGateway, "COM-0502", "party service unavailable"))
}

func ledgerRejected(err error) bool {
	var se *ledger.StatusError
	return errors.As(err, &se) && (se.Status == http.StatusBadRequest || se.Status == http.StatusUnprocessableEntity)
}

func (a *API) runLedgerFailure(w http.ResponseWriter, r *http.Request, err error) {
	var se *ledger.StatusError
	if ledgerRejected(err) && errors.As(err, &se) {
		a.problem(w, r, http.StatusUnprocessableEntity, domain.CodeRunLedger, fmt.Sprintf("the ledger rejected the request with status %d", se.Status))
		return
	}
	a.ledgerUnavailable(w, r, err)
}

func (a *API) ledgerUnavailable(w http.ResponseWriter, r *http.Request, err error) {
	a.runs.ledgerFaults.Add(1)
	slog.Error("ledger unavailable", "err", err, "correlationId", middleware.GetReqID(r.Context()))
	w.Header().Set("Retry-After", "1")
	fail(w, r, httpx.NewProblem(http.StatusBadGateway, "COM-0502", "ledger unavailable"))
}

func (a *API) writeRunMetrics(w http.ResponseWriter) {
	if a.runs == nil {
		return
	}
	fmt.Fprintf(w, "# HELP payables_payment_runs_total Payment run commands by outcome.\n# TYPE payables_payment_runs_total counter\npayables_payment_runs_total{outcome=\"started\"} %d\npayables_payment_runs_total{outcome=\"replayed\"} %d\npayables_payment_runs_total{outcome=\"completed\"} %d\npayables_payment_runs_total{outcome=\"failed\"} %d\n",
		a.runs.started.Load(), a.runs.replayed.Load(), a.runs.completed.Load(), a.runs.failed.Load())
	fmt.Fprintf(w, "# HELP payables_payment_run_ledger_faults_total Payment run ledger calls that failed on a server or transport fault.\n# TYPE payables_payment_run_ledger_faults_total counter\npayables_payment_run_ledger_faults_total %d\n", a.runs.ledgerFaults.Load())
	fmt.Fprintf(w, "# HELP payables_payment_run_party_faults_total Payment run vendor bank lookups that failed on a server or transport fault.\n# TYPE payables_payment_run_party_faults_total counter\npayables_payment_run_party_faults_total %d\n", a.runs.partyFaults.Load())
	fmt.Fprintf(w, "# HELP payables_payment_run_stuck_total Runs whose settlement found an item changed after the journal posted; each needs manual action.\n# TYPE payables_payment_run_stuck_total counter\npayables_payment_run_stuck_total %d\n", a.runs.stuck.Load())
	fmt.Fprintf(w, "# HELP payables_payment_file_reads_total Audited reads of a generated payment file.\n# TYPE payables_payment_file_reads_total counter\npayables_payment_file_reads_total %d\n", a.runs.fileReads.Load())
}
