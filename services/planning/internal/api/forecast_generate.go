package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/forecastclient"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

const (
	// CodeFeatureOff is 409 for a statistical generation while the
	// planning.statistical-forecast flag is off (4.22.10).
	CodeFeatureOff = "PLAN-FEATURE-OFF"
	// CodeForecastUnavailable is 502 when the forecast service cannot answer.
	CodeForecastUnavailable = "PLAN-FORECAST-UNAVAILABLE"
	// CodeForecastRange is 422 when a modelled value exceeds the largest
	// storable quantity (a trend extrapolated from a huge history).
	CodeForecastRange = "PLAN-FORECAST-RANGE"
)

// ForecastGenerator is the statistical forecast service (services/forecast-py).
type ForecastGenerator interface {
	Generate(ctx context.Context, series []forecastclient.Series) ([]forecastclient.Result, error)
}

// WithStatisticalForecast turns on the planning.statistical-forecast feature:
// generation calls g. Without it the generate route answers 409 PLAN-FEATURE-OFF.
func (a *API) WithStatisticalForecast(g ForecastGenerator) *API {
	a.generator = g
	return a
}

type generateSeriesDTO struct {
	ItemID       string            `json:"itemId"`
	SiteID       string            `json:"siteId"`
	History      []decimal.Decimal `json:"history"`
	Model        string            `json:"model"`
	SeasonLength int               `json:"seasonLength"`
	AutoSeason   bool              `json:"autoSeason"`
}

// generateRequest asks for a statistical forecast of monthly histories (FR-22.9).
// The caller sends the histories (the sales history is not owned by planning);
// History holds one non-negative value per calendar month, oldest first, ending
// the month before FirstPeriodStart.
type generateRequest struct {
	LegalEntityID    string              `json:"legalEntityId"`
	FirstPeriodStart string              `json:"firstPeriodStart"`
	HorizonPeriods   int                 `json:"horizonPeriods"`
	HistoryMonths    int                 `json:"historyMonths"`
	Series           []generateSeriesDTO `json:"series"`
}

var generateModels = map[string]bool{"auto": true, "ets": true, "croston": true, "tsb": true, "naive": true}

func (req *generateRequest) validate() (time.Time, map[string]string) {
	fields := map[string]string{}
	first, err := time.Parse(dateLayout, req.FirstPeriodStart)
	if err != nil || first.Day() != 1 {
		fields["firstPeriodStart"] = "must be the first day of a month as YYYY-MM-DD"
	}
	if req.HorizonPeriods == 0 {
		req.HorizonPeriods = 12
	}
	if req.HorizonPeriods < 1 || req.HorizonPeriods > domain.MaxGenerateHorizon {
		fields["horizonPeriods"] = "must be 1 to " + strconv.Itoa(domain.MaxGenerateHorizon) + " months"
	}
	if req.HistoryMonths == 0 {
		req.HistoryMonths = domain.DefaultHistoryMonth
	}
	if req.HistoryMonths < domain.MinHistoryMonths || req.HistoryMonths > domain.MaxHistoryMonths {
		fields["historyMonths"] = "must be " + strconv.Itoa(domain.MinHistoryMonths) + " to " + strconv.Itoa(domain.MaxHistoryMonths)
	}
	if n := len(req.Series); n == 0 || n > domain.MaxGenerateSeries {
		fields["series"] = "must hold 1 to " + strconv.Itoa(domain.MaxGenerateSeries) + " series"
	}
	type key struct{ item, site string }
	seen := map[key]bool{}
	for i := range req.Series {
		s := &req.Series[i]
		prefix := "series[" + strconv.Itoa(i) + "]."
		if !ids.Valid(s.ItemID) {
			fields[prefix+"itemId"] = "must be a UUID"
		}
		if !ids.Valid(s.SiteID) {
			fields[prefix+"siteId"] = "must be a UUID"
		}
		if seen[key{s.ItemID, s.SiteID}] {
			fields[prefix+"itemId"] = "repeats an item and site of this request"
		}
		seen[key{s.ItemID, s.SiteID}] = true
		if n := len(s.History); n == 0 || n > domain.MaxHistoryValues {
			fields[prefix+"history"] = "must hold 1 to " + strconv.Itoa(domain.MaxHistoryValues) + " monthly values"
		}
		for _, v := range s.History {
			if !validQty(v) {
				fields[prefix+"history"] = "values must be 0 or more with at most 6 decimals"
				break
			}
		}
		if s.Model == "" {
			s.Model = "auto"
		}
		if !generateModels[s.Model] {
			fields[prefix+"model"] = "must be auto, ets, croston, tsb or naive"
		}
		if s.SeasonLength < 0 || s.SeasonLength > 24 {
			fields[prefix+"seasonLength"] = "must be 1 to 24 (omit for 12)"
		}
	}
	return first, fields
}

