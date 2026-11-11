// Package cxmlmap puts an inbound cXML document of a partner's network onto the services that own the objects (#35, 35-4): a
// ConfirmationRequest that accepts an order as it was sent confirms the purchase order in purchasing, and a ShipNoticeRequest becomes
// an advance ship notice in receiving. Both run under the service principal edi-cxml in the legal entity of the order, with the
// narrow duties of the grants edi -> purchasing and edi -> receiving.
//
// An InvoiceDetailRequest is recorded and published but not put onto payables: payables posts a vendor invoice against receipt lines
// (GR/IR), which a cXML invoice does not name; it is parked with EDI-0038 for matching by an AP clerk.
//
// A document that cannot be mapped (an unknown order, a rejected line, a quantity over what is open) is parked: the message is FAILED
// with the park code and detail, and POST /messages/{id}:post tries it again once the cause is fixed. A transient failure (an outage)
// is returned as an error and leaves the message as it was.
package cxmlmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/sources"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

// ServicePrincipal is the principal the downstream calls run as; its duties are the ones the calls need.
const ServicePrincipal = "edi-cxml"

// Purchasing and Receiving are the downstream calls; sources.Client implements both.
type Purchasing interface {
	PurchaseOrder(ctx context.Context, legalEntityID, id string) (sources.PurchaseOrder, error)
	ConfirmPurchaseOrder(ctx context.Context, legalEntityID, id string, rowVersion int, key string) error
}

type Receiving interface {
	CreateASN(ctx context.Context, legalEntityID, key string, body sources.ASNRequest) (sources.ASNRef, error)
}

var ErrNotConfigured = errors.New("purchasing and receiving are not configured")

// NotMappableError says the message is not a cXML document this package maps.
type NotMappableError struct{ Detail string }

func (e *NotMappableError) Error() string { return e.Detail }

// ParkedError is a message parked instead of mapped.
type ParkedError struct{ domain.Park }

type Result struct {
	MessageID string `json:"messageId"`
	Status    string `json:"status"`
	Kind      string `json:"downstreamKind,omitempty"`
	Ref       string `json:"downstreamRef,omitempty"`
}

type Mapper struct {
	Store      *store.Store
	Purchasing Purchasing
	Receiving  Receiving
}

func asPrincipal(ctx context.Context, legalEntityID string) (context.Context, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return nil, errors.New("cxml mapping: no tenant in context")
	}
	sp, err := tenant.ServicePrincipal(ServicePrincipal, p.TenantID, legalEntityID)
	if err != nil {
		return nil, err
	}
	return tenant.With(ctx, sp), nil
}

// Map puts one inbound cXML message onto its service. A message already POSTED returns its object; a message that is neither MAPPED nor
// parked is not mappable (reprocess a failed mapping first).
func (m *Mapper) Map(ctx context.Context, id string) (Result, error) {
	if m.Purchasing == nil || m.Receiving == nil {
		return Result{}, ErrNotConfigured
	}
	st, err := m.Store.MessageForPost(ctx, id)
	if err != nil {
		return Result{}, err
	}
	if st.Direction != "IN" || st.Standard != "CXML" {
		return Result{}, &NotMappableError{"only an inbound cXML document is put onto purchasing or receiving"}
	}
	if st.Status == "POSTED" {
		kind, ref, err := m.Store.DownstreamOf(ctx, id)
		return Result{MessageID: id, Status: st.Status, Kind: kind, Ref: ref}, err
	}
	if !st.Postable() {
		return Result{}, &NotMappableError{"message is " + st.Status + "; only a mapped or parked document is posted (reprocess a failed mapping first)"}
	}
	var c domain.CXMLCanonical
	if err := json.Unmarshal(st.Canonical, &c); err != nil {
		return Result{}, err
	}
	switch c.DocType {
	case "ConfirmationRequest":
		return m.confirm(ctx, st, c)
	case "ShipNoticeRequest":
		return m.shipNotice(ctx, st, c)
	case "InvoiceDetailRequest":
		return Result{}, m.park(ctx, id, domain.Park{Code: domain.CodeCXMLInvoiceTarget,
			Detail: "invoice " + c.Number + " is recorded; payables posts vendor invoices against receipt lines, which a cXML invoice does not name, so it is matched by an AP clerk"})
	}
	return Result{}, &NotMappableError{c.DocType + " is not put onto a service"}
}

