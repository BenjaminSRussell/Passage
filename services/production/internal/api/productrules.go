package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/productclient"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
)

// CodeProductUnavailable is the 503 when the released product's order settings cannot be read (FR-21.1).
const CodeProductUnavailable = "PRODUCTION_PRODUCT_UNAVAILABLE"

// WithProducts reads the order quantity rules and the default quantity of a request that names a
// releasedProductId from the product service (FR-21.1, FR-8.4). Without it (PRODUCT_URL unset) such a request
// is refused with 503, and the rules come in the request as before.
func (a *API) WithProducts(r productclient.Resolver) *API {
	a.products = r
	return a
}

// productSettings is the released product's site default for a request; present is false when the request
// named no released product or the product has no default for the site.
type productSettings struct {
	present bool
	def     productclient.SiteDefault
}

// readProductSettings resolves the site default of releasedProductID at siteID. It writes the problem and
// returns false when the request must stop: a malformed id, an unknown released product (422
// PRODUCTION_INVALID_INPUT), an unconfigured or unreachable product service (503). A product with no default
// for the site yields no rules.
func (a *API) readProductSettings(w http.ResponseWriter, r *http.Request, legalEntityID, releasedProductID, siteID string) (productSettings, bool) {
	if releasedProductID == "" {
		return productSettings{}, true
	}
	if !ids.Valid(releasedProductID) {
		invalid(w, r, map[string]string{"releasedProductId": "must be a UUID"})
		return productSettings{}, false
	}
	if a.products == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeProductUnavailable, "releasedProductId needs the product service (PRODUCT_URL); send minimumQty, maximumQty and multiple instead"))
		return productSettings{}, false
	}
	def, err := a.products.SiteDefault(r.Context(), legalEntityID, releasedProductID, siteID)
	switch {
	case err == nil:
		return productSettings{present: true, def: def}, true
	case errors.Is(err, productclient.ErrNoDefault):
		return productSettings{}, true
	case errors.Is(err, productclient.ErrNotFound):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "PRODUCTION_INVALID_INPUT", "releasedProductId is not a released product with settings for this site"))
	default:
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeProductUnavailable, "the product service could not be read for the order quantity rules; retry"))
	}
	return productSettings{}, false
}

// orderSettings resolves the rules that bind a change to an existing order: the released product the order
// was created with (FR-21.1), so a caller cannot skip them by leaving the id out. A request that names
// another released product than the order's is refused (400 on releasedProductId); an order made without
// one takes the request's id, as before. It writes the problem and returns false when the change must stop.
func (a *API) orderSettings(w http.ResponseWriter, r *http.Request, o store.Order, sent string) (productSettings, bool) {
	id := sent
	if o.ReleasedProductID != nil {
		if sent != "" && !strings.EqualFold(sent, *o.ReleasedProductID) {
			invalid(w, r, map[string]string{"releasedProductId": "is not the released product this order was created with"})
			return productSettings{}, false
		}
		id = *o.ReleasedProductID
	}
	return a.readProductSettings(w, r, o.LegalEntityID, id, o.SiteID)
}

// mergeSettings resolves the rules for a merge of orders (FR-21.15, FR-21.1). The orders must not name
// different released products; the one that names a product supplies the rules the summed quantity must
// meet (the store refuses different products again under its locks and the survivor keeps the product).
// Like a quantity change, a merge of an order that holds a product needs the product service (503 without it).
func (a *API) mergeSettings(w http.ResponseWriter, r *http.Request, refs []store.MergeRef) (productSettings, decimal.Decimal, bool) {
	var anchor *store.Order
	total := decimal.Zero
	for _, ref := range refs {
		o, err := a.store.GetOrder(r.Context(), ref.OrderID)
		if err != nil {
			a.refuse(w, r, err)
			return productSettings{}, decimal.Zero, false
		}
		total = total.Add(o.Quantity)
		if o.ReleasedProductID == nil {
			continue
		}
		if anchor != nil && !strings.EqualFold(*anchor.ReleasedProductID, *o.ReleasedProductID) {
			httpx.Fail(w, r, problemFrom(&domain.Error{Code: domain.CodeMergeRefused, Detail: "orders " + anchor.OrderNumber + " and " + o.OrderNumber + " were created with different released products"}))
			return productSettings{}, decimal.Zero, false
		}
		if anchor == nil {
			c := o
			anchor = &c
		}
	}
	if anchor == nil {
		return productSettings{}, total, true
	}
	settings, ok := a.orderSettings(w, r, *anchor, "")
	return settings, total, ok
}

// defaultQuantity is the standard order quantity, used when a create names no quantity.
func (s productSettings) defaultQuantity() decimal.Decimal {
	if !s.present || !s.def.StdQty.IsPositive() {
		return decimal.Zero
	}
	return s.def.StdQty
}

// check enforces the released product's minimum and multiple order quantity on qty (FR-21.1: "multiple/min/max
// enforced"). It applies on top of any minimumQty, maximumQty and multiple in the request, which can tighten the
// rules but never loosen the product's.
func (s productSettings) check(qty decimal.Decimal) error {
	if !s.present {
		return nil
	}
	return domain.CheckOrderQuantity(qty, domain.OrderQuantityRules{Minimum: s.def.MinQty, Multiple: s.def.MultipleQty})
}

// splitRules makes the split's minimum and multiple at least the product's (PROD-0015): the higher minimum
// applies, and the product's multiple replaces the request's. A different multiple in the request still binds,
// so a split quantity that breaks it is returned as an error here.
func (s productSettings) splitRules(splitQty, minQty, multiple decimal.Decimal) (decimal.Decimal, decimal.Decimal, error) {
	if !s.present {
		return minQty, multiple, nil
	}
	if s.def.MinQty.GreaterThan(minQty) {
		minQty = s.def.MinQty
	}
	if s.def.MultipleQty.IsPositive() {
		if multiple.IsPositive() && !splitQty.Mod(multiple).IsZero() {
			return minQty, multiple, &domain.Error{Code: domain.CodeSplitQuantity, Detail: "split quantity " + splitQty.String() + " is not a multiple of " + multiple.String()}
		}
		multiple = s.def.MultipleQty
	}
	return minQty, multiple, nil
}
