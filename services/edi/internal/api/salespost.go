package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/poster"
	"github.com/BenjaminSRussell/passage/services/edi/internal/sources"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

const (
	CodeXrefExists  = "EDI-0019"
	CodeNotPostable = "EDI-0021"
)

type Intake = poster.Intake

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

func (a *API) partnerExists(w http.ResponseWriter, r *http.Request, partnerID string) bool {
	if !validID(partnerID) {
		httpx.Fail(w, r, notFound("partner"))
		return false
	}
	if _, err := a.store.GetPartner(r.Context(), partnerID); err != nil {
		httpx.Fail(w, r, storeProblem(err, "partner"))
		return false
	}
	return true
}

func storeProblem(err error, what string) error {
	if errors.Is(err, store.ErrNotFound) {
		return notFound(what)
	}
	return err
}

func (a *API) listXrefs(w http.ResponseWriter, r *http.Request) {
	partnerID := chi.URLParam(r, "id")
	if !a.partnerExists(w, r, partnerID) {
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
	if !a.partnerExists(w, r, partnerID) {
		return
	}
	d, err := a.store.GetSalesDefaults(r.Context(), partnerID)
	if err != nil {
		httpx.Fail(w, r, storeProblem(err, "sales defaults"))
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

// postMessage posts one inbound 850 or ORDERS to sales (see poster.Post). A
// parked order answers 422 with its park code; once the cause is fixed the same
// route posts it again.
func (a *API) postMessage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("message"))
		return
	}
	ctx := r.Context()
	if rec, err := a.store.GetMessage(ctx, id); err == nil && rec.Standard == "CXML" && rec.Direction == "IN" {
		a.postCXMLMessage(w, r, id)
		return
	}
	res, err := (&poster.Poster{Store: a.store, Intake: a.Intake}).Post(ctx, id)
	var parked *poster.ParkedError
	var notPostable *poster.NotPostableError
	switch {
	case err == nil:
		httpx.JSON(w, http.StatusOK, res)
	case errors.Is(err, poster.ErrNotConfigured):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeSourceDown, "sales intake is not configured"))
	case errors.As(err, &notPostable):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNotPostable, notPostable.Detail))
	case errors.Is(err, poster.ErrNoTradeProfile):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNoTradeProfile, "partner has no trade profile; PUT /partners/{id}/trade-profile first"))
	case errors.Is(err, poster.ErrNoSalesDefaults):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, domain.CodeNoSalesDefaults, "partner has no sales defaults; PUT /partners/{id}/sales-defaults first"))
	case errors.As(err, &parked):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, parked.Code, "order parked: "+parked.Detail))
	case errors.Is(err, sources.ErrForbidden):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, CodeSourceForbidden, "the caller may not place orders for this partner's legal entity"))
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(w, r, notFound("message"))
	default:
		slog.ErrorContext(ctx, "edi post to sales failed", "err", err, "messageId", id, "correlationId", middleware.GetReqID(ctx))
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, CodeSourceDown, "the sales service could not take the order; post again"))
	}
}
