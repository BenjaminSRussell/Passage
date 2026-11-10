package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

const (
	dutyVendorPortalRead        = "procurement.vendor-portal.read"
	dutyVendorPortalAcknowledge = "procurement.vendor-portal.acknowledge"
)

// VendorPrincipal names the one vendor a verified caller acts for. The vendor id must come from the
// caller's verified credentials and never from a header, query parameter or body; an implementation
// returns false for a caller that is not a supplier account.
type VendorPrincipal interface {
	VendorID(ctx context.Context) (string, bool)
}

// NoVendorPrincipal is the default: no credential in this service carries a vendor yet, so the vendor
// routes refuse every caller until the supplier portal's principal is wired in with WithVendorPrincipal.
type NoVendorPrincipal struct{}

// VendorID implements VendorPrincipal.
func (NoVendorPrincipal) VendorID(context.Context) (string, bool) { return "", false }

// WithVendorPrincipal replaces the source of the vendor a caller acts for.
func (a *API) WithVendorPrincipal(v VendorPrincipal) *API {
	a.vendors = v
	return a
}

func (a *API) vendorOf(r *http.Request) (string, error) {
	id, ok := a.vendors.VendorID(r.Context())
	if !ok || !ids.Valid(id) {
		return "", httpx.NewProblem(http.StatusForbidden, codeScope, "this account is not a supplier account")
	}
	return id, nil
}

func (a *API) registerAcknowledgementRoutes(r chi.Router) {
	inquire := r.With(authz.RequireDuty(dutyPurchaseOrderInquire))
	inquire.Get("/acknowledgements/awaiting", a.listAwaitingAcknowledgement)
	inquire.Get("/acknowledgements", a.listAcknowledgements)
	inquire.Get("/purchase-orders/{id}/acknowledgements", a.listOrderAcknowledgements)
	r.Post("/acknowledgements/{id}", a.acknowledgementAction)
	r.With(authz.RequireDuty(dutyVendorPortalRead)).Get("/vendor/purchase-orders", a.vendorListOrders)
	r.With(authz.RequireDuty(dutyVendorPortalRead)).Get("/vendor/purchase-orders/{id}", a.vendorGetOrder)
	r.With(authz.RequireDuty(dutyVendorPortalAcknowledge)).Post("/vendor/purchase-orders/{id}/acknowledgements", a.vendorAcknowledge)
}

type vendorLineView struct {
	LineNo       int             `json:"lineNo"`
	ProductID    string          `json:"productId,omitempty"`
	Description  string          `json:"description,omitempty"`
	Qty          decimal.Decimal `json:"qty"`
	Unit         string          `json:"unit"`
	UnitPrice    decimal.Decimal `json:"unitPrice"`
	PriceUnit    decimal.Decimal `json:"priceUnit"`
	DiscountPct  decimal.Decimal `json:"discountPct"`
	LineAmount   decimal.Decimal `json:"lineAmount"`
	TaxAmount    decimal.Decimal `json:"taxAmount"`
	DeliveryDate domain.Day      `json:"deliveryDate"`
	ReceivedQty  decimal.Decimal `json:"receivedQty"`
	Status       string          `json:"status"`
}

type vendorChargeView struct {
	Calc   string          `json:"calc"`
	Value  decimal.Decimal `json:"value"`
	Amount decimal.Decimal `json:"amount"`
	LineNo int             `json:"lineNo,omitempty"`
}

// vendorOrderView is what a vendor sees of an order: the commercial content only, with none of the
// buyer's users, accounts, requisition or agreement links.
type vendorOrderView struct {
	ID               string                    `json:"id"`
	Number           string                    `json:"number"`
	Currency         string                    `json:"currency"`
	ApprovalStatus   string                    `json:"approvalStatus"`
	VersionNo        int                       `json:"versionNo"`
	Subtotal         decimal.Decimal           `json:"subtotal"`
	DiscountTotal    decimal.Decimal           `json:"discountTotal"`
	ChargesTotal     decimal.Decimal           `json:"chargesTotal"`
	TaxTotal         decimal.Decimal           `json:"taxTotal"`
	Total            decimal.Decimal           `json:"total"`
	Lines            []vendorLineView          `json:"lines"`
	Charges          []vendorChargeView        `json:"charges"`
	Acknowledgements []store.POAcknowledgement `json:"acknowledgements"`
}

