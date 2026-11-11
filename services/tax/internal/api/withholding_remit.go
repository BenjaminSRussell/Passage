package api

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

const (
	dutyWithholdingRecord = "tax.withholding.record"
	dutyWithholdingRemit  = "tax.withholding.remit"
	dutyWithholdingRead   = "tax.withholding.read"

	maxWithholdingEntries = 1000
)

// registerWithholdingRemittance mounts the withholding entries, remittance runs and certificates (#13 deliverable 8).
func (a *API) registerWithholdingRemittance(r chi.Router) {
	r.With(authz.RequireDuty(dutyWithholdingRecord)).Post("/v1/tax/withholding/entries", a.recordWithholding)
	r.With(authz.RequireDuty(dutyWithholdingRemit)).Post("/v1/tax/withholding/remittance-runs", a.runRemittance)
	r.With(authz.RequireDuty(dutyWithholdingRead)).Get("/v1/tax/withholding/remittance-runs", a.listRemittanceRuns)
	r.With(authz.RequireDuty(dutyWithholdingRead)).Get("/v1/tax/withholding/remittance-runs/{id}", a.getRemittanceRun)
	r.With(authz.RequireDuty(dutyWithholdingRead)).Get("/v1/tax/withholding/certificates", a.listWithholdingCertificates)
}

type withholdingEntriesRequest struct {
	Entries []domain.WithholdingEntry `json:"entries"`
}

func (a *API) recordWithholding(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	var req withholdingEntriesRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.reject(w, r, err)
		return
	}
	if len(req.Entries) == 0 || len(req.Entries) > maxWithholdingEntries {
		a.reject(w, r, invalid("entries", "entries must hold between 1 and %d rows", maxWithholdingEntries))
		return
	}
	seen := map[string]bool{}
	for i, e := range req.Entries {
		field := fmt.Sprintf("entries[%d]", i)
		if !ids.Valid(e.ID) {
			a.reject(w, r, invalid(field+".id", "id must be a UUID"))
			return
		}
		if seen[e.ID] {
			a.reject(w, r, invalid(field+".id", "id %s appears twice", e.ID))
			return
		}
		seen[e.ID] = true
		if err := domain.ValidateWithholdingEntry(e); err != nil {
			de := err.(*domain.Error)
			a.reject(w, r, invalid(field+"."+de.Field, "%s", de.Detail))
			return
		}
	}
	n, err := a.config.RecordWithholding(r.Context(), req.Entries)
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]int{"recorded": n, "duplicates": len(req.Entries) - n})
}

type remittanceRequest struct {
	PeriodFrom domain.Date `json:"period_from"`
	PeriodTo   domain.Date `json:"period_to"`
}

func (a *API) runRemittance(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	var req remittanceRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.reject(w, r, err)
		return
	}
	switch {
	case req.PeriodFrom.IsZero() || req.PeriodTo.IsZero():
		a.reject(w, r, invalid("period_from", "period_from and period_to are required"))
		return
	case req.PeriodTo.Before(req.PeriodFrom.Time):
		a.reject(w, r, invalid("period_to", "period_to may not precede period_from"))
		return
	case req.PeriodFrom.Year() != req.PeriodTo.Year():
		a.reject(w, r, invalid("period_to", "a remittance run stays within one calendar year, because its numbers are per year"))
		return
	}
	run, err := a.config.RunRemittance(r.Context(), domain.Period{From: req.PeriodFrom, To: req.PeriodTo})
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, run)
}

func (a *API) listRemittanceRuns(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	out, err := a.config.ListRemittanceRuns(r.Context())
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) getRemittanceRun(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		a.reject(w, r, invalid("id", "id must be a UUID"))
		return
	}
	out, err := a.config.GetRemittanceRun(r.Context(), id)
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listWithholdingCertificates(w http.ResponseWriter, r *http.Request) {
	if !a.configReady(w, r) {
		return
	}
	out, err := a.config.ListWithholdingCertificates(r.Context(), r.URL.Query().Get("party_ref"))
	if err != nil {
		a.reject(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}
