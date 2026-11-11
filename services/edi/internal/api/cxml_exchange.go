package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"

	"github.com/BenjaminSRussell/passage/services/edi/internal/cxmlmap"
	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/sources"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

// The cXML exchange (#35, 35-4). Outbound: POST /partners/{id}/cxml:send builds an OrderRequest from a purchase order, or a
// ConfirmationRequest, ShipNoticeRequest or InvoiceDetailRequest from a sales order, shipment or invoice, records it, posts it to the
// partner's network (PUT /partners/{id}/cxml/outbound) and records the network's Response. Inbound: the documents recorded by the public
// listener are put onto purchasing and receiving by POST /messages/{id}:post and, best effort, right after receipt (cxmlmap).

// PurchaseOrders reads a purchase order to send it (purchasing, duty procurement.purchase-order.inquire).
type PurchaseOrders interface {
	PurchaseOrder(ctx context.Context, legalEntityID, id string) (sources.PurchaseOrder, error)
}

type cxmlOutboundBody struct {
	OutboundURL       string `json:"outboundUrl"`
	OurIdentity       string `json:"ourIdentity"`
	OutboundSecretRef string `json:"outboundSecretRef"`
	VendorID          string `json:"vendorId"`
}

func (a *API) putCXMLOutbound(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	var body cxmlOutboundBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if u, err := url.Parse(body.OutboundURL); err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		fields["outboundUrl"] = "must be an https URL without credentials"
	}
	if n := len(strings.TrimSpace(body.OurIdentity)); n < 1 || n > 80 {
		fields["ourIdentity"] = "must be 1 to 80 characters"
	}
	if body.OutboundSecretRef != "" && !domain.ValidSecretRef(body.OutboundSecretRef) {
		fields["outboundSecretRef"] = "must be a secret reference"
	}
	if body.VendorID != "" && !validID(body.VendorID) {
		fields["vendorId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	cfg := store.CXMLOutboundConfig{OutboundURL: body.OutboundURL, OurIdentity: strings.TrimSpace(body.OurIdentity), OutboundSecretRef: body.OutboundSecretRef, VendorID: body.VendorID}
	if err := a.store.PutCXMLOutbound(r.Context(), id, cfg); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, domain.CodeCXMLNotConfigured, "the partner has no cXML identity; PUT /partners/{id}/cxml first"))
			return
		}
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, cfg)
}

func (a *API) getCXMLOutbound(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !validID(id) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	cfg, err := a.store.GetCXMLOutbound(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, storeProblem(err, "cXML identity"))
		return
	}
	httpx.JSON(w, http.StatusOK, cfg)
}

type cxmlSendBody struct {
	DocumentType string `json:"documentType"`
	SourceID     string `json:"sourceId"`
}

// CXMLSendResult is what the send route answers: the recorded document and the network's answer.
type CXMLSendResult struct {
	store.CXMLOutboundRecord
	DocumentType string `json:"documentType"`
	Outcome      string `json:"outcome"`
	StatusCode   int    `json:"statusCode,omitempty"`
	ResponseText string `json:"responseText,omitempty"`
}

