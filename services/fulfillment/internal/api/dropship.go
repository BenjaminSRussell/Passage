package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// dutyVendorConfirm is the duty of the vendor's ship confirmation. Section
// 5.28.5 names it PurchasingConfirm; the seeded purchasing duty for confirming
// purchase orders is PurchaseOrderConfirm (naming is unified under #210).
const dutyVendorConfirm = "PurchaseOrderConfirm"

// routesDropShip mounts the drop-ship flow (FR-28.7). Creating and cancelling
// need WarehouseRelease; the vendor confirmation is a purchasing act that books
// the cost, so it needs the purchasing confirmation duty (section 5.28.5).
func (a *API) routesDropShip(r chi.Router) {
	r.With(authz.RequireDuty("WarehouseRelease")).Post("/v1/fulfillment/drop-ships", a.createDropShip)
	r.With(authz.RequireDuty("WarehouseView")).Get("/v1/fulfillment/drop-ships", a.listDropShips)
	r.With(authz.RequireDuty("WarehouseView")).Get("/v1/fulfillment/drop-ships/{id}", a.getDropShip)
	r.With(authz.RequireDuty(dutyVendorConfirm)).Post("/v1/fulfillment/drop-ships/{id}:confirm", a.confirmDropShip)
	r.With(authz.RequireDuty("WarehouseRelease")).Post("/v1/fulfillment/drop-ships/{id}:cancel", a.cancelDropShip)
	r.With(authz.RequireDuty("WarehouseRelease")).Post("/v1/fulfillment/drop-ships/{id}:close-remainder", a.closeDropShipRemainder)
}

const maxVendorShipmentRef = 64

type dropShipRequest struct {
	LegalEntityID       string `json:"legalEntityId"`
	OrderID             string `json:"orderId"`
	OrderLineID         string `json:"orderLineId"`
	CustomerID          string `json:"customerId"`
	ProductID           string `json:"productId"`
	Unit                string `json:"unit"`
	Qty                 string `json:"qty"`
	VendorID            string `json:"vendorId"`
	PurchaseOrderID     string `json:"purchaseOrderId"`
	PurchaseOrderLineID string `json:"purchaseOrderLineId"`
	UnitCost            string `json:"unitCost"`
	Currency            string `json:"currency"`
	// OrderType is order_type of order.confirmed; a JOURNAL order never ships.
	OrderType string `json:"orderType"`
}

type dropShipConfirmRequest struct {
	Qty               string `json:"qty"`
	VendorShipmentRef string `json:"vendorShipmentRef"`
	ShippedOn         string `json:"shippedOn"`
	PostingDate       string `json:"postingDate"`
}

func (a *API) createDropShip(w http.ResponseWriter, r *http.Request) {
	var req dropShipRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	uuids := []struct{ field, value string }{{"legalEntityId", req.LegalEntityID}, {"orderId", req.OrderID}, {"orderLineId", req.OrderLineID},
		{"customerId", req.CustomerID}, {"productId", req.ProductID}, {"vendorId", req.VendorID}, {"purchaseOrderId", req.PurchaseOrderID},
		{"purchaseOrderLineId", req.PurchaseOrderLineID}}
	for _, u := range uuids {
		if !ids.Valid(u.value) {
			fail(w, r, u.field, "must be a UUID")
			return
		}
	}
	if !unitCode.MatchString(req.Unit) {
		fail(w, r, "unit", "must be 1 to 8 letters or digits")
		return
	}
	if !domain.ValidOrderType(req.OrderType) {
		fail(w, r, "orderType", "must be JOURNAL, ORDER, SUBSCRIPTION or RETURNED when given")
		return
	}
	if req.OrderType == domain.OrderTypeJournal {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeJournalOrder, "FUL-0065: a JOURNAL order never ships, so it has no drop-ship"))
		return
	}
	qty, err := positive(req.Qty)
	if err != nil {
		fail(w, r, "qty", "must be a positive decimal with at most 6 decimals")
		return
	}
	cost, err := positive(req.UnitCost)
	if err != nil {
		fail(w, r, "unitCost", "must be a positive decimal with at most 6 decimals, the purchase order cost")
		return
	}
	if !currencyCode.MatchString(req.Currency) {
		fail(w, r, "currency", "must be a 3-letter ISO currency code")
		return
	}
	d, err := a.store.CreateDropShip(r.Context(), store.DropShipInput{LegalEntityID: req.LegalEntityID, OrderID: req.OrderID, OrderLineID: req.OrderLineID,
		CustomerID: req.CustomerID, ProductID: req.ProductID, Unit: req.Unit, Qty: qty, VendorID: req.VendorID, PurchaseOrderID: req.PurchaseOrderID,
		PurchaseOrderLineID: req.PurchaseOrderLineID, UnitCost: cost, Currency: req.Currency}, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, d.Version)
	httpx.JSON(w, http.StatusCreated, d)
}

