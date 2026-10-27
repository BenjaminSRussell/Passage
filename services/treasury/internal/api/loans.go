package api

import (
	"net/http"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/domain"
	"github.com/BenjaminSRussell/passage/services/treasury/internal/store"
)

type createLoanRequest struct {
	LegalEntityID    string `json:"legalEntityId"`
	Role             string `json:"role"`
	CounterpartyName string `json:"counterpartyName"`
	Principal        string `json:"principal"`
	Currency         string `json:"currency"`
	StartDate        string `json:"startDate"`
	MaturityDate     string `json:"maturityDate"`
	ScheduleType     string `json:"scheduleType"`
	FixedRate        string `json:"fixedRate"`
	DayCount         string `json:"dayCount"`
	PeriodMonths     int    `json:"periodMonths"`
}

// createLoan records a loan contract and generates its schedule (FR-36.19).
// The response carries the schedule; the same terms always produce the same lines.
func (a *API) createLoan(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req createLoanRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if req.Role != "borrower" && req.Role != "lender" {
		fields["role"] = "borrower or lender"
	}
	counterparty := strings.TrimSpace(req.CounterpartyName)
	if n := len(counterparty); n < 1 || n > 140 {
		fields["counterpartyName"] = "1 to 140 characters"
	}
	principal, ok := parseAmount(req.Principal, req.Currency)
	if !ok {
		fields["principal"] = "must be a positive decimal string with at most the currency's minor-unit places"
	}
	if !domain.ValidCurrency(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	start, err := domain.Day(req.StartDate)
	if err != nil {
		fields["startDate"] = "must be a YYYY-MM-DD date"
	}
	maturity, err := domain.Day(req.MaturityDate)
	if err != nil {
		fields["maturityDate"] = "must be a YYYY-MM-DD date"
	}
	if !domain.ValidScheduleType(domain.ScheduleType(req.ScheduleType)) {
		fields["scheduleType"] = "one of Annuity, Linear, Bullet"
	}
	// numeric(9,6): a fraction between 0 and 1 with at most six places (0.031250 = 3.125%).
	rate, err := decimal.NewFromString(req.FixedRate)
	if err != nil || rate.IsNegative() || rate.GreaterThan(decimal.NewFromInt(1)) || !rate.Equal(rate.Round(6)) {
		fields["fixedRate"] = "must be a decimal fraction from 0 to 1 with at most six places, e.g. 0.03 for 3.00%"
	}
	if !domain.ValidDayCount(domain.DayCount(req.DayCount)) {
		fields["dayCount"] = "one of ACT/360, ACT/365, 30/360"
	}
	if !domain.ValidPeriodMonths(req.PeriodMonths) {
		fields["periodMonths"] = "one of 1, 3, 6, 12"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if outsideScope(w, r, req.LegalEntityID) {
		return
	}
	schedule, err := domain.BuildSchedule(domain.LoanTerms{
		Principal: principal, Currency: req.Currency, Start: start, Maturity: maturity,
		ScheduleType: domain.ScheduleType(req.ScheduleType), Rate: rate,
		DayCount: domain.DayCount(req.DayCount), PeriodMonths: req.PeriodMonths,
	})
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	loan, err := a.store.CreateLoan(r.Context(), store.NewLoan{
		LegalEntityID: req.LegalEntityID, Role: req.Role, CounterpartyName: counterparty,
		Principal: principal.String(), Currency: req.Currency, StartDate: req.StartDate, MaturityDate: req.MaturityDate,
		ScheduleType: req.ScheduleType, FixedRate: rate.String(), DayCount: req.DayCount,
		PeriodMonths: req.PeriodMonths, Schedule: schedule,
	}, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.loansCreated.Add(1)
	setETag(w, loan.Version)
	httpx.JSON(w, http.StatusCreated, loan)
}

func (a *API) listLoans(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "required UUID query parameter"})
		return
	}
	items, err := a.store.ListLoans(r.Context(), le)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getLoan(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeLoanNotFound, "loan contract not found")
	if !ok {
		return
	}
	loan, err := a.store.GetLoan(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, loan.Version)
	httpx.JSON(w, http.StatusOK, loan)
}
