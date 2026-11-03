package api

import (
	"net/http"

	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// listOpenOrderLines is GET /purchase-order-lines: open supply, the lines of confirmed purchase
// orders with quantity still to receive, filtered by delivery warehouse, site, product and due date.
// Master planning reads it to net purchases against a warehouse's demand (wave 22).
func (a *API) listOpenOrderLines(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	limit, err := listLimit(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	after := q.Get("cursor") // "<date>|<po id>|<line no>"; the store checks it
	f := store.OpenLineFilter{LegalEntityID: q.Get("legalEntityId"), WarehouseID: q.Get("warehouseId"),
		SiteID: q.Get("siteId"), ProductID: q.Get("productId")}
	for _, c := range []struct{ name, value string }{{"legalEntityId", f.LegalEntityID}, {"warehouseId", f.WarehouseID},
		{"siteId", f.SiteID}, {"productId", f.ProductID}} {
		if err := uuidField(c.name, c.value, false); err != nil {
			fail(w, r, err)
			return
		}
	}
	if f.LegalEntityID != "" {
		if err := inScope(p, f.LegalEntityID); err != nil {
			fail(w, r, err)
			return
		}
	}
	for _, d := range []struct {
		name string
		dst  **domain.Day
	}{{"dueFrom", &f.DueFrom}, {"dueTo", &f.DueTo}} {
		if v := q.Get(d.name); v != "" {
			day, err := domain.ParseDay(v)
			if err != nil {
				fail(w, r, invalid(d.name, "must be YYYY-MM-DD"))
				return
			}
			*d.dst = &day
		}
	}
	if f.DueFrom != nil && f.DueTo != nil && f.DueTo.Before(f.DueFrom.Time) {
		fail(w, r, invalid("dueTo", "must not be before dueFrom"))
		return
	}
	page, err := a.store.ListOpenOrderLines(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}