func (m *Mapper) order(ctx context.Context, st store.PostState, orderID string) (store.CXMLOrder, error) {
	o, err := m.Store.GetCXMLOrder(ctx, st.PartnerID, orderID)
	if errors.Is(err, store.ErrNotFound) {
		return o, m.park(ctx, st.ID, domain.Park{Code: domain.CodeCXMLUnknownOrder, Detail: "no purchase order was sent to this partner under orderID " + orderID})
	}
	return o, err
}

func (m *Mapper) confirm(ctx context.Context, st store.PostState, c domain.CXMLCanonical) (Result, error) {
	orderID, park := domain.PlanConfirmation(c)
	if park != nil {
		return Result{}, m.park(ctx, st.ID, *park)
	}
	o, err := m.order(ctx, st, orderID)
	if err != nil {
		return Result{}, err
	}
	cctx, err := asPrincipal(ctx, o.LegalEntityID)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", sources.ErrForbidden, err)
	}
	po, err := m.Purchasing.PurchaseOrder(cctx, o.LegalEntityID, o.PurchaseOrderID)
	if errors.Is(err, sources.ErrNotFound) {
		return Result{}, m.park(ctx, st.ID, domain.Park{Code: domain.CodeCXMLUnknownOrder, Detail: "purchase order " + orderID + " no longer exists in purchasing"})
	}
	if err != nil {
		return Result{}, err
	}
	var open []domain.POLineQty
	for _, l := range po.Lines {
		if l.Status != "CANCELLED" {
			open = append(open, domain.POLineQty{LineNo: l.LineNo, Qty: l.Qty})
		}
	}
	if park := domain.CheckConfirmationCoversOrder(c, open); park != nil {
		return Result{}, m.park(ctx, st.ID, *park)
	}
	// Confirmed already (the buyer confirmed by hand, or an earlier attempt got through before the answer was lost): nothing to do.
	if po.ApprovalStatus != "CONFIRMED" {
		err = m.Purchasing.ConfirmPurchaseOrder(cctx, o.LegalEntityID, o.PurchaseOrderID, po.RowVersion, st.ID)
		var refused *sources.DownstreamRefusedError
		if errors.As(err, &refused) {
			return Result{}, m.park(ctx, st.ID, domain.Park{Code: domain.CodeCXMLDownstream, Detail: "purchasing " + refused.Code + ": " + refused.Detail})
		}
		if err != nil {
			return Result{}, err
		}
	}
	return m.done(ctx, st.ID, "PURCHASE_ORDER", o.PurchaseOrderID)
}

func (m *Mapper) shipNotice(ctx context.Context, st store.PostState, c domain.CXMLCanonical) (Result, error) {
	plan, park := domain.PlanShipNotice(c)
	if park != nil {
		return Result{}, m.park(ctx, st.ID, *park)
	}
	o, err := m.order(ctx, st, plan.OrderID)
	if err != nil {
		return Result{}, err
	}
	cctx, err := asPrincipal(ctx, o.LegalEntityID)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", sources.ErrForbidden, err)
	}
	req := sources.ASNRequest{LegalEntityID: o.LegalEntityID, PurchaseOrderID: o.PurchaseOrderID, TrackingNumber: plan.TrackingNumber, ShipDate: plan.ShipDate}
	for _, l := range plan.Lines {
		req.Lines = append(req.Lines, sources.ASNLine{POLineNo: l.POLineNo, Qty: l.Qty})
	}
	ref, err := m.Receiving.CreateASN(cctx, o.LegalEntityID, st.ID, req)
	var refused *sources.DownstreamRefusedError
	if errors.As(err, &refused) {
		return Result{}, m.park(ctx, st.ID, domain.Park{Code: domain.CodeCXMLDownstream, Detail: "receiving " + refused.Code + ": " + refused.Detail})
	}
	if err != nil {
		return Result{}, err
	}
	return m.done(ctx, st.ID, "ASN", ref.ID)
}

func (m *Mapper) done(ctx context.Context, id, kind, ref string) (Result, error) {
	if err := m.Store.MarkMapped(ctx, id, kind, ref); err != nil {
		return Result{}, fmt.Errorf("record the mapped %s %s: %w", kind, ref, err)
	}
	return Result{MessageID: id, Status: "POSTED", Kind: kind, Ref: ref}, nil
}

func (m *Mapper) park(ctx context.Context, id string, p domain.Park) error {
	if err := m.Store.ParkMessage(ctx, id, p.Code, p.Detail); err != nil {
		return err
	}
	slog.WarnContext(ctx, "edi cxml document parked", "messageId", id, "parkCode", p.Code, "detail", p.Detail)
	return &ParkedError{p}
}
