package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/lean/internal/domain"
	"github.com/BenjaminSRussell/passage/services/lean/internal/store"
)

type openPeriodRequest struct {
	PeriodStart string `json:"periodStart"`
	PeriodEnd   string `json:"periodEnd"`
	Currency    string `json:"currency"`
}

// openPeriod opens a costing period of a flow (FR-24.12).
func (a *API) openPeriod(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	flowID, ok := pathID(w, r)
	if !ok {
		return
	}
	var req openPeriodRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	start, startOK := parseDate(fields, "periodStart", req.PeriodStart)
	end, endOK := parseDate(fields, "periodEnd", req.PeriodEnd)
	if startOK && endOK && end.Before(start) {
		fields["periodEnd"] = "must not be before periodStart"
	}
	if !domain.ValidCurrency(req.Currency) {
		fields["currency"] = "must be a three-letter ISO 4217 code in upper case"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	p, fresh, err := a.store.OpenPeriod(r.Context(), store.OpenPeriod{FlowID: flowID, PeriodStart: start, PeriodEnd: end, Currency: req.Currency}, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusCreated, p)
}

// listPeriods returns a flow's costing periods, latest first.
func (a *API) listPeriods(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	flowID, ok := pathID(w, r)
	if !ok {
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListPeriods(r.Context(), flowID, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

type settlePeriodRequest struct {
	Version        int              `json:"version"`
	MaterialCost   *decimal.Decimal `json:"materialCost"`
	ConversionCost *decimal.Decimal `json:"conversionCost"`
	OutputValue    *decimal.Decimal `json:"outputValue"`
}

// settlePeriod settles a period with its amounts (BR-24.9, AC-24.8). The
// amounts' scale is checked against the period currency by the store.
func (a *API) settlePeriod(w http.ResponseWriter, r *http.Request) {
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
	var req settlePeriodRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	if req.Version < 1 {
		fields["version"] = "must be the version you read (1 or more)"
	}
	for name, v := range map[string]*decimal.Decimal{"materialCost": req.MaterialCost, "conversionCost": req.ConversionCost, "outputValue": req.OutputValue} {
		if v == nil {
			fields[name] = "is required"
		} else if v.IsNegative() {
			fields[name] = "must be zero or more"
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	p, fresh, err := a.store.SettlePeriod(r.Context(), store.SettlePeriod{FlowID: flowID, PeriodID: periodID, Version: req.Version,
		MaterialCost: *req.MaterialCost, ConversionCost: *req.ConversionCost, OutputValue: *req.OutputValue}, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusOK, p)
}
