package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// registerInboundRoutes mounts the read side of the changes other services asked of purchase orders
// (slice 18). The changes themselves are written by the consumer, never over HTTP.
func (a *API) registerInboundRoutes(r chi.Router) {
	inquire := r.With(authz.RequireDuty(dutyPurchaseOrderInquire))
	inquire.Get("/inbound-changes", a.listInboundChanges)
	inquire.Get("/inbound-changes/{id}", a.getInboundChange)
}

// getInboundChange handles GET /inbound-changes/{id}.
func (a *API) getInboundChange(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := a.store.GetInboundChange(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// listInboundChanges handles GET /inbound-changes, newest first, filtered by source, status, purchase
// order and legal entity.
func (a *API) listInboundChanges(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.InboundFilter{Source: q.Get("source"), Status: q.Get("status"), PurchaseOrderID: q.Get("purchaseOrderId"), LegalEntityID: q.Get("legalEntityId")}
	if f.Source != "" && f.Source != domain.SourcePlanningMessage && f.Source != domain.SourceDropShipClosed {
		fail(w, r, invalid("source", "must be PLANNING_MESSAGE or DROPSHIP_CLOSED"))
		return
	}
	if f.Status != "" && f.Status != domain.InboundApplied && f.Status != domain.InboundRefused && f.Status != domain.InboundIgnored {
		fail(w, r, invalid("status", "must be APPLIED, REFUSED or IGNORED"))
		return
	}
	for name, v := range map[string]string{"purchaseOrderId": f.PurchaseOrderID, "legalEntityId": f.LegalEntityID} {
		if err := uuidField(name, v, false); err != nil {
			fail(w, r, err)
			return
		}
	}
	page, err := a.store.ListInboundChanges(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}