type generatedSeries struct {
	ItemID       string   `json:"itemId"`
	SiteID       string   `json:"siteId"`
	Model        string   `json:"model"`
	SeasonLength int      `json:"seasonLength"`
	ShortHistory bool     `json:"shortHistory"`
	MAPE         *float64 `json:"mape"`
	WMAPE        *float64 `json:"wmape"`
	Warnings     []string `json:"warnings"`
}

// generateForecast writes a statistical forecast into a model (FR-22.9): the
// forecast service models each history, and the cells replace the model's
// statistical cells for the same item, site and month while manual and
// consensus cells stay. It needs If-Match on the model (0 creates it).
func (a *API) generateForecast(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	if a.generator == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeFeatureOff, "the planning.statistical-forecast feature is not enabled"))
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
	var req generateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	first, fields := req.validate()
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if err := a.checkGenerateTarget(r.Context(), code, expected); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	// forecast-py models in floating point; the history goes over as float64
	// and the forecast comes back as float64, made a decimal cell below.
	series := make([]forecastclient.Series, len(req.Series))
	for i, s := range req.Series {
		hist := make([]float64, len(s.History))
		for j, v := range s.History {
			hist[j], _ = v.Float64()
		}
		series[i] = forecastclient.Series{ItemID: s.ItemID, SiteID: s.SiteID, History: hist, Model: s.Model,
			HorizonPeriods: req.HorizonPeriods, SeasonLength: s.SeasonLength, AutoSeason: s.AutoSeason, HistoryMonths: req.HistoryMonths}
	}
	results, err := a.generator.Generate(r.Context(), series)
	if err != nil {
		if errors.Is(err, forecastclient.ErrUnavailable) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, CodeForecastUnavailable, "the statistical forecast service did not answer; try again later"))
			return
		}
		httpx.Fail(w, r, err)
		return
	}
	periods := domain.MonthPeriods(first, req.HorizonPeriods)
	cells := make([]store.ForecastCellInput, 0, len(results)*len(periods))
	generated := make([]generatedSeries, len(results))
	for i, res := range results {
		for j, p := range periods {
			qty, ok := domain.QtyFromFloat(res.Forecast[j])
			if !ok {
				httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeForecastRange,
					"the forecast of item "+series[i].ItemID+" at site "+series[i].SiteID+" for "+p.Start.Format(dateLayout)+" exceeds the largest storable quantity; nothing was written"))
				return
			}
			cells = append(cells, store.ForecastCellInput{ItemID: series[i].ItemID, SiteID: series[i].SiteID,
				PeriodStart: p.Start.Format(dateLayout), PeriodEnd: p.End.Format(dateLayout), Source: "statistical", Qty: qty})
		}
		warnings := res.Warnings
		if warnings == nil {
			warnings = []string{}
		}
		generated[i] = generatedSeries{ItemID: series[i].ItemID, SiteID: series[i].SiteID, Model: res.Model, SeasonLength: res.SeasonLength,
			ShortHistory: res.ShortHistory, MAPE: res.MAPE, WMAPE: res.WMAPE, Warnings: warnings}
	}
	out, err := a.store.GenerateStatisticalCells(r.Context(), code, req.LegalEntityID, expected, cells)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etagOf(out.Model.Version))
	httpx.JSON(w, http.StatusOK, map[string]any{"model": out.Model, "generated": generated, "cellsWritten": out.CellsWritten, "cellsKept": out.CellsKept})
}

// checkGenerateTarget refuses a stale If-Match or a submitted or published model
// before the forecast service is called, so a conflict does not cost a model run
// of up to 1,000 series. It holds no lock: the store checks both again under the
// row lock when it writes, after the call (the HTTP call never runs inside the
// transaction).
func (a *API) checkGenerateTarget(ctx context.Context, code string, expected int) error {
	m, _, err := a.store.ListForecastLines(ctx, code, "", "", 1)
	var p *httpx.Problem
	switch {
	case errors.As(err, &p) && p.Code == store.CodeForecastNotFound:
		m.Version, m.Status = 0, domain.ConsensusDraft // created on first use
	case err != nil:
		return err
	}
	if m.Version != expected {
		return httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict,
			"the forecast model was changed by another user; current version is "+strconv.Itoa(m.Version))
	}
	if !domain.ConsensusWritable(m.Status) {
		return httpx.NewProblem(http.StatusConflict, store.CodeForecastLocked, "forecast model "+code+" is "+m.Status+"; its cells are locked")
	}
	return nil
}
