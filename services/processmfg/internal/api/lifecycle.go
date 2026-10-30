package api

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

// registerLifecycle mounts the batch-order lifecycle (PRD 4.24.5): release and
// start and report-as-finished under "Maintain batch orders", end (which posts
// the cost split) under the wave-21 production journal duty.
func (a *API) registerLifecycle(r chi.Router) {
	write := authz.RequireDuty(DutyBatchOrderWrite)
	r.With(write).Post("/v1/production/batch-orders/{id}:release", a.advance(domain.CmdRelease))
	r.With(write).Post("/v1/production/batch-orders/{id}:start", a.advance(domain.CmdStart))
	r.With(write).Post("/v1/production/batch-orders/{id}:report-finished", a.reportFinished)
	r.With(authz.RequireDuty(DutyCoProductAllocate)).Post("/v1/production/batch-orders/{id}:end", a.end)
}

type versionRequest struct {
	Version int `json:"version"`
}

func (a *API) advance(cmd string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := principal(w, r); !ok {
			return
		}
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var req versionRequest
		idem, ok := decodeCommand(w, r, &req)
		if !ok {
			return
		}
		if req.Version < 1 {
			invalid(w, r, map[string]string{"version": "must be the version you read (1 or more)"})
			return
		}
		o, fresh, err := a.store.AdvanceBatchOrder(r.Context(), id, cmd, req.Version, idem)
		if err != nil {
			fail(w, r, err)
			return
		}
		writeCommand(w, fresh, http.StatusOK, o)
	}
}

type reportedLineRequest struct {
	LineNumber int             `json:"lineNumber"`
	Qty        decimal.Decimal `json:"qty"`
}

type reportRequest struct {
	Version int                   `json:"version"`
	MainQty decimal.Decimal       `json:"mainQty"`
	Outputs []reportedLineRequest `json:"outputs"`
}

func (a *API) reportFinished(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req reportRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	if req.Version < 1 {
		fields["version"] = "must be the version you read (1 or more)"
	}
	if !validQty(req.MainQty) {
		fields["mainQty"] = "must be a positive quantity below 10^12 with at most 6 decimals"
	}
	outputs := map[int]decimal.Decimal{}
	for i, o := range req.Outputs {
		field := fmt.Sprintf("outputs[%d]", i)
		if _, dup := outputs[o.LineNumber]; dup {
			fields[field+".lineNumber"] = "is listed twice"
		}
		if o.Qty.IsNegative() || o.Qty.GreaterThanOrEqual(maxQty()) || o.Qty.Exponent() < -domain.QtyPrecision {
			fields[field+".qty"] = "must be zero or more, below 10^12, with at most 6 decimals"
		}
		outputs[o.LineNumber] = o.Qty
	}
	if len(req.Outputs) > domain.MaxFormulaLines {
		fields["outputs"] = fmt.Sprintf("at most %d lines", domain.MaxFormulaLines)
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	o, fresh, err := a.store.ReportBatchOrder(r.Context(), id, store.ReportInput{MainQty: req.MainQty, Outputs: outputs}, req.Version, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusOK, o)
}

type basisRequest struct {
	LineNumber   int             `json:"lineNumber"`
	StandardCost decimal.Decimal `json:"standardCost"`
	Percent      decimal.Decimal `json:"percent"`
}

type byRateRequest struct {
	LineNumber int             `json:"lineNumber"`
	CostType   string          `json:"costType"`
	Rate       decimal.Decimal `json:"rate"`
}

type endRequest struct {
	Version    int             `json:"version"`
	Method     string          `json:"method"`
	Currency   string          `json:"currency"`
	BatchCost  decimal.Decimal `json:"batchCost"`
	Main       basisRequest    `json:"main"`
	CoProducts []basisRequest  `json:"coProducts"`
	ByProducts []byRateRequest `json:"byProducts"`
}

// validWeight reports whether d is a usable standard cost, percentage or
// by-product rate: zero or more, below 10^12, at most 6 decimals.
func validWeight(d decimal.Decimal) bool {
	return !d.IsNegative() && d.LessThan(maxQty()) && d.Exponent() >= -domain.QtyPrecision
}

// checkBasis adds the field errors of one main or co-product basis.
func checkBasis(fields map[string]string, field string, b basisRequest) {
	if !validWeight(b.StandardCost) {
		fields[field+".standardCost"] = "must be zero or more, below 10^12, with at most 6 decimals"
	}
	if !validWeight(b.Percent) || b.Percent.GreaterThan(decimal.NewFromInt(100)) {
		fields[field+".percent"] = "must be from 0 to 100 with at most 6 decimals"
	}
}

func validMethod(m string) bool {
	switch domain.AllocationMethod(m) {
	case domain.MethodNone, domain.MethodManual, domain.MethodTCA, domain.MethodQuantity:
		return true
	}
	return false
}

func (a *API) end(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req endRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	if req.Version < 1 {
		fields["version"] = "must be the version you read (1 or more)"
	}
	if !validMethod(req.Method) {
		fields["method"] = "must be none, manual, tca or quantity"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code such as USD"
	} else {
		// money rounds half away from zero to the currency's minor unit; a
		// cost with more decimals than that is refused rather than rounded.
		cost := money.Amount{Value: req.BatchCost, Currency: req.Currency}
		if req.BatchCost.IsNegative() || !cost.Round().Value.Equal(req.BatchCost) || req.BatchCost.GreaterThanOrEqual(decimal.New(1, 14)) {
			fields["batchCost"] = fmt.Sprintf("must be zero or more with at most %d decimals for %s", money.MinorUnits(req.Currency), req.Currency)
		}
	}
	if len(req.CoProducts) > domain.MaxFormulaLines || len(req.ByProducts) > domain.MaxFormulaLines {
		fields["coProducts"] = fmt.Sprintf("at most %d co-products and %d by-products", domain.MaxFormulaLines, domain.MaxFormulaLines)
	}
	checkBasis(fields, "main", req.Main)
	co := map[int]domain.CostBasis{}
	for i, c := range req.CoProducts {
		field := fmt.Sprintf("coProducts[%d]", i)
		if _, dup := co[c.LineNumber]; dup {
			fields[field+".lineNumber"] = "is listed twice"
		}
		checkBasis(fields, field, c)
		co[c.LineNumber] = domain.CostBasis{StandardCost: c.StandardCost, Percent: c.Percent}
	}
	by := map[int]domain.ByRate{}
	for i, b := range req.ByProducts {
		field := fmt.Sprintf("byProducts[%d]", i)
		if _, dup := by[b.LineNumber]; dup {
			fields[field+".lineNumber"] = "is listed twice"
		}
		switch b.CostType {
		case "per-series", "per-quantity", "percent", "recycled":
		default:
			fields[field+".costType"] = "must be per-series, per-quantity, percent or recycled"
		}
		if !validWeight(b.Rate) {
			fields[field+".rate"] = "must be zero or more, below 10^12, with at most 6 decimals"
		}
		by[b.LineNumber] = domain.ByRate{CostType: b.CostType, Rate: b.Rate}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	in := store.EndInput{Method: domain.AllocationMethod(req.Method), Currency: req.Currency, BatchCost: req.BatchCost,
		MainBasis: domain.CostBasis{StandardCost: req.Main.StandardCost, Percent: req.Main.Percent}, CoBasis: co, ByRates: by}
	o, fresh, err := a.store.EndBatchOrder(r.Context(), id, in, req.Version, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusOK, o)
}