func (a *API) sendCXML(w http.ResponseWriter, r *http.Request) {
	partnerID := chi.URLParam(r, "id")
	if !validID(partnerID) {
		httpx.Fail(w, r, notFound("partner"))
		return
	}
	var body cxmlSendBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !slices.Contains(domain.CXMLOutboundTypes, body.DocumentType) {
		fields["documentType"] = "must be " + strings.Join(domain.CXMLOutboundTypes, ", ")
	}
	if !validID(body.SourceID) {
		fields["sourceId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	res, err := a.SendCXML(r.Context(), partnerID, body.DocumentType, body.SourceID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if res.Outcome != string(domain.OutcomeAccepted) {
		status = http.StatusBadGateway
		httpx.Fail(w, r, httpx.NewProblem(status, domain.CodeCXMLSendFailed, "the network did not accept the document ("+res.Outcome+"): "+res.ResponseText+"; the document is recorded as message "+res.MessageID+" and is sent again as a new document"))
		return
	}
	httpx.JSON(w, status, res)
}

// SendCXML builds, records and posts one outbound cXML document. Errors are problems.
func (a *API) SendCXML(ctx context.Context, partnerID, docType, sourceID string) (CXMLSendResult, error) {
	partner, err := a.store.GetPartner(ctx, partnerID)
	if err != nil {
		return CXMLSendResult{}, storeProblem(err, "partner")
	}
	if partner.Status != "active" {
		return CXMLSendResult{}, httpx.NewProblem(http.StatusConflict, CodePartnerInactive, "partner is inactive; no documents can be sent to it")
	}
	cfg, err := a.store.GetPartnerCXML(ctx, partnerID)
	if err != nil {
		return CXMLSendResult{}, httpx.NewProblem(http.StatusConflict, domain.CodeCXMLNotConfigured, "the partner has no cXML identity; PUT /partners/{id}/cxml first")
	}
	out, err := a.store.GetCXMLOutbound(ctx, partnerID)
	if err != nil || out.OutboundURL == "" || out.OurIdentity == "" {
		return CXMLSendResult{}, httpx.NewProblem(http.StatusConflict, domain.CodeCXMLNotConfigured, "the partner has no outbound cXML settings; PUT /partners/{id}/cxml/outbound first")
	}
	secret := ""
	if out.OutboundSecretRef != "" {
		if a.Secrets == nil {
			return CXMLSendResult{}, httpx.NewProblem(http.StatusServiceUnavailable, domain.CodeCXMLNotConfigured, "the shared secret cannot be read: no secrets are mounted")
		}
		raw, err := a.Secrets.Read(out.OutboundSecretRef)
		if err != nil {
			return CXMLSendResult{}, httpx.NewProblem(http.StatusServiceUnavailable, domain.CodeCXMLNotConfigured, "the shared secret cannot be read")
		}
		secret = strings.TrimSpace(string(raw))
	}
	profile, err := a.store.GetTradeProfile(ctx, partnerID)
	if errors.Is(err, store.ErrNotFound) {
		return CXMLSendResult{}, httpx.NewProblem(http.StatusConflict, CodeNoTradeProfile, "partner has no trade profile; PUT /partners/{id}/trade-profile first")
	}
	if err != nil {
		return CXMLSendResult{}, err
	}
	payloadID := ids.New() + "@passage"
	at := a.now().UTC()
	creds := domain.CXMLCredentials{From: out.OurIdentity, To: cfg.NetworkIdentity, Secret: secret}
	doc, orderRef, err := a.buildCXMLDocument(ctx, docType, sourceID, profile, out, creds, payloadID, at)
	if err != nil {
		return CXMLSendResult{}, err
	}
	rec, err := a.store.RecordCXMLOutbound(ctx, store.CXMLOutbound{PartnerID: partnerID, PayloadID: payloadID, DocType: docType, Raw: redactSecret(doc, secret), Order: orderRef})
	if err != nil {
		return CXMLSendResult{}, err
	}
	res := CXMLSendResult{CXMLOutboundRecord: rec, DocumentType: docType}
	code, text, err := a.postCXML(ctx, out.OutboundURL, doc)
	switch {
	case err != nil:
		res.Outcome, res.ResponseText = string(domain.OutcomeRetry), err.Error()
	default:
		res.StatusCode, res.ResponseText = code, text
		res.Outcome = string(domain.ClassifyStatus(code))
	}
	if ferr := a.store.FinishCXMLOutbound(ctx, rec, res.Outcome == string(domain.OutcomeAccepted), res.Outcome+": "+res.ResponseText); ferr != nil {
		slog.ErrorContext(ctx, "cxml outbound result not recorded", "message", rec.MessageID, "err", ferr)
	}
	return res, nil
}

// redactSecret keeps the shared secret out of the stored copy of an outbound document.
func redactSecret(doc, secret string) string {
	if secret == "" {
		return doc
	}
	return strings.ReplaceAll(doc, "<SharedSecret>"+secret+"</SharedSecret>", "<SharedSecret>[redacted]</SharedSecret>")
}

func (a *API) postCXML(ctx context.Context, to, doc string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, to, strings.NewReader(doc))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "text/xml; charset=UTF-8")
	resp, err := a.punchoutClient().Do(req)
	if err != nil {
		slog.WarnContext(ctx, "cxml outbound transport failed", "err", err)
		return 0, "", errors.New("the network could not be reached")
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, "", errors.New("the network's answer could not be read")
	}
	code, text, err := domain.ParseCXMLStatus(raw)
	if err != nil {
		return 0, "", errors.New("the network's answer is not a cXML Response")
	}
	return code, text, nil
}

