package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

type atpPeriodRequest struct {
	Date   string `json:"date"`
	Supply string `json:"supply"`
	Demand string `json:"demand"`
}

// atpCheckRequest places a quantity against caller-supplied ATP buckets. It is
// stateless (PRD §5.27.5); the projection from inventory events is a later slice.
type atpCheckRequest struct {
	Requested    string             `json:"requested"`
	Quantity     string             `json:"quantity"`
	CompleteOnly bool               `json:"completeOnly"`
	Periods      []atpPeriodRequest `json:"periods"`
}

// atpPeriodView, atpScheduleView and atpPromiseView are the wire form: dates as
// YYYY-MM-DD and decimals as strings, matching api/openapi.yaml.
type atpPeriodView struct {
	Date   string          `json:"date"`
	Supply decimal.Decimal `json:"supply"`
	Demand decimal.Decimal `json:"demand"`
}

type atpScheduleView struct {
	Date     string          `json:"date"`
	Quantity decimal.Decimal `json:"quantity"`
	Period   int             `json:"period"`
}

type atpPromiseView struct {
	PromisedDate string            `json:"promisedDate,omitempty"`
	Complete     bool              `json:"complete"`
	Backorder    decimal.Decimal   `json:"backorder"`
	Source       string            `json:"source"`
	Schedule     []atpScheduleView `json:"schedule"`
}

type atpCheckResponse struct {
	Promise    atpPromiseView    `json:"promise"`
	Periods    []atpPeriodView   `json:"periods"`
	Cumulative []decimal.Decimal `json:"cumulative"`
}

func dayOf(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(dayLayout)
}

func atpResponse(p domain.Promise, periods []domain.Period) atpCheckResponse {
	promise := atpPromiseView{PromisedDate: dayOf(p.PromisedDate), Complete: p.Complete, Backorder: p.Backorder, Source: p.Source, Schedule: []atpScheduleView{}}
	for _, s := range p.Schedule {
		promise.Schedule = append(promise.Schedule, atpScheduleView{Date: dayOf(s.Date), Quantity: s.Quantity, Period: s.Period})
	}
	views := []atpPeriodView{}
	for _, per := range periods {
		views = append(views, atpPeriodView{Date: dayOf(per.Date), Supply: per.Supply, Demand: per.Demand})
	}
	return atpCheckResponse{Promise: promise, Periods: views, Cumulative: domain.Cumulative(periods)}
}

// parsePeriods validates ATP buckets, adding a message per bad field to fields.
func parsePeriods(in []atpPeriodRequest, fields map[string]string) []domain.Period {
	periods := make([]domain.Period, 0, len(in))
	for i, pr := range in {
		path := func(name string) string { return "periods[" + strconv.Itoa(i) + "]." + name }
		date, errDate := time.Parse(dayLayout, pr.Date)
		supply, errSupply := decimal.NewFromString(pr.Supply)
		demand, errDemand := decimal.NewFromString(pr.Demand)
		if errDate != nil {
			fields[path("date")] = "must be a date such as 2027-03-01"
		}
		if errSupply != nil || supply.IsNegative() {
			fields[path("supply")] = "must be a non-negative decimal"
		}
		if errDemand != nil || demand.IsNegative() {
			fields[path("demand")] = "must be a non-negative decimal"
		}
		if i > 0 && errDate == nil && !date.After(periods[i-1].Date) {
			fields[path("date")] = "must be later than the previous bucket"
		}
		periods = append(periods, domain.Period{Date: date, Supply: supply, Demand: demand})
	}
	return periods
}

// atpCheck answers the BR-27.1 question: when can this quantity be promised?
func (a *API) atpCheck(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req atpCheckRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	requested, err := time.Parse(dayLayout, req.Requested)
	if err != nil {
		fields["requested"] = "must be a date such as 2027-03-01"
	}
	qty, err := decimal.NewFromString(req.Quantity)
	if err != nil {
		fields["quantity"] = "must be a decimal"
	} else if err := domain.ValidateQuantity(qty); err != nil {
		fields["quantity"] = err.Error()
	}
	if n := len(req.Periods); n == 0 || n > maxPeriods {
		fields["periods"] = "must have 1 to 1000 buckets"
	}
	periods := parsePeriods(req.Periods, fields)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	promise, err := domain.PromiseDate(periods, requested, qty, req.CompleteOnly)
	if err != nil {
		validationFailed(w, r, map[string]string{"periods": err.Error()})
		return
	}
	a.atp.Add(1)
	httpx.JSON(w, http.StatusOK, atpResponse(promise, periods))
}
