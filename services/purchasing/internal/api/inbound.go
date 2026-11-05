package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// registerInboundRoutes mounts the changes other services asked of purchase orders (slice 18) and the two
// ways a buyer settles a refused one, POST /inbound-changes/{id}:acknowledge and :retry (slice 19). The
// changes themselves are written by the consumer, never over HTTP.
func (a *API) registerInboundRoutes(r chi.Router) {
	inquire := r.With(authz.RequireDuty(dutyPurchaseOrderInquire))
	inquire.Get("/inbound-changes", a.listInboundChanges)
	inquire.Get("/inbound-changes/{id}", a.getInboundChange)
	r.Post("/inbound-changes/{id}", a.inboundChangeAction)
}

// inboundChangeAction handles POST /inbound-changes/{id}:acknowledge|:retry. Both need procurement.purchase-order.maintain,
// the duty of editing the order by hand, and the change's legal entity in the caller's scope. A retry runs
// the request again as the caller, through the change management path.
func (a *API) inboundChangeAction(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "id"), ":")
	if !found || (action != "acknowledge" && action != "retry") || !ids.Valid(id) {
		fail(w, r, httpx.NewProblem(http.StatusNotFound, codeUnknownAction, "unknown inbound change action"))
		return
	}
	if !p.HasDuty(dutyPurchaseOrderMaintain) {
		fail(w, r, httpx.NewProblem(http.StatusForbidden, codeScope, "missing duty "+dutyPurchaseOrderMaintain))
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	note, err := readDecision(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	cur, err := a.store.GetInboundChange(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := inScope(p, cur.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	idem := store.Idem{Key: key, Hash: hashBody("inbound-change."+action, map[string]any{"id": id, "note": note})}
	var out store.InboundChange
	if action == "retry" {
		out, err = a.store.RetryInboundChange(r.Context(), id, note, idem)
	} else {
		out, err = a.store.AcknowledgeInboundChange(r.Context(), id, note, idem)
	}
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
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
	f := store.InboundFilter{Source: q.Get("source"), Status: q.Get("status"), PurchaseOrderID: q.Get("purchaseOrderId"), LegalEntityID: q.Get("legalEntityId"),
		Resolution: q.Get("resolution")}
	switch f.Source {
	case "", domain.SourcePlanningMessage, domain.SourceDropShipClosed, domain.SourceDropShipCancelled:
	default:
		fail(w, r, invalid("source", "must be PLANNING_MESSAGE, DROPSHIP_CLOSED or DROPSHIP_CANCELLED"))
		return
	}
	switch f.Resolution {
	case "", "NONE", domain.ResolutionAcknowledged, domain.ResolutionRetried:
	default:
		fail(w, r, invalid("resolution", "must be NONE, ACKNOWLEDGED or RETRIED"))
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
