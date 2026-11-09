package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/sources"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

const (
	CodeXrefExists  = "EDI-0019"
	CodeNotPostable = "EDI-0021"
)

// Intake places an order with the sales service (POST /v1/sales/orders under
// the service-only duty sales.channel.intake).
type Intake interface {
	PlaceOrder(ctx context.Context, legalEntityID, key string, order domain.SalesOrderRequest) (domain.SalesOrderRef, error)
}

func (a *API) partnerProfile(w http.ResponseWriter, r *http.Request) (string, domain.TradeProfile, bool) {
	partnerID := chi.URLParam(r, "id")
	if !validID(partnerID) {
		httpx.Fail(w, r, notFound("partner"))
		return "", domain.TradeProfile{}, false
	}
	if _, err := a.store.GetPartner(r.Context(), partnerID); err != nil {
		httpx.Fail(w, r, storeProblem(err, "partner"))
		return "", domain.TradeProfile{}, false
	}
	profile, err := a.store.GetTradeProfile(r.Context(), partnerID)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNoTradeProfile, "partner has no trade profile; PUT /partners/{id}/trade-profile first"))
		return "", domain.TradeProfile{}, false
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return "", domain.TradeProfile{}, false
	}
	return partnerID, profile, true
}

func storeProblem(err error, what string) error {
	if errors.Is(err, store.ErrNotFound) {
		return notFound(what)
	}
	return err
}

func (a *API) listXrefs(w http.ResponseWriter, r *http.Request) {
	partnerID := chi.URLParam(r, "id")
	if !validID(partnerID) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	items, err := a.store.ListXrefs(r.Context(), partnerID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) writeXref(w http.ResponseWriter, r *http.Request, save func(partnerID, legalEntityID string, x domain.ItemXref) (domain.ItemXref, error), status int) {
	partnerID, profile, ok := a.partnerProfile(w, r)
	if !ok {
		return
	}
	var x domain.ItemXref
	if err := httpx.Decode(r, &x); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := x.Validate(); len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	saved, err := save(partnerID, profile.LegalEntityID, x)
	switch {
	case errors.Is(err, store.ErrDuplicate):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeXrefExists, "the partner already maps this item reference"))
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(w, r, notFound("item cross-reference"))
	case err != nil:
		httpx.Fail(w, r, err)
	default:
		httpx.JSON(w, status, saved)
	}
}

func (a *API) createXref(w http.ResponseWriter, r *http.Request) {
	a.writeXref(w, r, func(partnerID, le string, x domain.ItemXref) (domain.ItemXref, error) {
		return a.store.CreateXref(r.Context(), partnerID, le, x)
	}, http.StatusCreated)
}

func (a *API) putXref(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "xrefId")
	if !validID(id) {
		httpx.Fail(w, r, notFound("item cross-reference"))
		return
	}
	a.writeXref(w, r, func(partnerID, _ string, x domain.ItemXref) (domain.ItemXref, error) {
		return a.store.UpdateXref(r.Context(), partnerID, id, x)
	}, http.StatusOK)
}

