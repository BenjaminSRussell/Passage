package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/payroll/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payroll/internal/store"
)

const (
	dutyFinalize     = "payroll.run.finalize"
	dutyPost         = "payroll.run.post"
	dutyRead         = "payroll.run.read"
	dutyProfile      = "payroll.posting-profile.maintain"
	maxProfileRoles  = 200
	maxRunStatements = 500
	maxRunBody       = 8 << 20
	dateLayout       = "2006-01-02"

	CodeRunsUnavailable = "PAY-RUN-009"
	CodeGLUnavailable   = "PAY-GL-002"
)

// Runs is the pay run store.
type Runs interface {
	CreateRun(ctx context.Context, in store.NewRun) (store.Run, error)
	GetRun(ctx context.Context, id string) (store.Run, error)
	Advance(ctx context.Context, id string, to domain.RunStatus) (store.Run, error)
	PostRun(ctx context.Context, ledger ledgerclient.Ledger, id, ledgerID string) (store.Run, bool, error)
	Reconcile(ctx context.Context, ledger ledgerclient.Ledger, legalEntityID, ledgerID string) (store.Reconciliation, error)
	GetPostingProfile(ctx context.Context, legalEntityID string) (map[string]string, error)
	SetPostingProfile(ctx context.Context, legalEntityID string, accounts map[string]string) (map[string]string, error)
}

var (
	roleName    = regexp.MustCompile(`^PAYROLL_[A-Z0-9_]{1,80}$`)
	accountName = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
)

// WithRuns enables stored pay runs; ledger is the general ledger the runs post to and reconcile with (nil keeps posting off).
func (a *API) WithRuns(runs Runs, ledger ledgerclient.Ledger) *API {
	a.runs, a.ledger = runs, ledger
	return a
}

func (a *API) registerRuns(r chi.Router) {
	r.With(authz.RequireDuty(dutyCalculate)).Post("/v1/payroll/runs", a.createRun)
	r.With(authz.RequireDuty(dutyCalculate)).Post("/v1/payroll/runs/{id}/review", a.advance(domain.RunReviewed))
	r.With(authz.RequireDuty(dutyFinalize)).Post("/v1/payroll/runs/{id}/finalize", a.advance(domain.RunFinalized))
	r.With(authz.RequireDuty(dutyPost)).Post("/v1/payroll/runs/{id}/post", a.postRun)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/payroll/runs/{id}", a.getRun)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/payroll/gl-reconciliation", a.reconcile)
	r.With(authz.RequireDuty(dutyProfile)).Put("/v1/payroll/posting-profiles/{legalEntityId}", a.setProfile)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/payroll/posting-profiles/{legalEntityId}", a.getProfile)
}

type runRequest struct {
	LegalEntityID string               `json:"legalEntityId"`
	PayDate       string               `json:"payDate"`
	PeriodStart   string               `json:"periodStart"`
	PeriodEnd     string               `json:"periodEnd"`
	Statements    []calculationRequest `json:"statements"`
}

type runStatementView struct {
	WorkerID     string `json:"workerId"`
	RuleSet      string `json:"ruleSet"`
	Hash         string `json:"hash"`
	Gross        string `json:"gross"`
	Net          string `json:"net"`
	EmployerCost string `json:"employerCost"`
}

type runView struct {
	ID             string             `json:"id"`
	LegalEntityID  string             `json:"legalEntityId"`
	PayDate        string             `json:"payDate"`
	PeriodStart    string             `json:"periodStart"`
	PeriodEnd      string             `json:"periodEnd"`
	Status         string             `json:"status"`
	CalculatedBy   string             `json:"calculatedBy"`
	FinalizedBy    string             `json:"finalizedBy,omitempty"`
	LedgerID       string             `json:"ledgerId,omitempty"`
	JournalEntryID string             `json:"journalEntryId,omitempty"`
	Currency       string             `json:"currency"`
	Gross          string             `json:"gross"`
	Net            string             `json:"net"`
	EmployerCost   string             `json:"employerCost"`
	Statements     []runStatementView `json:"statements"`
}

