package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// routesInquiry mounts the delivered-not-invoiced report and the SSCC register
// of section 5.28.14.
// WarehouseView covers "GET shipments, tracking, DNI" (section 5.28.17).
func (a *API) routesInquiry(r chi.Router) {
	r.With(authz.RequireDuty("WarehouseView")).Get("/api/v1/fulfillment/delivered-not-invoiced", a.deliveredNotInvoiced)
	r.With(authz.RequireDuty("WarehouseView")).Get("/api/v1/fulfillment/sscc-register", a.listSSCCs)
}

type dniResponse struct {
	LegalEntityID string            `json:"legalEntityId"`
	CustomerID    *string           `json:"customerId"`
	AsOf          string            `json:"asOf"`
	Account       string            `json:"account"`
	Totals        []domain.DNITotal `json:"totals"`
	// LineCount is the number of open slips; Lines holds at most limit of them,
	// while Totals always cover all LineCount.
	LineCount int       `json:"lineCount"`
	Lines     []dniLine `json:"lines"`
}

type dniLine struct {
	domain.DNIRow
	DaysOutstanding int `json:"daysOutstanding"`
}

// deliveredNotInvoiced answers GET /api/v1/fulfillment/delivered-not-invoiced
// for one legal entity, optionally one customer, as of a date (default today,
// UTC). It returns the open 1405 balance per currency and the slips that make
// it up with their days outstanding (FR-28.12, AC-28.8).
func (a *API) deliveredNotInvoiced(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.DNIFilter{LegalEntityID: q.Get("legalEntityId"), CustomerID: q.Get("customerId"), AsOf: dateOnly(a.now())}
	if !ids.Valid(f.LegalEntityID) {
		fail(w, r, "legalEntityId", "is required and must be a UUID")
		return
	}
	if f.CustomerID != "" && !ids.Valid(f.CustomerID) {
		fail(w, r, "customerId", "must be a UUID")
		return
	}
	if v := q.Get("asOf"); v != "" {
		t, err := time.Parse(dateLayout, v)
		if err != nil {
			fail(w, r, "asOf", "must be a YYYY-MM-DD date")
			return
		}
		f.AsOf = t
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	rows, err := a.store.DeliveredNotInvoiced(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := dniResponse{
		LegalEntityID: f.LegalEntityID,
		AsOf:          f.AsOf.Format(dateLayout),
		Account:       domain.AccountDeliveredNotInvoiced,
		Totals:        domain.DeliveredNotInvoiced(rows),
		LineCount:     len(rows),
		Lines:         []dniLine{},
	}
	if f.CustomerID != "" {
		out.CustomerID = &f.CustomerID
	}
	for i, row := range rows {
		if i == limit {
			break
		}
		out.Lines = append(out.Lines, dniLine{DNIRow: row, DaysOutstanding: domain.DaysOutstanding(row.PostingDate, f.AsOf)})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
