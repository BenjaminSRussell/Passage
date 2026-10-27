package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

const (
	maxForecastLines = 1000
	codeMessage      = "must be 2 to 20 characters of A-Z, 0-9, _ or -"
)

var hundred = decimal.NewFromInt(100)

// forecastLinesRequest is the bulk write of forecast cells (FR-22.9 manual
// override). A reductionPrinciple, with an optional reductionPercent, replaces
// the model's reduction setup (FR-22.8).
type forecastLinesRequest struct {
	ReductionPrinciple *string           `json:"reductionPrinciple"`
	ReductionPercent   *decimal.Decimal  `json:"reductionPercent"`
	Lines              []forecastLineDTO `json:"lines"`
}

type forecastLineDTO struct {
	ItemID      string           `json:"itemId"`
	SiteID      string           `json:"siteId"`
	PeriodStart string           `json:"periodStart"`
	PeriodEnd   string           `json:"periodEnd"`
	Qty         *decimal.Decimal `json:"qty"`
	SalesQty    decimal.Decimal  `json:"salesQty"`
	Source      string           `json:"source"`
}

// Planners write manual cells and the statistical job writes statistical ones;
// consensus cells come only from the approval workflow, not from this API.
var writableSources = map[string]bool{"statistical": true, "manual": true}

func validQty(q decimal.Decimal) bool { return !q.IsNegative() && q.Exponent() >= -6 }

func (req forecastLinesRequest) validate() map[string]string {
	fields := map[string]string{}
	if n := len(req.Lines); n == 0 || n > maxForecastLines {
		fields["lines"] = "must hold 1 to " + strconv.Itoa(maxForecastLines) + " cells"
	}
	if req.ReductionPrinciple != nil && !validPrinciple(*req.ReductionPrinciple) {
		fields["reductionPrinciple"] = "must be none, percent, transactions or transactions-dynamic"
	}
	if req.ReductionPercent != nil {
		switch {
		case req.ReductionPrinciple == nil || *req.ReductionPrinciple != domain.ReductionPercent:
			fields["reductionPercent"] = "is only accepted with reductionPrinciple percent"
		case !validQty(*req.ReductionPercent) || req.ReductionPercent.GreaterThan(hundred):
			fields["reductionPercent"] = "must be 0 to 100 with at most 6 decimals"
		}
	}
	type cellKey struct{ item, site, start string }
	seen := map[cellKey]bool{}
	for i, l := range req.Lines {
		prefix := "lines[" + strconv.Itoa(i) + "]."
		if !ids.Valid(l.ItemID) {
			fields[prefix+"itemId"] = "must be a UUID"
		}
		if !ids.Valid(l.SiteID) {
			fields[prefix+"siteId"] = "must be a UUID"
		}
		start, errStart := time.Parse(dateLayout, l.PeriodStart)
		end, errEnd := time.Parse(dateLayout, l.PeriodEnd)
		if errStart != nil {
			fields[prefix+"periodStart"] = "must be a date as YYYY-MM-DD"
		}
		if errEnd != nil {
			fields[prefix+"periodEnd"] = "must be a date as YYYY-MM-DD"
		}
		if errStart == nil && errEnd == nil && end.Before(start) {
			fields[prefix+"periodEnd"] = "must not be before periodStart"
		}
		if l.Qty == nil {
			fields[prefix+"qty"] = "is required; send 0 to clear the forecast of the period"
		} else if !validQty(*l.Qty) {
			fields[prefix+"qty"] = "must be 0 or more with at most 6 decimals"
		}
		if !validQty(l.SalesQty) {
			fields[prefix+"salesQty"] = "must be 0 or more with at most 6 decimals"
		}
		if l.Source != "" && !writableSources[l.Source] {
			fields[prefix+"source"] = "must be manual or statistical"
		}
		k := cellKey{l.ItemID, l.SiteID, l.PeriodStart}
		if seen[k] {
			fields[prefix+"periodStart"] = "repeats a cell of the same item, site and period in this request"
		}
		seen[k] = true
	}
	return fields
}

func validPrinciple(p string) bool {
	switch p {
	case domain.ReductionNone, domain.ReductionPercent, domain.ReductionTransactions, domain.ReductionTransactionsDynamic:
		return true
	}
	return false
}

