package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// Slip is set by the event consumer for an 856 or DESADV of one packing slip (never by the route).
	Slip *domain.SlipRef `json:"-"`
	// Warehouse is set by the event consumer for the whole-shipment 856 or DESADV of a shipment the warehouse shipped without a
	// packing slip (never by the route).
	Warehouse *domain.WarehouseShip `json:"-"`
}

// outboundFromSource generates the 856 (or the EDIFACT DESADV) from a
// fulfillment shipment or the 810 (or INVOIC) from a billing invoice. The
// documents are read from their owning services
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
	switch req.TransactionSet {
	case "856", "810", "DESADV", "INVOIC":
	default:
		fields["transactionSet"] = "must be 856, 810, DESADV or INVOIC"
	}
	if !validID(req.SourceID) {
		fields["sourceId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	res, created, err := a.fromSource(r.Context(), partnerID, req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, res)
}

// SendFromSource generates and records the 856, DESADV, 810 or INVOIC of one source document for a partner, as the
// route does; the event consumer calls it. A document already sent returns its interchange with created false.
// A slip (nil for the route's whole-shipment notice) limits the 856 or DESADV to that slip's containers and keys it by shipment
// and slip, so a second slip of the same shipment is sent too.
func (a *API) SendFromSource(ctx context.Context, partnerID, transactionSet, sourceID string, slip *domain.SlipRef) (store.OutboundResult, bool, error) {
	return a.fromSource(ctx, partnerID, sourceRequest{TransactionSet: transactionSet, SourceID: sourceID, Slip: slip})
}

// SendFromWarehouse generates and records the whole-shipment 856 or DESADV of a shipment the warehouse shipped without a packing
// slip (dev.passage.wms.shipment.shipped.v1). It is keyed like the route's whole-shipment notice, so a redelivered event, or the
// route asked later, returns the interchange already recorded.
func (a *API) SendFromWarehouse(ctx context.Context, partnerID, transactionSet, shipmentID string, ws domain.WarehouseShip) (store.OutboundResult, bool, error) {
	return a.fromSource(ctx, partnerID, sourceRequest{TransactionSet: transactionSet, SourceID: shipmentID, Warehouse: &ws})
}

func (a *API) fromSource(ctx context.Context, partnerID string, req sourceRequest) (store.OutboundResult, bool, error) {
	if a.Sources == nil {
		return store.OutboundResult{}, false, httpx.NewProblem(http.StatusServiceUnavailable, CodeSourceDown, "source services are not configured")
	}
	if _, err := a.store.GetPartner(ctx, partnerID); errors.Is(err, store.ErrNotFound) {
		return store.OutboundResult{}, false, notFound("partner")
	} else if err != nil {
		return store.OutboundResult{}, false, err
	}
	profile, err := a.store.GetTradeProfile(ctx, partnerID)
	if errors.Is(err, store.ErrNotFound) {
		return store.OutboundResult{}, false, httpx.NewProblem(http.StatusConflict, CodeNoTradeProfile, "partner has no trade profile; PUT /partners/{id}/trade-profile first")
	}
	if err != nil {
		return store.OutboundResult{}, false, err
	}
	parts := []string{"source", partnerID, req.TransactionSet, req.SourceID}
	if req.Slip != nil && req.Slip.ID != "" {
		parts = append(parts, req.Slip.ID)
	}
	key := strings.Join(parts, ":")
	doc, shipped, err := a.sourceDocument(ctx, req, profile, partnerID, key)
	if err != nil {
		return store.OutboundResult{}, false, sourceProblem(err)
	}
	res, created, err := a.recordOutbound(ctx, key, partnerID, req.TransactionSet, doc)
	if err != nil {
		return res, created, err
	}
	if req.TransactionSet == "856" || req.TransactionSet == "DESADV" {
		// Registered on a replay too (the insert ignores what is there), so a failure after the document was recorded is repaired by asking again.
		if err := a.store.RegisterSelfBillShipments(ctx, partnerID, profile.LegalEntityID, shipped); err != nil {
			return res, created, err
		}
	}
	return res, created, nil
}

// sourceDocument builds the document. An 856 or DESADV lists only the containers this notice owns (ClaimNoticeSSCCs): one another
// notice of the partner already sent, such as the warehouse's whole-shipment notice before a later packing slip, is left out, and a
// notice left with none is not sent.
func (a *API) sourceDocument(ctx context.Context, req sourceRequest, p domain.TradeProfile, partnerID, key string) (json.RawMessage, []domain.SBShipmentLine, error) {
	var (
		doc     any
		err     error
		shipped []domain.SBShipmentLine
	)
	switch req.TransactionSet {
	case "856", "DESADV":
		var sh domain.SourceShipment
		if sh, err = a.Sources.Shipment(ctx, p.LegalEntityID, req.SourceID); err != nil {
			return nil, nil, err
		}
		if req.Warehouse != nil {
			if sh, err = domain.ShippedByWarehouse(p, sh, *req.Warehouse); err != nil {
				return nil, nil, err
			}
		}
		if err = domain.ValidateShipmentSlip(p, sh, req.Slip); err != nil {
			return nil, nil, err
		}
		var o domain.SourceOrder
		if o, err = a.Sources.Order(ctx, p.LegalEntityID, sh.OrderID); err != nil {
			return nil, nil, err
		}
		// The notice must build before it claims its containers, so a document that cannot be sent claims nothing.
		if _, err = domain.ASNFromSlip(p, sh, o, req.Slip); err != nil {
			return nil, nil, err
		}
		slip, wanted := req.Slip, domain.NoticeSSCCs(sh, req.Slip)
		owned, cerr := a.store.ClaimNoticeSSCCs(ctx, partnerID, p.LegalEntityID, req.TransactionSet, key, wanted)
		if cerr != nil {
			return nil, nil, cerr
		}
		if len(owned) == 0 && len(wanted) > 0 {
			return nil, nil, fmt.Errorf("%w: every container of shipment %s was already notified to this partner", domain.ErrOutbound, sh.ShipmentNumber)
		}
		if len(owned) < len(wanted) {
			sh, slip = domain.KeepSSCCs(sh, slip, owned)
		}
		doc, err = domain.ASNFromSlip(p, sh, o, slip)
		if err == nil {
			shipped, err = domain.SelfBillingLinesForSlip(p, sh, o, slip)
		}
	default:
		var inv domain.SourceInvoice
		if inv, err = a.Sources.Invoice(ctx, p.LegalEntityID, req.SourceID); err != nil {
			return nil, nil, err
		}
		if err = domain.ValidateInvoiceSource(p, inv); err != nil {
			return nil, nil, err
		}
		var orderID string
		if orderID, err = domain.InvoiceOrderID(inv); err != nil {
			return nil, nil, err
		}
		var o domain.SourceOrder
		if o, err = a.Sources.Order(ctx, p.LegalEntityID, orderID); err != nil {
			return nil, nil, err
		}
		doc, err = domain.InvoiceFromBilling(p, inv, o)
	}
	if err != nil {
		return nil, nil, err
	}
	raw, err := json.Marshal(doc)
	return raw, shipped, err
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
