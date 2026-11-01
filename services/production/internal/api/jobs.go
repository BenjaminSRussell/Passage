package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

// listJobs serves the dated jobs across orders (PRD 4.21 ProductionJob): filter by
// resource, order, site, job type, order status and a time window; keyset pages with the
// count and planned hours of the whole filter.
func (a *API) listJobs(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	q := r.URL.Query()
	fields := map[string]string{}
	f := store.JobFilter{LegalEntityID: q.Get("legalEntityId"), SiteID: q.Get("siteId"), ResourceID: q.Get("resourceId"), OrderID: q.Get("orderId"),
		JobType: domain.JobType(q.Get("jobType")), OrderStatus: domain.Status(q.Get("orderStatus")), Cursor: q.Get("cursor")}
	f.Limit = reportLimit(q.Get("limit"), fields)
	for name, v := range map[string]string{"legalEntityId": f.LegalEntityID, "siteId": f.SiteID, "resourceId": f.ResourceID, "orderId": f.OrderID, "cursor": f.Cursor} {
		if v != "" && !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	if f.JobType != "" && !domain.ValidJobType(f.JobType) {
		fields["jobType"] = "must be queue-before, setup, process, queue-after or transport"
	}
	if f.OrderStatus != "" && !f.OrderStatus.Valid() {
		fields["orderStatus"] = "must be a production order status"
	}
	for name, dst := range map[string]**time.Time{"from": &f.From, "to": &f.To} {
		if raw := q.Get(name); raw != "" {
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				fields[name] = "must be a date and time (RFC 3339)"
				continue
			}
			*dst = &t
		}
	}
	if f.From != nil && f.To != nil && !f.To.After(*f.From) {
		fields["to"] = "must be after from"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	page, err := a.store.ListJobs(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list production jobs", err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

// orderJobs serves the jobs of one order in route order.
func (a *API) orderJobs(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := orderID(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if _, err := a.store.GetOrder(r.Context(), id); err != nil {
		a.fail(w, r, "read production order", err)
		return
	}
	page, err := a.store.ListJobs(r.Context(), store.JobFilter{OrderID: id, Limit: maxOrderJobs})
	if err != nil {
		a.fail(w, r, "list production jobs", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"orderId": id, "items": page.Items, "plannedHours": page.PlannedHours})
}
