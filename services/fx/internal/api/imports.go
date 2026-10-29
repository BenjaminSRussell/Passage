package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/fx/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fx/internal/importer"
)

const manualImportTimeout = 90 * time.Second

type Importer interface {
	Run(ctx context.Context, rateType string, date time.Time) (importer.Result, error)
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
}

func (a *API) WithImporters(importers map[string]Importer) *API {
	a.importers = importers
	return a
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
	ctx, cancel := context.WithTimeout(r.Context(), manualImportTimeout)
	defer cancel()
	res, err := imp.Run(ctx, body.RateType, date)
	if err != nil {
		a.fail(w, r, "manual import failed", err)
		return
	}
	out := importRunBody{
		RunID: res.RunID, Provider: name, RateType: body.RateType, Date: date.Format(time.DateOnly),
		Attempts: res.Attempts, Imported: res.Imported, Unchanged: res.Unchanged,
		AlreadyDone: res.AlreadyDone, Degraded: res.Degraded, LastSuccessAt: formatTime(res.LastSuccessAt),
	}
	httpx.JSON(w, http.StatusOK, out)
}
