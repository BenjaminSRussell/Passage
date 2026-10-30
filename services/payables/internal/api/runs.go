package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
	StartRun(ctx context.Context, in store.NewRun) (store.PaymentRun, bool, error)
	GetRun(ctx context.Context, id string) (store.PaymentRun, error)
	RunFileContent(ctx context.Context, id string) (string, error)
	InvoiceNumbers(ctx context.Context, proposalID string) (map[string]string, error)
	SaveRunFile(ctx context.Context, id string, f store.RunFile) (store.PaymentRun, error)
	RecordJournal(ctx context.Context, id, entryID string) (store.PaymentRun, error)
	SettleRun(ctx context.Context, id string) (store.PaymentRun, error)
	CompleteRun(ctx context.Context, id string) (store.PaymentRun, error)
	FailRun(ctx context.Context, id, reason string) (store.PaymentRun, error)
}

type LedgerPoster interface {
	Post(ctx context.Context, e ledger.Entry) (string, error)
	LegalEntity(ctx context.Context, ledgerID string) (string, error)
}

type runDeps struct {
	store          RunStore
	ledger         LedgerPoster
	controlAccount string
	started        atomic.Int64
	replayed       atomic.Int64
	completed      atomic.Int64
	failed         atomic.Int64
	ledgerFaults   atomic.Int64
}

func (a *API) WithPaymentRuns(s RunStore, l LedgerPoster, controlAccount string) *API {
	if controlAccount == "" {
		controlAccount = DefaultControlAcct
	}
	a.runs = &runDeps{store: s, ledger: l, controlAccount: controlAccount}
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
	r.With(authz.RequireDuty(dutyRunRead)).Get(pathRuns+"/{id}/file", a.getRunFile)
}

type runRequest struct {
	LegalEntityID   string               `json:"legalEntityId"`
	ProposalID      string               `json:"proposalId"`
	LedgerID        string               `json:"ledgerId"`
	BankAccount     string               `json:"bankAccount"`
	ControlAccount  string               `json:"controlAccount"`
	DiscountAccount string               `json:"discountAccount"`
	File            domain.RunFileConfig `json:"file"`
}

func (q runRequest) validate() map[string]string {
	fields := map[string]string{}
	if !ids.Valid(q.ProposalID) {
		fields["proposalId"] = "is required and must be a UUID"
	}
	if !ids.Valid(q.LedgerID) {
		fields["ledgerId"] = "is required and must be a UUID"
	}
	for name, v := range map[string]string{"bankAccount": q.BankAccount, "controlAccount": q.ControlAccount} {
		if v == "" || len(v) > maxAccountLen {
			fields[name] = fmt.Sprintf("is required, at most %d characters", maxAccountLen)
		}
	}
	if len(q.DiscountAccount) > maxAccountLen {
		fields["discountAccount"] = fmt.Sprintf("at most %d characters", maxAccountLen)
	}
	for vendor := range q.File.Creditors {
		if !ids.Valid(vendor) {
			fields["file.creditors"] = "keys must be vendor UUIDs"
		}
	}
	return fields
}

