package api

import (
	"net/http"
	"time"

	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// inquiryFilter reads the shared inquiry query parameters: legalEntityId (must be in scope),
// vendorId, productId and asOf (default today, UTC).
func (a *API) inquiryFilter(w http.ResponseWriter, r *http.Request) (store.InquiryFilter, bool) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return store.InquiryFilter{}, false
	}
	q := r.URL.Query()
	f := store.InquiryFilter{LegalEntityID: q.Get("legalEntityId"), VendorID: q.Get("vendorId"), ProductID: q.Get("productId")}
	for _, c := range []struct{ name, value string }{{"legalEntityId", f.LegalEntityID}, {"vendorId", f.VendorID}, {"productId", f.ProductID}} {
		if err := uuidField(c.name, c.value, false); err != nil {
			fail(w, r, err)
			return f, false
		}
	}
	if f.LegalEntityID != "" {
		if err := inScope(p, f.LegalEntityID); err != nil {
			fail(w, r, err)
			return f, false
		}
	}
	now := a.now().UTC()
	f.AsOf = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for _, d := range []struct {
		name string
		dst  *time.Time
	}{{"asOf", &f.AsOf}} {
		if v := q.Get(d.name); v != "" {
			day, err := domain.ParseDay(v)
			if err != nil {
				fail(w, r, invalid(d.name, "must be YYYY-MM-DD"))
				return f, false
			}
			*d.dst = day.Time
		}
	}
	return f, true
}

// listOpenOrdersByVendor is GET /inquiries/open-orders-by-vendor (PRD 3.17.14).
func (a *API) listOpenOrdersByVendor(w http.ResponseWriter, r *http.Request) {
	limit, err := listLimit(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	f, ok := a.inquiryFilter(w, r)
	if !ok {
		return
	}
	page, err := a.store.ListOpenOrdersByVendor(r.Context(), f, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

// listOverdueLines is GET /inquiries/overdue-lines (PRD 3.17.14).
func (a *API) listOverdueLines(w http.ResponseWriter, r *http.Request) {
	limit, err := listLimit(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	f, ok := a.inquiryFilter(w, r)
	if !ok {
		return
	}
	page, err := a.store.ListOverdueLines(r.Context(), f, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

// listBackorderedLines is GET /inquiries/backordered-lines (PRD 3.17.14): open lines received in part.
func (a *API) listBackorderedLines(w http.ResponseWriter, r *http.Request) {
	limit, err := listLimit(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	f, ok := a.inquiryFilter(w, r)
	if !ok {
		return
	}
	page, err := a.store.ListBackorderedLines(r.Context(), f, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

// listPriceHistory is GET /inquiries/price-history (PRD 3.17.14, ME1P): productId is required.
func (a *API) listPriceHistory(w http.ResponseWriter, r *http.Request) {
	limit, err := listLimit(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	f, ok := a.inquiryFilter(w, r)
	if !ok {
		return
	}
	if f.ProductID == "" {
		fail(w, r, invalid("productId", "is required"))
		return
	}
	q := r.URL.Query()
	for _, d := range []struct {
		name string
		dst  **domain.Day
	}{{"from", &f.From}, {"to", &f.To}} {
		if v := q.Get(d.name); v != "" {
			day, err := domain.ParseDay(v)
			if err != nil {
				fail(w, r, invalid(d.name, "must be YYYY-MM-DD"))
				return
			}
			*d.dst = &day
		}
	}
	if f.From != nil && f.To != nil && f.To.Before(f.From.Time) {
		fail(w, r, invalid("to", "must not be before from"))
		return
	}
	page, err := a.store.ListPriceHistory(r.Context(), f, q.Get("cursor"), limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}