func toRunView(run store.Run) runView {
	gross, net, employer := run.Totals()
	v := runView{ID: run.ID, LegalEntityID: run.LegalEntityID, PayDate: run.PayDate, PeriodStart: run.PeriodStart, PeriodEnd: run.PeriodEnd, Status: string(run.Status),
		CalculatedBy: run.CalculatedBy, FinalizedBy: run.FinalizedBy, LedgerID: run.LedgerID, JournalEntryID: run.JournalEntryID, Currency: store.Currency,
		Gross: cents(gross), Net: cents(net), EmployerCost: cents(employer), Statements: make([]runStatementView, 0, len(run.Statements))}
	for _, s := range run.Statements {
		v.Statements = append(v.Statements, runStatementView{WorkerID: s.WorkerID, RuleSet: s.RuleSet, Hash: s.Hash, Gross: cents(s.Gross), Net: cents(s.Net), EmployerCost: cents(s.EmployerCost)})
	}
	return v
}

func (a *API) runsReady(w http.ResponseWriter, r *http.Request) bool {
	if a.runs == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeRunsUnavailable, "pay runs are not configured for this service"))
		return false
	}
	return true
}

func (a *API) glReady(w http.ResponseWriter, r *http.Request) bool {
	if !a.runsReady(w, r) {
		return false
	}
	if a.ledger == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeGLUnavailable, "the general ledger is not configured for this service"))
		return false
	}
	return true
}

func inScope(w http.ResponseWriter, r *http.Request, legalEntityID string) bool {
	p, _ := tenant.FromContext(r.Context())
	if !tenant.InLegalEntityScope(p, legalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the legal entity is outside the caller's scope"))
		return false
	}
	return true
}

func writeValidation(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

func fail(w http.ResponseWriter, r *http.Request, err error) {
	var de *domain.Error
	if errors.As(err, &de) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail))
		return
	}
	httpx.Fail(w, r, err)
}

func validDate(fields map[string]string, name, v string) {
	if _, err := time.Parse(dateLayout, v); err != nil {
		fields[name] = "must be a date as YYYY-MM-DD"
	}
}

func (a *API) createRun(w http.ResponseWriter, r *http.Request) {
	if !a.runsReady(w, r) {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	if !ids.Valid(p.TenantID) || p.UserID == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant or user"))
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 128 {
		writeValidation(w, r, map[string]string{"Idempotency-Key": "is required and at most 128 characters"})
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRunBody))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusRequestEntityTooLarge, "COM-0413", "request body exceeds the size limit"))
		return
	}
	var req runRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "malformed JSON body: "+err.Error()))
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	validDate(fields, "payDate", req.PayDate)
	validDate(fields, "periodStart", req.PeriodStart)
	validDate(fields, "periodEnd", req.PeriodEnd)
	if req.PeriodEnd < req.PeriodStart {
		fields["periodEnd"] = "must not be before periodStart"
	}
	if n := len(req.Statements); n == 0 || n > maxRunStatements {
		fields["statements"] = fmt.Sprintf("between 1 and %d statements are required", maxRunStatements)
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	if !inScope(w, r, req.LegalEntityID) {
		return
	}
	if req.PayDate < req.PeriodStart {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeInvalidInput, "payDate must not be before periodStart"))
		return
	}
	canonical, err := json.Marshal(req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	hash := sha256.Sum256(canonical)
	in := store.NewRun{LegalEntityID: req.LegalEntityID, PayDate: req.PayDate, PeriodStart: req.PeriodStart, PeriodEnd: req.PeriodEnd, Key: key, RequestHash: hex.EncodeToString(hash[:])}
	seen := map[string]bool{}
	for i, sr := range req.Statements {
		prefix := fmt.Sprintf("statements[%d].", i)
		item := map[string]string{}
		calc := toInput(sr, item)
		if !ids.Valid(sr.WorkerID) {
			item["workerId"] = "is required and must be a UUID"
		} else if seen[sr.WorkerID] {
			item["workerId"] = "appears twice in the run"
		}
		seen[sr.WorkerID] = true
		for k, v := range item {
			fields[prefix+k] = v
		}
		if len(item) > 0 {
			continue
		}
		pack, ok := domain.PackFor(sr.Country, sr.TaxYear)
		if !ok {
			a.failed.Add(1)
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeNoRule, fmt.Sprintf("no published rule set for %s %d", sr.Country, sr.TaxYear)))
			return
		}
		st, err := domain.Calculate(pack, calc)
		if err != nil {
			a.failed.Add(1)
			fail(w, r, err)
			return
		}
		h, err := st.Hash()
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		in.Statements = append(in.Statements, store.NewStatement{WorkerID: sr.WorkerID, RuleSet: pack.Code, Hash: h, Statement: st})
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	run, err := a.runs.CreateRun(r.Context(), in)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.runsCreated.Add(1)
	httpx.JSON(w, http.StatusCreated, toRunView(run))
}

