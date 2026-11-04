package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// routesDeliveryTolerance mounts the over-delivery tolerance per product and
// customer (BR-28.7, FUL-0005).
func (a *API) routesDeliveryTolerance(r chi.Router) {
	r.With(authz.RequireDuty("WarehouseView")).Get("/v1/fulfillment/delivery-tolerances", a.listDeliveryTolerances)
	r.With(authz.RequireDuty("PackingRuleMaintain")).Put("/v1/fulfillment/delivery-tolerances", a.saveDeliveryTolerance)
}

type deliveryToleranceRequest struct {
	ProductID       *string `json:"productId"`
	CustomerID      *string `json:"customerId"`
	OverdeliveryPct string  `json:"overdeliveryPct"`
}

func (a *API) listDeliveryTolerances(w http.ResponseWriter, r *http.Request) {
	productID, customerID := strings.ToLower(r.URL.Query().Get("productId")), strings.ToLower(r.URL.Query().Get("customerId"))
	if productID != "" && !ids.Valid(productID) {
		fail(w, r, "productId", "must be a UUID")
		return
	}
	if customerID != "" && !ids.Valid(customerID) {
		fail(w, r, "customerId", "must be a UUID")
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListDeliveryTolerances(r.Context(), productID, customerID, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.DeliveryTolerance]{Items: items})
}

func (a *API) saveDeliveryTolerance(w http.ResponseWriter, r *http.Request) {
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	if raw == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, CodePreconditionIf, "If-Match with the tolerance version is required (0 when the scope has none)"))
		return
	}
	version, err := strconv.Atoi(raw)
	if err != nil || version < 0 {
		fail(w, r, "If-Match", "must be the tolerance version as an integer, 0 when the scope has none")
		return
	}
	var req deliveryToleranceRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := store.DeliveryToleranceInput{}
	if req.ProductID != nil && *req.ProductID != "" {
		if !ids.Valid(*req.ProductID) {
			fail(w, r, "productId", "must be a UUID when given")
			return
		}
		p := strings.ToLower(*req.ProductID)
		in.ProductID = &p
	}
	if req.CustomerID != nil && *req.CustomerID != "" {
		if !ids.Valid(*req.CustomerID) {
			fail(w, r, "customerId", "must be a UUID when given")
			return
		}
		c := strings.ToLower(*req.CustomerID)
		in.CustomerID = &c
	}
	pct, err := decimal.NewFromString(req.OverdeliveryPct)
	if err != nil || pct.IsNegative() || pct.GreaterThan(decimal.NewFromInt(100)) || !pct.Equal(pct.Round(4)) {
		fail(w, r, "overdeliveryPct", "must be a decimal percentage from 0 to 100 with at most 4 decimals")
		return
	}
	in.OverdeliveryPct = pct
	t, err := a.store.SaveDeliveryTolerance(r.Context(), in, version, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, t.Version)
	httpx.JSON(w, http.StatusOK, t)
}