func (a *API) buildCXMLDocument(ctx context.Context, docType, sourceID string, p domain.TradeProfile, out store.CXMLOutboundConfig, c domain.CXMLCredentials, payloadID string, at time.Time) (string, *store.CXMLOrder, error) {
	bad := func(err error) (string, *store.CXMLOrder, error) {
		var pr *httpx.Problem
		switch {
		case errors.As(err, &pr):
			return "", nil, err
		case errors.Is(err, domain.ErrCXMLOutbound), errors.Is(err, domain.ErrOutbound):
			return "", nil, httpx.NewProblem(http.StatusUnprocessableEntity, CodeOutbound, err.Error())
		case errors.Is(err, domain.ErrSourceNotReady):
			return "", nil, httpx.NewProblem(http.StatusConflict, CodeSourceNotFinal, err.Error())
		}
		return "", nil, sourceProblem(err)
	}
	if docType == domain.CXMLOrderRequest {
		po, ok := a.Sources.(PurchaseOrders)
		if a.Sources == nil || !ok {
			return "", nil, httpx.NewProblem(http.StatusServiceUnavailable, CodeSourceDown, "purchasing is not configured")
		}
		o, err := po.PurchaseOrder(ctx, p.LegalEntityID, sourceID)
		if err != nil {
			return bad(err)
		}
		if o.LegalEntityID != p.LegalEntityID || o.VendorID != out.VendorID || out.VendorID == "" {
			return "", nil, httpx.NewProblem(http.StatusUnprocessableEntity, CodeSourceScope, "the purchase order is not for this partner's legal entity and vendor (set vendorId with PUT /partners/{id}/cxml/outbound)")
		}
		if o.ApprovalStatus != "APPROVED" && o.ApprovalStatus != "CONFIRMED" {
			return "", nil, httpx.NewProblem(http.StatusConflict, CodeSourceNotFinal, "purchase order "+o.Number+" is "+o.ApprovalStatus+"; only an approved order is sent")
		}
		created, _ := time.Parse(time.RFC3339, o.CreatedAt)
		order := domain.CXMLOrder{Number: o.Number, Date: created, Currency: o.Currency, Total: o.Total}
		for _, l := range o.Lines {
			if l.Status == "CANCELLED" {
				continue
			}
			order.Lines = append(order.Lines, domain.CXMLOrderLine{LineNo: l.LineNo, ProductID: l.ProductID, Description: l.Description, Qty: l.Qty, Unit: l.Unit, UnitPrice: l.UnitPrice, DeliveryDate: l.DeliveryDate})
		}
		doc, err := domain.BuildCXMLOrderRequest(c, payloadID, at, order)
		if err != nil {
			return bad(err)
		}
		return doc, &store.CXMLOrder{PartnerID: "", OrderID: o.Number, PurchaseOrderID: o.ID, LegalEntityID: o.LegalEntityID, VendorID: o.VendorID}, nil
	}
	if a.Sources == nil {
		return "", nil, httpx.NewProblem(http.StatusServiceUnavailable, CodeSourceDown, "source services are not configured")
	}
	switch docType {
	case domain.CXMLConfirmationRequest:
		o, err := a.Sources.Order(ctx, p.LegalEntityID, sourceID)
		if err != nil {
			return bad(err)
		}
		if o.LegalEntityID != p.LegalEntityID || o.CustomerID != p.CustomerID {
			return bad(domain.ErrSourceScope)
		}
		doc, err := domain.BuildCXMLConfirmation(c, payloadID, at, o, "C-"+o.ID)
		if err != nil {
			return bad(err)
		}
		return doc, nil, nil
	case domain.CXMLShipNoticeRequest:
		sh, err := a.Sources.Shipment(ctx, p.LegalEntityID, sourceID)
		if err != nil {
			return bad(err)
		}
		if err := domain.ValidateShipmentSource(p, sh); err != nil {
			return bad(err)
		}
		o, err := a.Sources.Order(ctx, p.LegalEntityID, sh.OrderID)
		if err != nil {
			return bad(err)
		}
		doc, err := domain.BuildCXMLShipNotice(c, payloadID, at, sh, o)
		if err != nil {
			return bad(err)
		}
		return doc, nil, nil
	default:
		inv, err := a.Sources.Invoice(ctx, p.LegalEntityID, sourceID)
		if err != nil {
			return bad(err)
		}
		if err := domain.ValidateInvoiceSource(p, inv); err != nil {
			return bad(err)
		}
		orderID, err := domain.InvoiceOrderID(inv)
		if err != nil {
			return bad(err)
		}
		o, err := a.Sources.Order(ctx, p.LegalEntityID, orderID)
		if err != nil {
			return bad(err)
		}
		doc, err := domain.BuildCXMLInvoice(c, payloadID, at, inv, o.ExternalRef)
		if err != nil {
			return bad(err)
		}
		return doc, nil, nil
	}
}

// postCXMLMessage puts an inbound cXML message onto purchasing or receiving (POST /messages/{id}:post).
func (a *API) postCXMLMessage(w http.ResponseWriter, r *http.Request, id string) {
	res, err := a.CXMLMapper.Map(r.Context(), id)
	var parked *cxmlmap.ParkedError
	var notMappable *cxmlmap.NotMappableError
	switch {
	case err == nil:
		httpx.JSON(w, http.StatusOK, res)
	case errors.Is(err, cxmlmap.ErrNotConfigured):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeSourceDown, "purchasing and receiving are not configured"))
	case errors.As(err, &notMappable):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNotPostable, notMappable.Detail))
	case errors.As(err, &parked):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, parked.Code, "document parked: "+parked.Detail))
	case errors.Is(err, sources.ErrForbidden):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, CodeSourceForbidden, "the caller may not act on this partner's legal entity in purchasing or receiving"))
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(w, r, notFound("message"))
	default:
		slog.ErrorContext(r.Context(), "edi cxml mapping failed", "err", err, "messageId", id)
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, CodeSourceDown, "purchasing or receiving could not take the document; post again"))
	}
}

// mapReceivedCXML puts a freshly received document onto its service, best effort: the network already has its 200, so a failure is only
// logged and leaves the message MAPPED for POST /messages/{id}:post.
func (a *API) mapReceivedCXML(ctx context.Context, messageID string) {
	if a.CXMLMapper == nil || messageID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := a.CXMLMapper.Map(ctx, messageID); err != nil {
		slog.InfoContext(ctx, "cxml document not mapped on receipt", "messageId", messageID, "err", err)
	}
}