func (a *API) listDropShips(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.DropShipFilter{Status: q.Get("status"), OrderID: q.Get("orderId"), VendorID: q.Get("vendorId")}
	switch f.Status {
	case "", domain.DropShipOpen, domain.DropShipPartial, domain.DropShipConfirmed, domain.DropShipCancelled:
	default:
		fail(w, r, "status", "must be OPEN, PARTIALLY_CONFIRMED, CONFIRMED or CANCELLED")
		return
	}
	if f.OrderID != "" && !ids.Valid(f.OrderID) {
		fail(w, r, "orderId", "must be a UUID")
		return
	}
	if f.VendorID != "" && !ids.Valid(f.VendorID) {
		fail(w, r, "vendorId", "must be a UUID")
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	f.Limit = limit
	items, err := a.store.ListDropShips(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.DropShip]{Items: items})
}

func dropShipID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeDropShipNotFound, "drop-ship not found"))
		return "", false
	}
	return id, true
}

func (a *API) getDropShip(w http.ResponseWriter, r *http.Request) {
	id, ok := dropShipID(w, r)
	if !ok {
		return
	}
	d, err := a.store.GetDropShip(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, d.Version)
	httpx.JSON(w, http.StatusOK, d)
}

// dropShipCommand reads the id and the If-Match version of a drop-ship command.
func dropShipCommand(w http.ResponseWriter, r *http.Request) (string, int, bool) {
	id, ok := dropShipID(w, r)
	if !ok {
		return "", 0, false
	}
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	if raw == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, CodePreconditionIf, "If-Match with the drop-ship version is required"))
		return "", 0, false
	}
	version, err := strconv.Atoi(raw)
	if err != nil || version < 1 {
		fail(w, r, "If-Match", "must be the drop-ship version as an integer")
		return "", 0, false
	}
	return id, version, true
}

func (a *API) confirmDropShip(w http.ResponseWriter, r *http.Request) {
	id, version, ok := dropShipCommand(w, r)
	if !ok {
		return
	}
	var req dropShipConfirmRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	shipped, err := time.Parse(dateLayout, req.ShippedOn)
	if err != nil {
		fail(w, r, "shippedOn", "is required, as a date YYYY-MM-DD")
		return
	}
	// The vendor's local date may run a day ahead of UTC, so tomorrow (UTC) is
	// still accepted; anything later is refused.
	if shipped.After(a.now().Truncate(24 * time.Hour).Add(24 * time.Hour)) {
		fail(w, r, "shippedOn", "cannot be in the future")
		return
	}
	in := store.DropShipConfirmInput{VendorShipmentRef: strings.TrimSpace(req.VendorShipmentRef), ShippedOn: shipped}
	if utf8.RuneCountInString(in.VendorShipmentRef) > maxVendorShipmentRef {
		fail(w, r, "vendorShipmentRef", "must be at most 64 characters")
		return
	}
	if req.Qty != "" {
		qty, err := positive(req.Qty)
		if err != nil {
			fail(w, r, "qty", "must be a positive decimal with at most 6 decimals, or left out to confirm everything still open")
			return
		}
		in.Qty = &qty
	}
	if req.PostingDate != "" {
		pd, err := time.Parse(dateLayout, req.PostingDate)
		if err != nil {
			fail(w, r, "postingDate", "must be a date as YYYY-MM-DD")
			return
		}
		in.PostingDate = &pd
	}
	d, err := a.store.ConfirmDropShip(r.Context(), id, version, in, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, d.Version)
	httpx.JSON(w, http.StatusOK, d)
}

func (a *API) cancelDropShip(w http.ResponseWriter, r *http.Request) {
	id, version, ok := dropShipCommand(w, r)
	if !ok {
		return
	}
	_, idem, err := command(r, nil)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	d, err := a.store.CancelDropShip(r.Context(), id, version, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, d.Version)
	httpx.JSON(w, http.StatusOK, d)
}

func (a *API) closeDropShipRemainder(w http.ResponseWriter, r *http.Request) {
	id, version, ok := dropShipCommand(w, r)
	if !ok {
		return
	}
	_, idem, err := command(r, nil)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	d, err := a.store.CloseDropShipRemainder(r.Context(), id, version, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, d.Version)
	httpx.JSON(w, http.StatusOK, d)
}
