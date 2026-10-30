package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/fx/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fx/internal/importer"
	"github.com/BenjaminSRussell/passage/services/fx/internal/store"
)

const (
	manualImportTimeout = 90 * time.Second
	importRetryAfter    = 5
)

type Importer interface {
	Run(ctx context.Context, rateType string, date time.Time) (importer.Result, error)
	SupportsHistory() bool
}

type importBody struct {
	RateType string `json:"rateType"`
	Date     string `json:"date"`
}

type importRunBody struct {
	RunID         string `json:"runId"`
	Provider      string `json:"provider"`
	RateType      string `json:"rateType"`
	Date          string `json:"date"`
	Attempts      int    `json:"attempts"`
	Imported      int    `json:"imported"`
	Unchanged     int    `json:"unchanged"`
	AlreadyDone   bool   `json:"alreadyDone"`
	Degraded      bool   `json:"degraded"`
	LastSuccessAt string `json:"lastSuccessAt,omitempty"`
	Pending       bool   `json:"pending,omitempty"`
}

func (a *API) WithImporters(importers map[string]Importer) *API {
	a.importers = importers
	return a
}

func (a *API) checkImportTarget(w http.ResponseWriter, r *http.Request, imp Importer, name, rateTypeCode string, date time.Time) bool {
	rt, err := a.store.GetRateType(r.Context(), rateTypeCode)
	if err != nil {
		var p *httpx.Problem
		if errors.As(err, &p) && p.Status == http.StatusNotFound {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "FX_RATE_INVALID", "unknown rate type"))
			return false
		}
		a.fail(w, r, "manual import rate type lookup failed", err)
		return false
	}
	if rt.Provider == "" || rt.Provider != name {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "FX_PROVIDER_MISMATCH", "the rate type is not configured for this provider"))
		return false
	}
	if !imp.SupportsHistory() && !date.Equal(today()) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "FX_PROVIDER_HISTORY_UNSUPPORTED", "this provider can only import today's rates"))
		return false
	}
	return true
}

func (a *API) importNow(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	name := chi.URLParam(r, "p")
	imp, ok := a.importers[name]
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "FX_PROVIDER_UNKNOWN", "provider not found"))
		return
	}
	var body importBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if body.RateType == "" {
		fields["rateType"] = "is required"
	}
	date := parseDate(fields, "date", body.Date)
	if len(fields) > 0 {
		httpx.Fail(w, r, badRequest(fields))
		return
	}
	if date.After(today()) {
		a.fail(w, r, "manual import rejected", domain.ErrFutureRate)
		return
	}
	if !a.checkImportTarget(w, r, imp, name, body.RateType, date) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), manualImportTimeout)
	defer cancel()
	res, err := imp.Run(ctx, body.RateType, date)
	if err != nil {
		if errors.Is(err, store.ErrLostOwnership) {
			w.Header().Set("Retry-After", strconv.Itoa(importRetryAfter))
		}
		if errors.Is(err, context.Canceled) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "FX_IMPORT_CANCELLED", "the import was cancelled before it finished"))
			return
		}
		a.fail(w, r, "manual import failed", err)
		return
	}
	if res.InProgress {
		w.Header().Set("Retry-After", strconv.Itoa(importRetryAfter))
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, importInProgressCode, importInProgressMsg))
		return
	}
	validFrom := date
	if !res.ValidFrom.IsZero() {
		validFrom = res.ValidFrom
	}
	out := importRunBody{
		RunID: res.RunID, Provider: name, RateType: body.RateType, Date: validFrom.Format(time.DateOnly),
		Attempts: res.Attempts, Imported: res.Imported, Unchanged: res.Unchanged,
		AlreadyDone: res.AlreadyDone, Degraded: res.Degraded, Pending: res.Pending, LastSuccessAt: formatTime(res.LastSuccessAt),
	}
	httpx.JSON(w, http.StatusOK, out)
}
