package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/sources"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

type Sources interface {
	Shipment(ctx context.Context, legalEntityID, id string) (domain.SourceShipment, error)
	Invoice(ctx context.Context, legalEntityID, id string) (domain.SourceInvoice, error)
	Order(ctx context.Context, legalEntityID, id string) (domain.SourceOrder, error)
}

func (a *API) putTradeProfile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	var p domain.TradeProfile
	if err := httpx.Decode(r, &p); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := p.Validate()
	if _, bad := fields["legalEntityId"]; !bad {
		if caller, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(caller, p.LegalEntityID) {
			fields["legalEntityId"] = "must be a legal entity of the caller"
		}
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	saved, err := a.store.PutTradeProfile(r.Context(), id, p)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, saved)
}

type sourceRequest struct {
	TransactionSet string `json:"transactionSet"`
	SourceID       string `json:"sourceId"`
}

// outboundFromSource generates the 856 from a fulfillment shipment or the 810
// from a billing invoice. The documents are read from their owning services
// under a service grant limited to the read duty, for the legal entity of the
// partner's trade profile, and must belong to that legal entity and customer.
// Generation is idempotent per source document: asking again returns the
// interchange already recorded and never allocates a second control number.
func (a *API) outboundFromSource(w http.ResponseWriter, r *http.Request) {
	partnerID := chi.URLParam(r, "id")
	if !validID(partnerID) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	var req sourceRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.TransactionSet != "856" && req.TransactionSet != "810" {
		fields["transactionSet"] = "must be 856 or 810"
	}
	if !validID(req.SourceID) {
		fields["sourceId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	if a.Sources == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeSourceDown, "source services are not configured"))
		return
	}
	if _, err := a.store.GetPartner(r.Context(), partnerID); errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, notFound("partner"))
		return
	} else if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	profile, err := a.store.GetTradeProfile(r.Context(), partnerID)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNoTradeProfile, "partner has no trade profile; PUT /partners/{id}/trade-profile first"))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	doc, err := a.sourceDocument(r.Context(), req, profile)
	if err != nil {
		httpx.Fail(w, r, sourceProblem(err))
		return
	}
	key := strings.Join([]string{"source", partnerID, req.TransactionSet, req.SourceID}, ":")
	a.sendOutbound(w, r, key, partnerID, req.TransactionSet, doc)
}

func (a *API) sourceDocument(ctx context.Context, req sourceRequest, p domain.TradeProfile) (json.RawMessage, error) {
	var (
		doc any
		err error
	)
	switch req.TransactionSet {
	case "856":
		var sh domain.SourceShipment
		if sh, err = a.Sources.Shipment(ctx, p.LegalEntityID, req.SourceID); err != nil {
			return nil, err
		}
		if err = domain.ValidateShipmentSource(p, sh); err != nil {
			return nil, err
		}
		var o domain.SourceOrder
		if o, err = a.Sources.Order(ctx, p.LegalEntityID, sh.OrderID); err != nil {
			return nil, err
		}
		doc, err = domain.ASNFromShipment(p, sh, o)
	default:
		var inv domain.SourceInvoice
		if inv, err = a.Sources.Invoice(ctx, p.LegalEntityID, req.SourceID); err != nil {
			return nil, err
		}
		if err = domain.ValidateInvoiceSource(p, inv); err != nil {
			return nil, err
		}
		var orderID string
		if orderID, err = domain.InvoiceOrderID(inv); err != nil {
			return nil, err
		}
		var o domain.SourceOrder
		if o, err = a.Sources.Order(ctx, p.LegalEntityID, orderID); err != nil {
			return nil, err
		}
		doc, err = domain.InvoiceFromBilling(p, inv, o)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(doc)
}

func sourceProblem(err error) *httpx.Problem {
	switch {
	case errors.Is(err, sources.ErrNotFound):
		return notFound("source document")
	case errors.Is(err, sources.ErrForbidden):
		return httpx.NewProblem(http.StatusForbidden, CodeSourceForbidden, "the caller may not read this partner's legal entity from the source services")
	case errors.Is(err, domain.ErrSourceScope):
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeSourceScope, err.Error())
	case errors.Is(err, domain.ErrSourceNotReady):
		return httpx.NewProblem(http.StatusConflict, CodeSourceNotFinal, err.Error())
	case errors.Is(err, domain.ErrOutbound):
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodeOutbound, err.Error())
	}
	return httpx.NewProblem(http.StatusBadGateway, CodeSourceDown, "a source service could not be read")
}
