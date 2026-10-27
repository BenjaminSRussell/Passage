package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/lean/internal/domain"
)

// maxRates bounds the rates of one backflush request.
const maxRates = 200

type rateRequest struct {
	ActivityID  string           `json:"activityId"`
	RatePerHour *decimal.Decimal `json:"ratePerHour"`
}

type backflushRequest struct {
	Rates []rateRequest `json:"rates"`
}

// ConversionLine is one activity of a backflush: the jobs completed in the
// period, the quantity they processed, the hours (cycle time × quantity) and,
// for process and transfer activities, the rate and cost in the period
// currency. A service activity has no rate or cost here (FR-24.20).
type ConversionLine struct {
	ActivityID   string  `json:"activityId"`
	Sequence     int     `json:"sequence"`
	Name         string  `json:"name"`
	Kind         string  `json:"kind"`
	CycleTimeS   int     `json:"cycleTimeS"`
	Jobs         int     `json:"jobs"`
	ProcessedQty string  `json:"processedQty"`
	Hours        string  `json:"hours"`
	RatePerHour  *string `json:"ratePerHour"`
	Cost         *string `json:"cost"`
}

// BackflushPreview is the conversion cost of a costing period by backflush
// (FR-24.12). It is a preview: nothing is stored, and the period settles with
// the amounts the settle command is given.
type BackflushPreview struct {
	PeriodID       string           `json:"periodId"`
	FlowID         string           `json:"flowId"`
	PeriodStart    string           `json:"periodStart"`
	PeriodEnd      string           `json:"periodEnd"`
	Currency       string           `json:"currency"`
	Status         string           `json:"status"`
	ConversionCost string           `json:"conversionCost"`
	Activities     []ConversionLine `json:"activities"`
}

func fixedAmount(a *money.Amount) *string {
	if a == nil {
		return nil
	}
	s := a.Value.StringFixed(money.MinorUnits(a.Currency))
	return &s
}

// backflush previews the conversion cost of a period (FR-24.12): for every
// activity of the flow, the jobs completed in the period at the activity's
// cycle time and the hourly rate the caller gives. Activity cost rates belong
// to the wave-9 resources, so they come with the request. It reads only, so
// it takes no Idempotency-Key.
func (a *API) backflush(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	flowID, ok := pathID(w, r)
	if !ok {
		return
	}
	periodID := chi.URLParam(r, "periodId")
	if !ids.Valid(periodID) {
		invalid(w, r, map[string]string{"periodId": "must be a UUID"})
		return
	}
	var req backflushRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(req.Rates) > maxRates {
		invalid(w, r, map[string]string{"rates": "at most 200 rates"})
		return
	}
	rates := make(map[string]decimal.Decimal, len(req.Rates))
	for _, rt := range req.Rates {
		switch {
		case !ids.Valid(rt.ActivityID):
			invalid(w, r, map[string]string{"rates": "every activityId must be a UUID"})
			return
		case rt.RatePerHour == nil:
			invalid(w, r, map[string]string{"rates": "every rate needs ratePerHour"})
			return
		}
		if _, dup := rates[rt.ActivityID]; dup {
			invalid(w, r, map[string]string{"rates": "activity " + rt.ActivityID + " has more than one rate"})
			return
		}
		rates[rt.ActivityID] = *rt.RatePerHour
	}
	work, err := a.store.PeriodWork(r.Context(), flowID, periodID)
	if err != nil {
		fail(w, r, err)
		return
	}
	p := work.Period
	bf, err := domain.BackflushConversion(p.Currency, work.Activities, rates)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := BackflushPreview{PeriodID: p.ID, FlowID: p.FlowID, PeriodStart: p.PeriodStart, PeriodEnd: p.PeriodEnd, Currency: p.Currency,
		Status: p.Status, ConversionCost: *fixedAmount(&bf.Conversion), Activities: make([]ConversionLine, len(bf.Lines))}
	for i, l := range bf.Lines {
		line := ConversionLine{ActivityID: l.ActivityID, Sequence: l.Sequence, Name: l.Name, Kind: l.Kind, CycleTimeS: l.CycleTimeS, Jobs: l.Jobs,
			ProcessedQty: l.Qty.String(), Hours: l.Hours.StringFixed(4), Cost: fixedAmount(l.Cost)}
		if l.Rate != nil {
			s := l.Rate.Value.String()
			line.RatePerHour = &s
		}
		out.Activities[i] = line
	}
	httpx.JSON(w, http.StatusOK, out)
}
