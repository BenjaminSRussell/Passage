// Package api exposes the payroll engine over HTTP. This slice serves the
// stateless calculation preview; run persistence arrives in a later slice.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/payroll/internal/domain"
)

const (
	readyTimeout     = 500 * time.Millisecond
	dutyCalculate    = "payroll.run.maintain"
	validationDetail = "request failed validation"
	maxAmount        = 1_000_000_000_000
	maxCodeLength    = 32
)

// Pinger is the database readiness probe (satisfied by *pgxpool.Pool).
type Pinger interface {
	Ping(ctx context.Context) error
}

// API holds the handlers and their counters.
type API struct {
	db           Pinger
	runs         Runs
	ledger       ledgerclient.Ledger
	calculations atomic.Int64
	failed       atomic.Int64
	runsCreated  atomic.Int64
	runsPosted   atomic.Int64
}

// New returns the API. db may be nil in tests; readiness then reports ready.
func New(db Pinger) *API { return &API{db: db} }

// Register mounts the routes on r.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyCalculate)).Post("/v1/payroll/calculations", a.calculate)
	a.registerRuns(r)
}

// Metrics serves Prometheus text on the admin port.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "# HELP payroll_calculations_total Gross-to-net previews computed.\n# TYPE payroll_calculations_total counter\npayroll_calculations_total %d\n", a.calculations.Load())
		fmt.Fprintf(w, "# HELP payroll_calculation_failures_total Gross-to-net previews refused.\n# TYPE payroll_calculation_failures_total counter\npayroll_calculation_failures_total %d\n", a.failed.Load())
		fmt.Fprintf(w, "# HELP payroll_runs_created_total Pay runs stored.\n# TYPE payroll_runs_created_total counter\npayroll_runs_created_total %d\n", a.runsCreated.Load())
		fmt.Fprintf(w, "# HELP payroll_runs_posted_total Pay runs posted to the general ledger.\n# TYPE payroll_runs_posted_total counter\npayroll_runs_posted_total %d\n", a.runsPosted.Load())
	})
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	if a.db != nil {
		ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
		defer cancel()
		if err := a.db.Ping(ctx); err != nil {
			slog.Error("readiness check failed", "err", err)
			httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

type amountDTO struct {
	Code   string `json:"code"`
	Amount string `json:"amount"`
}

type preTaxDTO struct {
	Code        string `json:"code"`
	Amount      string `json:"amount"`
	ReducesFICA bool   `json:"reducesFica"`
}

type w4DTO struct {
	FilingStatus     string `json:"filingStatus"`
	Legacy           bool   `json:"legacy"`
	Allowances       int    `json:"allowances"`
	MultipleJobs     bool   `json:"multipleJobs"`
	DependentsCredit string `json:"dependentsCredit"`
	OtherIncome      string `json:"otherIncome"`
	Deductions       string `json:"deductions"`
	ExtraWithholding string `json:"extraWithholding"`
}

type deductionDTO struct {
	Code             string `json:"code"`
	Amount           string `json:"amount"`
	Priority         int    `json:"priority"`
	ArrearsPolicy    string `json:"arrearsPolicy"`
	ArrearsBalance   string `json:"arrearsBalance"`
	RecoveryLimitPct string `json:"recoveryLimitPct"`
}

type matchDTO struct {
	Code    string `json:"code"`
	Percent string `json:"percent"`
	CapRate string `json:"capRate"`
}

type calculationRequest struct {
	WorkerID       string         `json:"workerId"`
	RunType        string         `json:"runType"`
	Country        string         `json:"country"`
	TaxYear        int            `json:"taxYear"`
	PeriodsPerYear int            `json:"periodsPerYear"`
	W4             w4DTO          `json:"w4"`
	State          string         `json:"state"`
	Earnings       []amountDTO    `json:"earnings"`
	PreTax         []preTaxDTO    `json:"preTax"`
	PostTax        []deductionDTO `json:"postTax"`
	EmployerMatch  *matchDTO      `json:"employerMatch"`
	YTD            struct {
		FICAWages         string `json:"ficaWages"`
		FUTAWages         string `json:"futaWages"`
		SupplementalWages string `json:"supplementalWages"`
	} `json:"ytd"`
}

type bracketDTO struct {
	Schedule           string `json:"schedule"`
	FilingStatus       string `json:"filingStatus"`
	Over               string `json:"over"`
	Rate               string `json:"rate"`
	BaseTax            string `json:"baseTax"`
	AdjustedAnnualWage string `json:"adjustedAnnualWage"`
}

type lineDTO struct {
	Code        string      `json:"code"`
	Kind        string      `json:"kind"`
	Amount      string      `json:"amount"`
	BasisAmount string      `json:"basisAmount"`
	RuleVersion string      `json:"ruleVersion"`
	Bracket     *bracketDTO `json:"bracket,omitempty"`
}

type arrearsDTO struct {
	Code      string `json:"code"`
	Created   string `json:"created"`
	Recovered string `json:"recovered"`
	Balance   string `json:"balance"`
}

type calculationResponse struct {
	WorkerID        string       `json:"workerId,omitempty"`
	RuleSet         string       `json:"ruleSet"`
	Hash            string       `json:"hash"`
	Gross           string       `json:"gross"`
	TaxableWagesFed string       `json:"taxableWagesFed"`
	FICAWages       string       `json:"ficaWages"`
	Net             string       `json:"net"`
	EmployerCost    string       `json:"employerCost"`
	Lines           []lineDTO    `json:"lines"`
	Arrears         []arrearsDTO `json:"arrears"`
}

// cents renders a money amount with exactly two decimals.
func cents(d decimal.Decimal) string { return d.StringFixed(2) }

func toResponse(workerID, ruleSet, hash string, st domain.Statement) calculationResponse {
	resp := calculationResponse{
		WorkerID: workerID, RuleSet: ruleSet, Hash: hash,
		Gross: cents(st.Gross), TaxableWagesFed: cents(st.FedTaxableWages), FICAWages: cents(st.FICAWages),
		Net: cents(st.Net), EmployerCost: cents(st.EmployerCost),
		Lines: make([]lineDTO, 0, len(st.Lines)), Arrears: make([]arrearsDTO, 0, len(st.Arrears)),
	}
	for _, l := range st.Lines {
		line := lineDTO{Code: l.Code, Kind: l.Kind, Amount: cents(l.Amount), BasisAmount: cents(l.BasisAmount), RuleVersion: l.RuleVersion}
		if b := l.Bracket; b != nil {
			line.Bracket = &bracketDTO{Schedule: b.Schedule, FilingStatus: b.FilingStatus, Over: cents(b.Over),
				Rate: b.Rate.String(), BaseTax: cents(b.BaseTax), AdjustedAnnualWage: cents(b.AdjustedWage)}
		}
		resp.Lines = append(resp.Lines, line)
	}
	for _, e := range st.Arrears {
		resp.Arrears = append(resp.Arrears, arrearsDTO{Code: e.Code, Created: cents(e.Created), Recovered: cents(e.Recovered), Balance: cents(e.Balance)})
	}
	return resp
}

func validRunType(s string) bool { return s == domain.RunRegular || s == domain.RunBonus }

func validPeriods(n int) bool { return n == 12 || n == 24 || n == 26 || n == 52 }

func validFilingStatus(s string, legacy bool) bool {
	switch s {
	case domain.FilingSingle, domain.FilingMarriedFilingJointly:
		return true
	case domain.FilingMarriedFilingSeparately, domain.FilingHeadOfHousehold:
		return !legacy
	}
	return false
}

func parsePolicy(s string) (domain.ArrearsPolicy, bool) {
	switch s {
	case string(domain.PolicyArrears), string(domain.PolicyNoArrears), string(domain.PolicyPartial):
		return domain.ArrearsPolicy(s), true
	}
	return "", false
}

// calculate serves the gross-to-net preview. It writes nothing, so it takes no
// Idempotency-Key; run creation and calculation arrive with persistence.
func (a *API) calculate(w http.ResponseWriter, r *http.Request) {
	if p, _ := tenant.FromContext(r.Context()); !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return
	}
	var req calculationRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	in := toInput(req, fields)
	if len(fields) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
		p.Errors = fields
		httpx.Fail(w, r, p)
		return
	}
	if req.WorkerID != "" && !ids.Valid(req.WorkerID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "workerId must be a UUID when given"))
		return
	}
	pack, ok := domain.PackFor(req.Country, req.TaxYear)
	if !ok {
		a.failed.Add(1)
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeNoRule,
			fmt.Sprintf("no published rule set for %s %d", req.Country, req.TaxYear)))
		return
	}
	st, err := domain.Calculate(pack, in)
	if err != nil {
		a.failed.Add(1)
		var de *domain.Error
		if errors.As(err, &de) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail))
			return
		}
		httpx.Fail(w, r, err)
		return
	}
	hash, err := st.Hash()
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.calculations.Add(1)
	httpx.JSON(w, http.StatusOK, toResponse(req.WorkerID, pack.Code, hash, st))
}

