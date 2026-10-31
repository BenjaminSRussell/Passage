package api

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const (
	summaryPath   = "/v1/sales/summary"
	expirationRun = "/v1/sales/quotations:expire"
)

// expireQuotations runs the quotation expiry: every DRAFT or SENT quotation
// in scope whose validity has passed becomes EXPIRED (FR-27.1). The run is
// state-based, so repeating it is harmless; it takes no Idempotency-Key.
// legalEntityId limits it to one legal entity.
func (a *API) expireQuotations(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	res, err := a.store.ExpireQuotations(r.Context(), legalEntity, a.now(), p.UserID)
	if err != nil {
		a.fail(w, r, "expire quotations", err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// getSummary returns the sales workspace tiles and the quotation pipeline
// KPIs (PRD 5.27.14). windowDays sets the conversion-rate period (default 90);
// status, minProbabilityPct and expiringWithinDays narrow the pipeline.
func (a *API) getSummary(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.SummaryFilter{LegalEntityID: legalEntity, Today: a.now(), WindowDays: store.DefaultSummaryWindowDays, Status: q.Get("status")}
	bad := map[string]string{}
	intParam(q.Get("windowDays"), 1, store.MaxSummaryWindowDays, "windowDays", &f.WindowDays, bad)
	intParam(q.Get("minProbabilityPct"), 0, 100, "minProbabilityPct", &f.MinProbabilityPct, bad)
	intParam(q.Get("expiringWithinDays"), 1, store.MaxSummaryWindowDays, "expiringWithinDays", &f.ExpiringWithinDays, bad)
	if f.Status != "" && f.Status != domain.QuoteDraft && f.Status != domain.QuoteSent {
		bad["status"] = "must be DRAFT or SENT"
	}
	if len(bad) > 0 {
		validationFailed(w, r, bad)
		return
	}
	sum, err := a.store.Summary(r.Context(), f)
	if err != nil {
		a.fail(w, r, "sales summary", err)
		return
	}
	httpx.JSON(w, http.StatusOK, sum)
}

// intParam parses an optional whole-number query parameter within [lo, hi]
// into dst, recording a field error in bad when it is malformed or out of
// range. An absent parameter leaves dst unchanged.
func intParam(raw string, lo, hi int, name string, dst *int, bad map[string]string) {
	if raw == "" {
		return
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lo || n > hi {
		bad[name] = fmt.Sprintf("must be a whole number from %d to %d", lo, hi)
		return
	}
	*dst = n
}
