// Package api exposes the expense rules over HTTP. This slice serves stateless
// previews: report policy evaluation, advance settlement and travel variance.
// Report persistence, receipts, card import and AP posting arrive in later slices.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/expense/internal/domain"
)

const (
	readyTimeout     = 500 * time.Millisecond
	dutyExpense      = "expense.report.write"
	validationDetail = "request failed validation"
	dateLayout       = "2006-01-02"
	maxLines         = 500
	maxAdvances      = 50
)

// Pinger is the database readiness probe (satisfied by *pgxpool.Pool).
type Pinger interface {
	Ping(ctx context.Context) error
}

// API holds handlers and counters.
type API struct {
	db          Pinger
	evaluations atomic.Int64
	settlements atomic.Int64
	variances   atomic.Int64
}

// New returns the API. db may be nil in tests.
func New(db Pinger) *API { return &API{db: db} }

// Register mounts the routes on r.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	// Stateless previews: nothing is written, so no Idempotency-Key is taken.
	duty := authz.RequireDuty(dutyExpense)
	r.With(duty).Post("/v1/expense/report-evaluations", a.evaluate)
	r.With(duty).Post("/v1/expense/cash-advance-settlements", a.settle)
	r.With(duty).Post("/v1/expense/travel-variances", a.variance)
}

// Metrics serves Prometheus text on the admin port.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "# HELP expense_report_evaluations_total Policy evaluations served.\n# TYPE expense_report_evaluations_total counter\nexpense_report_evaluations_total %d\n", a.evaluations.Load())
		fmt.Fprintf(w, "# HELP expense_advance_settlements_total Advance settlement previews served.\n# TYPE expense_advance_settlements_total counter\nexpense_advance_settlements_total %d\n", a.settlements.Load())
		fmt.Fprintf(w, "# HELP expense_travel_variances_total Requisition variance checks served.\n# TYPE expense_travel_variances_total counter\nexpense_travel_variances_total %d\n", a.variances.Load())
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

type lineDTO struct {
	ID            string `json:"id"`
	Category      string `json:"category"`
	TxnDate       string `json:"txnDate"`
	Amount        string `json:"amount"`
	Merchant      string `json:"merchant"`
	Attendees     int    `json:"attendees"`
	HasReceipt    bool   `json:"hasReceipt"`
	Justification string `json:"justification"`
	PaidBy        string `json:"paidBy"`
}

type evaluationRequest struct {
	SubmitDate string    `json:"submitDate"`
	Lines      []lineDTO `json:"lines"`
}

type evaluationResponse struct {
	Submittable bool            `json:"submittable"`
	Results     []domain.Result `json:"results"`
	Totals      totalsDTO       `json:"totals"`
}

type totalsDTO struct {
	Total        string `json:"total"`
	Reimbursable string `json:"reimbursable"`
	CompanyPaid  string `json:"companyPaid"`
}

func (a *API) evaluate(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) {
		return
	}
	var req evaluationRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	submit := parseDate(fields, "submitDate", req.SubmitDate)
	if len(req.Lines) == 0 || len(req.Lines) > maxLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines", maxLines)
	}
	lines := make([]domain.Line, 0, len(req.Lines))
	for i, l := range req.Lines {
		line, ok := toLine(fields, fmt.Sprintf("lines[%d].", i), l)
		if ok {
			lines = append(lines, line)
		}
	}
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	if dup := duplicateIDs(lines); dup != "" {
		refuse(w, r, map[string]string{"lines": "line id " + dup + " is used more than once"})
		return
	}
	results := domain.EvaluateReport(domain.DefaultRules(), lines, domain.Context{SubmitDate: submit})
	t := domain.SumLines(lines)
	a.evaluations.Add(1)
	httpx.JSON(w, http.StatusOK, evaluationResponse{
		Submittable: domain.Submittable(results),
		Results:     nonNil(results),
		Totals:      totalsDTO{Total: t.Total.StringFixed(2), Reimbursable: t.Reimbursable.StringFixed(2), CompanyPaid: t.CompanyPaid.StringFixed(2)},
	})
}

type advanceDTO struct {
	ID      string `json:"id"`
	PaidOn  string `json:"paidOn"`
	Paid    string `json:"paid"`
	Cleared string `json:"cleared"`
}

type settleRequest struct {
	Advances          []advanceDTO `json:"advances"`
	ReimbursableTotal string       `json:"reimbursableTotal"`
}

type applicationDTO struct {
	AdvanceID string `json:"advanceId"`
	Applied   string `json:"applied"`
	OpenAfter string `json:"openAfter"`
	Status    string `json:"status"`
}

type settleResponse struct {
	Applications    []applicationDTO `json:"applications"`
	Applied         string           `json:"applied"`
	APInvoiceAmount string           `json:"apInvoiceAmount"`
	Code            string           `json:"code,omitempty"`
}