// toInput validates the DTO and converts it to the domain input. Problems are
// recorded in fields by JSON path; the returned input is only valid when fields is empty.
func toInput(req calculationRequest, fields map[string]string) domain.Input {
	var in domain.Input
	if !validRunType(req.RunType) {
		fields["runType"] = "must be REGULAR or OFF_CYCLE_BONUS"
	}
	if req.Country == "" {
		fields["country"] = "is required"
	}
	if !validPeriods(req.PeriodsPerYear) {
		fields["periodsPerYear"] = "must be 12, 24, 26 or 52"
	}
	if !validFilingStatus(req.W4.FilingStatus, req.W4.Legacy) {
		fields["w4.filingStatus"] = "must be SINGLE, MARRIED_FILING_JOINTLY, MARRIED_FILING_SEPARATELY or HEAD_OF_HOUSEHOLD (SINGLE or MARRIED_FILING_JOINTLY on a pre-2020 W-4)"
	}
	if req.W4.Allowances < 0 || req.W4.Allowances > 99 || (req.W4.Allowances > 0 && !req.W4.Legacy) {
		fields["w4.allowances"] = "must be 0 to 99, and only on a pre-2020 (legacy) W-4"
	}
	if req.State != "" && (len(req.State) != 2 || req.State != strings.ToUpper(req.State)) {
		fields["state"] = "must be a two-letter upper-case state code"
	}
	if len(req.Earnings) == 0 {
		fields["earnings"] = "at least one earning is required"
	}
	in.RunType, in.Periods, in.State = req.RunType, req.PeriodsPerYear, req.State
	in.W4 = domain.W4{
		FilingStatus:     req.W4.FilingStatus,
		Legacy:           req.W4.Legacy,
		Allowances:       req.W4.Allowances,
		MultipleJobs:     req.W4.MultipleJobs,
		DependentsCredit: money(fields, "w4.dependentsCredit", req.W4.DependentsCredit),
		OtherIncome:      money(fields, "w4.otherIncome", req.W4.OtherIncome),
		Deductions:       money(fields, "w4.deductions", req.W4.Deductions),
		ExtraWithholding: money(fields, "w4.extraWithholding", req.W4.ExtraWithholding),
	}
	gross, preTotal := decimal.Zero, decimal.Zero
	for i, e := range req.Earnings {
		earning := domain.Earning{Code: code(fields, fmt.Sprintf("earnings[%d].code", i), e.Code), Amount: money(fields, fmt.Sprintf("earnings[%d].amount", i), e.Amount)}
		gross = gross.Add(earning.Amount)
		in.Earnings = append(in.Earnings, earning)
	}
	for i, d := range req.PreTax {
		ded := domain.Deduction{Code: code(fields, fmt.Sprintf("preTax[%d].code", i), d.Code), Amount: money(fields, fmt.Sprintf("preTax[%d].amount", i), d.Amount), ReducesFICA: d.ReducesFICA}
		preTotal = preTotal.Add(ded.Amount)
		in.PreTax = append(in.PreTax, ded)
	}
	if preTotal.GreaterThan(gross) {
		fields["preTax"] = "pre-tax deductions must not exceed gross earnings"
	}
	for i, d := range req.PostTax {
		prefix := fmt.Sprintf("postTax[%d].", i)
		policy, ok := parsePolicy(d.ArrearsPolicy)
		if !ok {
			fields[prefix+"arrearsPolicy"] = "must be ARREARS, NO_ARREARS or PARTIAL"
		}
		in.PostTax = append(in.PostTax, domain.Deduction{
			Code:             code(fields, prefix+"code", d.Code),
			Amount:           money(fields, prefix+"amount", d.Amount),
			Priority:         d.Priority,
			Policy:           policy,
			ArrearsBalance:   money(fields, prefix+"arrearsBalance", d.ArrearsBalance),
			RecoveryLimitPct: fraction(fields, prefix+"recoveryLimitPct", d.RecoveryLimitPct, true),
		})
	}
	if req.EmployerMatch != nil {
		in.Match = &domain.Match{
			Code:    code(fields, "employerMatch.code", req.EmployerMatch.Code),
			Percent: fraction(fields, "employerMatch.percent", req.EmployerMatch.Percent, false),
			CapRate: fraction(fields, "employerMatch.capRate", req.EmployerMatch.CapRate, false),
		}
	}
	in.YTD = domain.YTD{
		FICAWages:         money(fields, "ytd.ficaWages", req.YTD.FICAWages),
		FUTAWages:         money(fields, "ytd.futaWages", req.YTD.FUTAWages),
		SupplementalWages: money(fields, "ytd.supplementalWages", req.YTD.SupplementalWages),
	}
	return in
}

func code(fields map[string]string, field, v string) string {
	if v == "" || len(v) > maxCodeLength {
		fields[field] = fmt.Sprintf("is required and at most %d characters", maxCodeLength)
	}
	return v
}

// money parses a non-negative decimal string; an empty string is zero.
func money(fields map[string]string, field, raw string) decimal.Decimal {
	if raw == "" {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(raw)
	if err != nil || d.IsNegative() || d.GreaterThan(decimal.NewFromInt(maxAmount)) {
		fields[field] = "must be a non-negative decimal amount"
		return decimal.Zero
	}
	return d
}

// fraction parses a rate between 0 and 1 (1 = 100%). Empty is zero when optional
// and otherwise an error; for recovery limits empty means the default 100%.
func fraction(fields map[string]string, field, raw string, defaultFull bool) decimal.Decimal {
	if raw == "" {
		if defaultFull {
			return decimal.NewFromInt(1)
		}
		fields[field] = "is required"
		return decimal.Zero
	}
	d, err := decimal.NewFromString(raw)
	if err != nil || d.IsNegative() || d.GreaterThan(decimal.NewFromInt(5)) {
		fields[field] = "must be a decimal fraction from 0 to 5"
		return decimal.Zero
	}
	return d
}