func (a *API) advance(to domain.RunStatus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.runsReady(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			writeValidation(w, r, map[string]string{"id": "must be a UUID"})
			return
		}
		run, err := a.runs.Advance(r.Context(), id, to)
		if err != nil {
			fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toRunView(run))
	}
}

func (a *API) getRun(w http.ResponseWriter, r *http.Request) {
	if !a.runsReady(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		writeValidation(w, r, map[string]string{"id": "must be a UUID"})
		return
	}
	run, err := a.runs.GetRun(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toRunView(run))
}

type postRequest struct {
	LedgerID string `json:"ledgerId"`
}

func (a *API) postRun(w http.ResponseWriter, r *http.Request) {
	if !a.glReady(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	var req postRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(id) {
		fields["id"] = "must be a UUID"
	}
	ledgerID, ok := ids.Canonical(req.LedgerID)
	if !ok {
		fields["ledgerId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	run, transitioned, err := a.runs.PostRun(r.Context(), a.ledger, id, ledgerID)
	if err != nil {
		fail(w, r, err)
		return
	}
	if transitioned {
		a.runsPosted.Add(1)
	}
	httpx.JSON(w, http.StatusOK, toRunView(run))
}

type reconAccountView struct {
	Account    string `json:"account"`
	Register   string `json:"register"`
	GL         string `json:"gl"`
	Difference string `json:"difference"`
}

type reconView struct {
	LegalEntityID string             `json:"legalEntityId"`
	Currency      string             `json:"currency"`
	PostedRuns    int                `json:"postedRuns"`
	Reconciled    bool               `json:"reconciled"`
	Accounts      []reconAccountView `json:"accounts"`
}

func (a *API) reconcile(w http.ResponseWriter, r *http.Request) {
	if !a.glReady(w, r) {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	for name, v := range map[string]string{"legalEntityId": q.Get("legalEntityId"), "ledgerId": q.Get("ledgerId")} {
		if !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	if !inScope(w, r, q.Get("legalEntityId")) {
		return
	}
	rec, err := a.runs.Reconcile(r.Context(), a.ledger, q.Get("legalEntityId"), q.Get("ledgerId"))
	if err != nil {
		fail(w, r, err)
		return
	}
	v := reconView{LegalEntityID: q.Get("legalEntityId"), Currency: store.Currency, PostedRuns: rec.Runs, Reconciled: rec.Reconciled, Accounts: make([]reconAccountView, 0, len(rec.Accounts))}
	for _, x := range rec.Accounts {
		v.Accounts = append(v.Accounts, reconAccountView{Account: x.Account, Register: cents(x.Register), GL: cents(x.GL), Difference: cents(x.Difference)})
	}
	httpx.JSON(w, http.StatusOK, v)
}

type profileRequest struct {
	Accounts map[string]string `json:"accounts"`
}

type profileView struct {
	LegalEntityID string            `json:"legalEntityId"`
	Accounts      map[string]string `json:"accounts"`
}

func (a *API) setProfile(w http.ResponseWriter, r *http.Request) {
	if !a.runsReady(w, r) {
		return
	}
	le := chi.URLParam(r, "legalEntityId")
	var req profileRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(le) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if n := len(req.Accounts); n == 0 || n > maxProfileRoles {
		fields["accounts"] = fmt.Sprintf("between 1 and %d role to account mappings are required", maxProfileRoles)
	}
	for role, account := range req.Accounts {
		if !roleName.MatchString(role) {
			fields["accounts."+role] = "the role must look like PAYROLL_<NAME>"
		} else if !accountName.MatchString(account) {
			fields["accounts."+role] = "the account must be 1 to 64 letters, digits or . _ : -"
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	if !inScope(w, r, le) {
		return
	}
	profile, err := a.runs.SetPostingProfile(r.Context(), le, req.Accounts)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, profileView{LegalEntityID: le, Accounts: profile})
}

func (a *API) getProfile(w http.ResponseWriter, r *http.Request) {
	if !a.runsReady(w, r) {
		return
	}
	le := chi.URLParam(r, "legalEntityId")
	if !ids.Valid(le) {
		writeValidation(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	if !inScope(w, r, le) {
		return
	}
	profile, err := a.runs.GetPostingProfile(r.Context(), le)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, profileView{LegalEntityID: le, Accounts: profile})
}