// putForecastLines upserts forecast cells and recomputes their reduced
// quantities. It requires If-Match with the model's ETag; a model that does
// not exist yet has ETag "0".
func (a *API) putForecastLines(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	code := chi.URLParam(r, "modelCode")
	if !planCodePattern.MatchString(code) {
		validationFailed(w, r, map[string]string{"modelCode": codeMessage})
		return
	}
	expected, ok := ifMatch(w, r, 0)
	if !ok {
		return
	}
	var req forecastLinesRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := req.validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	var spec *store.ModelSpec
	if req.ReductionPrinciple != nil {
		s := store.ModelSpec{ReductionPrinciple: *req.ReductionPrinciple, ReductionPercent: decimal.Zero}
		if req.ReductionPercent != nil {
			s.ReductionPercent = *req.ReductionPercent
		}
		spec = &s
	}
	cells := make([]store.ForecastCellInput, 0, len(req.Lines))
	for _, l := range req.Lines {
		source := l.Source
		if source == "" {
			source = "manual"
		}
		cells = append(cells, store.ForecastCellInput{ItemID: l.ItemID, SiteID: l.SiteID, PeriodStart: l.PeriodStart,
			PeriodEnd: l.PeriodEnd, Source: source, Qty: *l.Qty, SalesQty: l.SalesQty})
	}
	m, err := a.store.UpsertForecastLines(r.Context(), code, expected, spec, cells)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etagOf(m.Version))
	httpx.JSON(w, http.StatusOK, map[string]any{"model": m, "updated": len(cells)})
}

// getForecastLines returns a model's cells with their reduced quantities.
func (a *API) getForecastLines(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	code := chi.URLParam(r, "modelCode")
	q := r.URL.Query()
	itemID, siteID := q.Get("itemId"), q.Get("siteId")
	fields := map[string]string{}
	if !planCodePattern.MatchString(code) {
		fields["modelCode"] = codeMessage
	}
	if itemID != "" && !ids.Valid(itemID) {
		fields["itemId"] = "must be a UUID"
	}
	if siteID != "" && !ids.Valid(siteID) {
		fields["siteId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	m, lines, err := a.store.ListForecastLines(r.Context(), code, itemID, siteID, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etagOf(m.Version))
	httpx.JSON(w, http.StatusOK, map[string]any{"model": m, "items": lines})
}

// submitForecast submits a draft model for consensus approval (FR-22.9): its
// cells lock until an approver publishes or rejects it. If-Match is required,
// so a replay of the same submit is answered 412 rather than applied twice.
func (a *API) submitForecast(w http.ResponseWriter, r *http.Request) {
	code, expected, ok := consensusRequest(w, r)
	if !ok {
		return
	}
	m, err := a.store.SubmitForecastConsensus(r.Context(), code, expected)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etagOf(m.Version))
	httpx.JSON(w, http.StatusOK, map[string]any{"model": m})
}

// publishForecast approves a submitted model and publishes it as
// forecast.published.v1. The approver must not have submitted or edited it.
func (a *API) publishForecast(w http.ResponseWriter, r *http.Request) {
	a.decideForecast(w, r, true)
}

// rejectForecast returns a submitted model to draft so planners can change it.
func (a *API) rejectForecast(w http.ResponseWriter, r *http.Request) {
	a.decideForecast(w, r, false)
}

func (a *API) decideForecast(w http.ResponseWriter, r *http.Request, approve bool) {
	code, expected, ok := consensusRequest(w, r)
	if !ok {
		return
	}
	m, pub, err := a.store.DecideForecastConsensus(r.Context(), code, expected, approve)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etagOf(m.Version))
	body := map[string]any{"model": m}
	if pub != nil {
		body["publication"] = pub
	}
	httpx.JSON(w, http.StatusOK, body)
}

// consensusRequest checks the tenant, the model code and If-Match (an
// existing model's version, so at least 1).
func consensusRequest(w http.ResponseWriter, r *http.Request) (string, int, bool) {
	if !validTenant(w, r) {
		return "", 0, false
	}
	code := chi.URLParam(r, "modelCode")
	if !planCodePattern.MatchString(code) {
		validationFailed(w, r, map[string]string{"modelCode": codeMessage})
		return "", 0, false
	}
	expected, ok := ifMatch(w, r, 1)
	if !ok {
		return "", 0, false
	}
	return code, expected, true
}