func (a *API) vendorListOrders(w http.ResponseWriter, r *http.Request) {
	vendorID, err := a.vendorOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	page, err := a.store.ListVendorPurchaseOrders(r.Context(), vendorID, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

func (a *API) vendorGetOrder(w http.ResponseWriter, r *http.Request) {
	vendorID, err := a.vendorOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	po, err := a.store.GetVendorPurchaseOrder(r.Context(), vendorID, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	acks, err := a.store.ListAcknowledgements(r.Context(), store.AckFilter{PurchaseOrderID: id}, "", store.MaxPageSize)
	if err != nil {
		fail(w, r, err)
		return
	}
	view := vendorOrderView{ID: po.ID, Number: po.Number, Currency: po.Currency, ApprovalStatus: po.ApprovalStatus, VersionNo: po.VersionNo,
		Subtotal: po.Subtotal, DiscountTotal: po.DiscountTotal, ChargesTotal: po.ChargesTotal, TaxTotal: po.TaxTotal, Total: po.Total,
		Lines: []vendorLineView{}, Charges: []vendorChargeView{}, Acknowledgements: acks.Items}
	for _, l := range po.Lines {
		view.Lines = append(view.Lines, vendorLineView{LineNo: l.LineNo, ProductID: l.ProductID, Description: l.Description, Qty: l.Qty,
			Unit: l.Unit, UnitPrice: l.UnitPrice, PriceUnit: l.PriceUnit, DiscountPct: l.DiscountPct, LineAmount: l.LineAmount,
			TaxAmount: l.TaxAmount, DeliveryDate: l.DeliveryDate, ReceivedQty: l.ReceivedQty, Status: l.Status})
	}
	for _, c := range po.Charges {
		view.Charges = append(view.Charges, vendorChargeView{Calc: c.Calc, Value: c.Value, Amount: c.Amount, LineNo: c.LineNo})
	}
	for i := range view.Acknowledgements {
		view.Acknowledgements[i].AcknowledgedBy, view.Acknowledgements[i].DecidedBy = "", ""
	}
	httpx.JSON(w, http.StatusOK, view)
}

// vendorAcknowledge handles POST /vendor/purchase-orders/{id}/acknowledgements: the vendor accepts or
// rejects the current version of an order, or proposes line changes. The vendor is the caller's verified
// vendor; an order of another vendor is 404.
func (a *API) vendorAcknowledge(w http.ResponseWriter, r *http.Request) {
	vendorID, err := a.vendorOf(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var in domain.AckInput
	if err := httpx.Decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if err := in.Validate(); err != nil {
		fail(w, r, err)
		return
	}
	idem := store.Idem{Key: key, Hash: hashBody("po-ack", map[string]any{"id": id, "answer": in})}
	out, err := a.store.AcknowledgePurchaseOrder(r.Context(), vendorID, id, in, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.poAcknowledged.Add(1)
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) listAwaitingAcknowledgement(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	page, err := a.store.ListAwaitingAcknowledgement(r.Context(), after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

func (a *API) listAcknowledgements(w http.ResponseWriter, r *http.Request) {
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.AckFilter{PurchaseOrderID: q.Get("purchaseOrderId"), Outcome: q.Get("outcome"), OpenProposals: q.Get("pending") == "true"}
	switch f.Outcome {
	case "", domain.AckAccepted, domain.AckRejected, domain.AckChangesProposed:
	default:
		fail(w, r, invalid("outcome", "must be ACCEPTED, REJECTED or CHANGES_PROPOSED"))
		return
	}
	if err := uuidField("purchaseOrderId", f.PurchaseOrderID, false); err != nil {
		fail(w, r, err)
		return
	}
	page, err := a.store.ListAcknowledgements(r.Context(), f, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

func (a *API) listOrderAcknowledgements(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	limit, after, err := listParams(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	page, err := a.store.ListAcknowledgements(r.Context(), store.AckFilter{PurchaseOrderID: id}, after, limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	writePage(w, page)
}

// acknowledgementAction handles POST /acknowledgements/{id}:approve|:decline, a buyer's decision on a
// vendor's proposal. Both need procurement.purchase-order.confirm and the proposal's legal entity in the
// caller's scope.
func (a *API) acknowledgementAction(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "id"), ":")
	if !found || (action != "approve" && action != "decline") || !ids.Valid(id) {
		fail(w, r, httpx.NewProblem(http.StatusNotFound, codeUnknownAction, "unknown acknowledgement action"))
		return
	}
	if !p.HasDuty(dutyPurchaseOrderConfirm) {
		fail(w, r, httpx.NewProblem(http.StatusForbidden, codeScope, "missing duty "+dutyPurchaseOrderConfirm))
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
	cur, err := a.store.GetAcknowledgement(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if err := inScope(p, cur.LegalEntityID); err != nil {
		fail(w, r, err)
		return
	}
	idem := store.Idem{Key: key, Hash: hashBody("po-ack."+action, map[string]any{"id": id, "note": note})}
	out, err := a.store.DecideAcknowledgement(r.Context(), id, action == "approve", note, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.poProposalsDecided.Add(1)
	httpx.JSON(w, http.StatusOK, out)
}