func (a *API) deleteXref(w http.ResponseWriter, r *http.Request) {
	partnerID, id := chi.URLParam(r, "id"), chi.URLParam(r, "xrefId")
	if !validID(partnerID) || !validID(id) {
		httpx.Fail(w, r, notFound("item cross-reference"))
		return
	}
	if err := a.store.DeleteXref(r.Context(), partnerID, id); err != nil {
		httpx.Fail(w, r, storeProblem(err, "item cross-reference"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) putSalesDefaults(w http.ResponseWriter, r *http.Request) {
	partnerID, profile, ok := a.partnerProfile(w, r)
	if !ok {
		return
	}
	var d domain.SalesDefaults
	if err := httpx.Decode(r, &d); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := d.Validate(); len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	saved, err := a.store.PutSalesDefaults(r.Context(), partnerID, profile.LegalEntityID, d)
	if err != nil {
		httpx.Fail(w, r, storeProblem(err, "partner"))
		return
	}
	httpx.JSON(w, http.StatusOK, saved)
}

func (a *API) getSalesDefaults(w http.ResponseWriter, r *http.Request) {
	partnerID := chi.URLParam(r, "id")
	if !validID(partnerID) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	d, err := a.store.GetSalesDefaults(r.Context(), partnerID)
	if err != nil {
		httpx.Fail(w, r, storeProblem(err, "sales defaults"))
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

type postResult struct {
	MessageID    string `json:"messageId"`
	Status       string `json:"status"`
	SalesOrderID string `json:"salesOrderId"`
}

// postMessage turns an inbound 850 (or ORDERS) into a sales order under the
// service grant edi -> sales for sales.channel.intake, in the legal entity of
// the partner's trade profile. The sales request is built from the stored
// canonical order, the partner's item cross-references and defaults; the
// Idempotency-Key is the message id and the externalRef is the partner's PO
// number, so posting twice, or the same PO in a second interchange, yields the
// one sales order. An order that cannot be mapped (a change or cancellation,
// no ship date, an item with no cross-reference) or that sales refuses is
// parked: FAILED with its park code, in the message monitor, and posted again
// by calling this route once the cause is fixed.
func (a *API) postMessage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("message"))
		return
	}
	if a.Intake == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeSourceDown, "sales intake is not configured"))
		return
	}
	ctx := r.Context()
	st, err := a.store.MessageForPost(ctx, id)
	if err != nil {
		httpx.Fail(w, r, storeProblem(err, "message"))
		return
	}
	if st.Direction != "IN" || (st.TransactionSet != "850" && st.TransactionSet != "ORDERS") {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNotPostable, "only an inbound 850 or ORDERS is posted to sales"))
		return
	}
	if st.Status == "POSTED" {
		httpx.JSON(w, http.StatusOK, postResult{MessageID: id, Status: st.Status, SalesOrderID: st.SalesOrderID})
		return
	}
	if !st.Postable() {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNotPostable, "message is "+st.Status+"; only a mapped or parked order is posted (reprocess a failed mapping first)"))
		return
	}
	var order domain.Order
	if err := json.Unmarshal(st.Canonical, &order); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	profile, err := a.store.GetTradeProfile(ctx, st.PartnerID)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNoTradeProfile, "partner has no trade profile; PUT /partners/{id}/trade-profile first"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	defaults, err := a.store.GetSalesDefaults(ctx, st.PartnerID)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, domain.CodeNoSalesDefaults, "partner has no sales defaults; PUT /partners/{id}/sales-defaults first"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	refs := make([]domain.ItemRef, 0, len(order.Lines))
	for _, l := range order.Lines {
		if l.ItemRef != nil {
			refs = append(refs, *l.ItemRef)
		}
	}
	xrefs, err := a.store.LookupXrefs(ctx, st.PartnerID, refs)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req, park := domain.BuildSalesOrder(st.PartnerID, order, profile, defaults, xrefs)
	if park != nil {
		a.park(w, r, id, *park)
		return
	}
	ref, err := a.Intake.PlaceOrder(ctx, profile.LegalEntityID, id, req)
	var refused *sources.RefusedError
	switch {
	case errors.As(err, &refused):
		a.park(w, r, id, domain.Park{Code: domain.CodeSalesRefused, Detail: "sales " + refused.Code + ": " + refused.Detail})
		return
	case errors.Is(err, sources.ErrForbidden):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, CodeSourceForbidden, "the caller may not place orders for this partner's legal entity"))
		return
	case err != nil:
		slog.ErrorContext(ctx, "edi post to sales failed", "err", err, "messageId", id, "correlationId", middleware.GetReqID(ctx))
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, CodeSourceDown, "the sales service could not take the order; post again"))
		return
	}
	if err := a.store.MarkPosted(ctx, id, ref.ID); err != nil {
		slog.ErrorContext(ctx, "edi could not record the posted order", "err", err, "messageId", id, "salesOrderId", ref.ID, "correlationId", middleware.GetReqID(ctx))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, postResult{MessageID: id, Status: "POSTED", SalesOrderID: ref.ID})
}

func (a *API) park(w http.ResponseWriter, r *http.Request, id string, p domain.Park) {
	ctx := r.Context()
	if err := a.store.ParkMessage(ctx, id, p.Code, p.Detail); err != nil {
		slog.ErrorContext(ctx, "edi could not park the order", "err", err, "messageId", id, "correlationId", middleware.GetReqID(ctx))
		httpx.Fail(w, r, storeProblem(err, "message"))
		return
	}
	slog.WarnContext(ctx, "edi order parked", "messageId", id, "parkCode", p.Code, "detail", p.Detail, "correlationId", middleware.GetReqID(ctx))
	httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, p.Code, "order parked: "+p.Detail))
}