func (q runRequest) hash() string {
	raw, _ := json.Marshal(q)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
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
		a.problem(w, r, http.StatusForbidden, "AUTH-0003", "a payment run needs an identified user")
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
	req.LegalEntityID = le
	if req.ControlAccount == "" {
		req.ControlAccount = a.runs.controlAccount
	}
	if fields := req.validate(); len(fields) > 0 {
		a.invalid(w, r, fields)
		return
	}
	ctx := r.Context()
	if a.runs.ledger == nil {
		a.problem(w, r, http.StatusServiceUnavailable, codeRatesUnconfigured, "ledger is not configured")
		return
	}
	ledgerEntity, err := a.runs.ledger.LegalEntity(ctx, req.LedgerID)
	if err != nil {
		a.runLedgerFailure(w, r, err)
		return
	}
	if want, _ := ids.Canonical(ledgerEntity); want != le {
		a.problem(w, r, http.StatusUnprocessableEntity, domain.CodeRunConfig, "the ledger belongs to a different legal entity")
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
	if err := req.File.CheckVendors(proposal.Groups); err != nil {
		a.reject(w, r, err)
		return
	}
	fileRequest, _ := json.Marshal(req.File)
	run, created, err := a.runs.store.StartRun(ctx, store.NewRun{
		LegalEntityID: le, ProposalID: req.ProposalID, RequestedBy: caller.UserID, RequestHash: req.hash(), LedgerID: req.LedgerID,
		BankAccount: req.BankAccount, ControlAccount: req.ControlAccount, DiscountAccount: req.DiscountAccount, FileRequest: fileRequest,
	})
	switch {
	case errors.Is(err, store.ErrProposalNotFound):
		a.problem(w, r, http.StatusNotFound, codeNotFound, "payment proposal not found")
	case errors.Is(err, store.ErrNotApproved):
		a.problem(w, r, http.StatusConflict, domain.CodeProposalState, "payment proposal is not approved")
	case errors.Is(err, store.ErrRunConflict):
		a.problem(w, r, http.StatusConflict, domain.CodeRunConflict, err.Error())
	case errors.Is(err, store.ErrUnsupportedMethod), errors.Is(err, store.ErrDiscountAccount):
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

func (a *API) runResult(w http.ResponseWriter, r *http.Request, msg string, run store.PaymentRun, err error) {
	switch {
	case errors.Is(err, store.ErrRunNotFound):
		a.problem(w, r, http.StatusNotFound, codeNotFound, "payment run not found")
	case errors.Is(err, store.ErrRunState), errors.Is(err, store.ErrJournalPosted):
		a.problem(w, r, http.StatusConflict, domain.CodeRunState, err.Error())
	case errors.Is(err, store.ErrRunOutOfOrder):
		a.problem(w, r, http.StatusConflict, domain.CodeRunOutOfOrder, err.Error())
	case errors.Is(err, store.ErrVersionConflict), errors.Is(err, store.ErrOverSettled), errors.Is(err, store.ErrNotFound):
		a.problem(w, r, http.StatusConflict, domain.CodeRunItemChanged, "an open item changed since the proposal was approved: "+err.Error())
	case err != nil:
		a.fault(w, r, "payment run "+msg+" failed", err)
	default:
		a.runLog(r, "payment run "+msg, run)
		httpx.JSON(w, http.StatusOK, run)
	}
}

func (a *API) runStep(name string, do func(ctx context.Context, s RunStore, id string) (store.PaymentRun, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := a.runID(w, r)
		if !ok {
			return
		}
		run, err := do(r.Context(), a.runs.store, id)
		a.runResult(w, r, name, run, err)
	}
}

func (a *API) completeRun(w http.ResponseWriter, r *http.Request) {
	id, ok := a.runID(w, r)
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
	id, ok := a.runID(w, r)
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
	id, ok := a.runID(w, r)
	if !ok {
		return
	}
	content, err := a.runs.store.RunFileContent(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrRunNotFound), err == nil && content == "":
		a.problem(w, r, http.StatusNotFound, codeNotFound, "payment file not found")
	case err != nil:
		a.fault(w, r, "payment file get failed", err)
	default:
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(content))
	}
}

func (a *API) generateRunFile(w http.ResponseWriter, r *http.Request) {
	id, ok := a.runID(w, r)
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
	proposal, err := a.runs.store.GetProposal(ctx, run.ProposalID)
	if err != nil {
		a.fault(w, r, "payment proposal get failed", err)
		return
	}
	numbers, err := a.runs.store.InvoiceNumbers(ctx, run.ProposalID)
	if err != nil {
		a.fault(w, r, "invoice numbers failed", err)
		return
	}
	var cfg domain.RunFileConfig
	if err := json.Unmarshal(run.FileRequest, &cfg); err != nil {
		a.fault(w, r, "stored file request unreadable", err)
		return
	}
	runDate, err := time.Parse(time.DateOnly, proposal.RunDate)
	if err != nil {
		a.fault(w, r, "proposal run date unreadable", err)
		return
	}
	file, payments, err := domain.BuildRunFile(cfg, run.ID, runDate, a.now(), proposal.Groups, numbers)
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
	id, ok := a.runID(w, r)
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
		a.problem(w, r, http.StatusServiceUnavailable, codeRatesUnconfigured, "ledger is not configured")
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
	entryID, err := a.runs.ledger.Post(ctx, ledger.Entry{
		LedgerID: run.LedgerID, DocumentID: run.ID, PostingDate: proposal.RunDate, Currency: proposal.LegalEntityCurrency,
		IdempotencyKey: runKeyPrefix + run.ID, SourceType: runSourceType,
		Lines: domain.RunJournal(proposal.LegalEntityCurrency, proposal.Groups, run.ControlAccount, run.BankAccount, discountAccount),
	})
	if err != nil {
		a.runLedgerFailure(w, r, err)
		return
	}
	recorded, err := a.runs.store.RecordJournal(ctx, id, entryID)
	a.runResult(w, r, "post-journal", recorded, err)
}

func (a *API) runLedgerFailure(w http.ResponseWriter, r *http.Request, err error) {
	var se *ledger.StatusError
	if errors.As(err, &se) && se.Status >= 400 && se.Status < 500 && se.Status != http.StatusRequestTimeout && se.Status != http.StatusTooManyRequests {
		a.problem(w, r, http.StatusUnprocessableEntity, domain.CodeRunLedger, fmt.Sprintf("the ledger rejected the request with status %d", se.Status))
		return
	}
	a.runs.ledgerFaults.Add(1)
	slog.Error("ledger unavailable", "err", err, "correlationId", middleware.GetReqID(r.Context()))
	fail(w, r, httpx.NewProblem(http.StatusBadGateway, "COM-0502", "ledger unavailable"))
}

func (a *API) writeRunMetrics(w http.ResponseWriter) {
	if a.runs == nil {
		return
	}
	fmt.Fprintf(w, "# HELP payables_payment_runs_total Payment run commands by outcome.\n# TYPE payables_payment_runs_total counter\npayables_payment_runs_total{outcome=\"started\"} %d\npayables_payment_runs_total{outcome=\"replayed\"} %d\npayables_payment_runs_total{outcome=\"completed\"} %d\npayables_payment_runs_total{outcome=\"failed\"} %d\n",
		a.runs.started.Load(), a.runs.replayed.Load(), a.runs.completed.Load(), a.runs.failed.Load())
	fmt.Fprintf(w, "# HELP payables_payment_run_ledger_faults_total Payment run ledger calls that failed on a server or transport fault.\n# TYPE payables_payment_run_ledger_faults_total counter\npayables_payment_run_ledger_faults_total %d\n", a.runs.ledgerFaults.Load())
}