func (a *API) settle(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) {
		return
	}
	var req settleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	reimb := money(fields, "reimbursableTotal", req.ReimbursableTotal)
	if len(req.Advances) > maxAdvances {
		fields["advances"] = fmt.Sprintf("at most %d advances", maxAdvances)
	}
	advances := make([]domain.Advance, 0, len(req.Advances))
	seen := map[string]bool{}
	for i, adv := range req.Advances {
		prefix := fmt.Sprintf("advances[%d].", i)
		if adv.ID == "" || len(adv.ID) > 64 || seen[adv.ID] {
			fields[prefix+"id"] = "is required, unique and at most 64 characters"
		}
		seen[adv.ID] = true
		paid := money(fields, prefix+"paid", adv.Paid)
		cleared := decimal.Zero
		if adv.Cleared != "" {
			cleared = money(fields, prefix+"cleared", adv.Cleared)
		}
		if cleared.GreaterThan(paid) {
			fields[prefix+"cleared"] = "must not exceed paid"
		}
		advances = append(advances, domain.Advance{ID: adv.ID, PaidOn: parseDate(fields, prefix+"paidOn", adv.PaidOn), Paid: paid, Cleared: cleared})
	}
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	s := domain.SettleAdvances(advances, reimb)
	resp := settleResponse{Applications: make([]applicationDTO, 0, len(s.Applications)), Applied: s.Applied.StringFixed(2), APInvoiceAmount: s.APInvoiceAmount.StringFixed(2)}
	for _, app := range s.Applications {
		resp.Applications = append(resp.Applications, applicationDTO{AdvanceID: app.AdvanceID, Applied: app.Applied.StringFixed(2), OpenAfter: app.OpenAfter.StringFixed(2), Status: app.Status})
	}
	if s.Applied.IsPositive() {
		resp.Code = domain.CodeAdvanceApplied
	}
	a.settlements.Add(1)
	httpx.JSON(w, http.StatusOK, resp)
}

type varianceRequest struct {
	EstimatedTotal string `json:"estimatedTotal"`
	ActualTotal    string `json:"actualTotal"`
	TolerancePct   string `json:"tolerancePct"`
}

type varianceResponse struct {
	RequiresJustification bool   `json:"requiresJustification"`
	Code                  string `json:"code,omitempty"`
}

func (a *API) variance(w http.ResponseWriter, r *http.Request) {
	if !a.authorize(w, r) {
		return
	}
	var req varianceRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	estimated := money(fields, "estimatedTotal", req.EstimatedTotal)
	actual := money(fields, "actualTotal", req.ActualTotal)
	tolerance := decimal.RequireFromString("0.10")
	if req.TolerancePct != "" {
		pct, err := decimal.NewFromString(req.TolerancePct)
		if err != nil || pct.IsNegative() || pct.GreaterThan(decimal.NewFromInt(100)) {
			fields["tolerancePct"] = "must be a percentage from 0 to 100"
		} else {
			tolerance = pct.Div(decimal.NewFromInt(100))
		}
	}
	if len(fields) > 0 {
		refuse(w, r, fields)
		return
	}
	a.variances.Add(1)
	over := domain.ExceedsRequisition(estimated, actual, tolerance)
	resp := varianceResponse{RequiresJustification: over}
	if over {
		resp.Code = domain.CodeRequisition
	}
	httpx.JSON(w, http.StatusOK, resp)
}

// authorize rejects a malformed tenant; the duty is enforced by the route's
// authz middleware.
func (a *API) authorize(w http.ResponseWriter, r *http.Request) bool {
	if p, _ := tenant.FromContext(r.Context()); !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	return true
}

// duplicateIDs returns a line ID used more than once, or "".
func duplicateIDs(lines []domain.Line) string {
	seen := map[string]bool{}
	for _, l := range lines {
		if seen[l.ID] {
			return l.ID
		}
		seen[l.ID] = true
	}
	return ""
}

func refuse(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

func toLine(fields map[string]string, prefix string, l lineDTO) (domain.Line, bool) {
	before := len(fields)
	if l.ID == "" || len(l.ID) > 64 {
		fields[prefix+"id"] = "is required and at most 64 characters"
	}
	if l.Category == "" || len(l.Category) > 32 {
		fields[prefix+"category"] = "is required and at most 32 characters"
	}
	if len(l.Merchant) > 200 || len(l.Justification) > 500 {
		fields[prefix+"merchant"] = "merchant is at most 200 characters and justification at most 500"
	}
	if l.Attendees < 0 || l.Attendees > 500 {
		fields[prefix+"attendees"] = "must be 0 to 500"
	}
	if l.PaidBy != "" && l.PaidBy != domain.PaidByEmployee && l.PaidBy != domain.PaidByCompanyCard {
		fields[prefix+"paidBy"] = "must be EMPLOYEE or COMPANY_CARD"
	}
	date := parseDate(fields, prefix+"txnDate", l.TxnDate)
	amount := money(fields, prefix+"amount", l.Amount)
	return domain.Line{
		ID: l.ID, Category: l.Category, TxnDate: date, Amount: amount, Merchant: l.Merchant,
		Attendees: l.Attendees, HasReceipt: l.HasReceipt, Justification: l.Justification, PaidBy: l.PaidBy,
	}, len(fields) == before
}

func parseDate(fields map[string]string, field, raw string) time.Time {
	d, err := time.Parse(dateLayout, raw)
	if err != nil {
		fields[field] = "must be a YYYY-MM-DD date"
		return time.Time{}
	}
	return d
}

// money parses a non-negative decimal amount into the accounting currency.
func money(fields map[string]string, field, raw string) decimal.Decimal {
	d, err := decimal.NewFromString(raw)
	if err != nil || d.IsNegative() || d.GreaterThan(decimal.NewFromInt(1_000_000_000_000)) {
		fields[field] = "must be a non-negative decimal amount"
		return decimal.Zero
	}
	return d
}

func nonNil(rs []domain.Result) []domain.Result {
	if rs == nil {
		return []domain.Result{}
	}
	return rs
}
