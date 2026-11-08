package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// parametersPath serves the pricing parameters of one legal entity (PRD 5.26.13). Reading
// takes pricing.view; changing takes pricing.admin (PricingAdmin, with the procedures).
const parametersPath = "/v1/pricing/parameters"

// parametersRequest is the PUT body. The PUT replaces every value, so each one is required:
// a missing journalFourEyes must not save as false.
type parametersRequest struct {
	LegalEntityID   string `json:"legalEntityId"`
	JournalFourEyes *bool  `json:"journalFourEyes"`
	// PricingDateBasis, TotalDiscountBasis and SmartRoundingBands are required too (the PUT
	// replaces every value).
	PricingDateBasis   *string           `json:"pricingDateBasis"`
	TotalDiscountBasis *string           `json:"totalDiscountBasis"`
	SmartRoundingBands []domain.BandSpec `json:"smartRoundingBands"`
	// PriceSearchMode (FIRST_MATCH or SEARCH_NEXT) and SmallOrderThresholds (currency to
	// decimal string; an empty object is the reference table) are required too.
	PriceSearchMode      *string           `json:"priceSearchMode"`
	SmallOrderThresholds map[string]string `json:"smallOrderThresholds"`
	// FreeGoodsCalcRule (PROPORTIONAL, UNIT_REFERENCE or WHOLE_UNITS) is required too.
	FreeGoodsCalcRule *string `json:"freeGoodsCalcRule"`
	// Version is the version read (0 when the legal entity has no stored parameters).
	Version int `json:"version"`
}

type parametersView struct {
	LegalEntityID   string `json:"legalEntityId"`
	JournalFourEyes bool   `json:"journalFourEyes"`
	// PricingDateBasis is SHIP_DATE, ORDER_DATE or DELIVERY_DATE; TotalDiscountBasis is
	// AFTER_LINE_DISC or GROSS; SmartRoundingBands is the effective table (the default when
	// none is stored).
	PricingDateBasis   string            `json:"pricingDateBasis"`
	TotalDiscountBasis string            `json:"totalDiscountBasis"`
	SmartRoundingBands []domain.BandSpec `json:"smartRoundingBands"`
	// PriceSearchMode is the default of searchNext on a new condition record; the
	// SmallOrderThresholds of a currency replace the reference threshold (250 USD, 200 EUR).
	PriceSearchMode      string            `json:"priceSearchMode"`
	SmallOrderThresholds map[string]string `json:"smallOrderThresholds"`
	// FreeGoodsCalcRule is the calculation rule a new free goods rule takes when its create
	// request names none (BR-26.13).
	FreeGoodsCalcRule string     `json:"freeGoodsCalcRule"`
	Version           int        `json:"version"`
	UpdatedBy         string     `json:"updatedBy,omitempty"`
	UpdatedAt         *time.Time `json:"updatedAt,omitempty"`
}

func parametersResponse(p store.Parameters) parametersView {
	if p.PricingDateBasis == "" { // a store with nothing for the entity
		d := store.DefaultParameters(p.LegalEntityID)
		p.PricingDateBasis, p.TotalDiscountBasis, p.SmartRoundingBands = d.PricingDateBasis, d.TotalDiscountBasis, d.SmartRoundingBands
		p.PriceSearchMode, p.FreeGoodsCalcRule = d.PriceSearchMode, d.FreeGoodsCalcRule
	}
	if p.SmallOrderThresholds == nil {
		p.SmallOrderThresholds = map[string]string{}
	}
	return parametersView{LegalEntityID: p.LegalEntityID, JournalFourEyes: p.JournalFourEyes, PricingDateBasis: p.PricingDateBasis,
		TotalDiscountBasis: p.TotalDiscountBasis, SmartRoundingBands: p.SmartRoundingBands, PriceSearchMode: p.PriceSearchMode,
		SmallOrderThresholds: p.SmallOrderThresholds, FreeGoodsCalcRule: p.FreeGoodsCalcRule, Version: p.Version, UpdatedBy: p.UpdatedBy, UpdatedAt: p.UpdatedAt}
}

func (a *API) getParameters(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	if !legalEntityInScope(w, r, le) {
		return
	}
	p, err := a.uncached().GetParameters(r.Context(), le)
	if err != nil {
		slog.Error("get pricing parameters failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, parametersResponse(p))
}

func (a *API) setParameters(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req parametersRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if req.JournalFourEyes == nil {
		fields["journalFourEyes"] = "is required"
	}
	if req.PricingDateBasis == nil {
		fields["pricingDateBasis"] = "is required"
	} else if !domain.ValidDateBasis(*req.PricingDateBasis) {
		fields["pricingDateBasis"] = "must be SHIP_DATE, ORDER_DATE or DELIVERY_DATE"
	}
	if req.TotalDiscountBasis == nil {
		fields["totalDiscountBasis"] = "is required"
	} else if !domain.ValidTotalBasis(*req.TotalDiscountBasis) {
		fields["totalDiscountBasis"] = "must be AFTER_LINE_DISC or GROSS"
	}
	if req.SmartRoundingBands == nil {
		fields["smartRoundingBands"] = "is required"
	} else if _, faults := domain.ParseBands(req.SmartRoundingBands); faults != nil {
		for k, v := range faults {
			fields[k] = v
		}
	}
	if req.PriceSearchMode == nil {
		fields["priceSearchMode"] = "is required"
	} else if !domain.ValidSearchMode(*req.PriceSearchMode) {
		fields["priceSearchMode"] = "must be FIRST_MATCH or SEARCH_NEXT"
	}
	if req.SmallOrderThresholds == nil {
		fields["smallOrderThresholds"] = "is required (an empty object keeps the reference thresholds)"
	} else if _, faults := domain.ParseThresholds(req.SmallOrderThresholds); faults != nil {
		for k, v := range faults {
			fields[k] = v
		}
	}
	if req.FreeGoodsCalcRule == nil {
		fields["freeGoodsCalcRule"] = "is required"
	} else if !domain.ValidCalcRule(*req.FreeGoodsCalcRule) {
		fields["freeGoodsCalcRule"] = "must be PROPORTIONAL, UNIT_REFERENCE or WHOLE_UNITS"
	}
	if req.Version < 0 {
		fields["version"] = "must be the version read, or 0 when none was stored"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !legalEntityInScope(w, r, req.LegalEntityID) {
		return
	}
	p, err := a.store.SetParameters(r.Context(), store.Parameters{LegalEntityID: req.LegalEntityID,
		JournalFourEyes: *req.JournalFourEyes, PricingDateBasis: *req.PricingDateBasis, TotalDiscountBasis: *req.TotalDiscountBasis,
		SmartRoundingBands: req.SmartRoundingBands, PriceSearchMode: *req.PriceSearchMode,
		SmallOrderThresholds: req.SmallOrderThresholds, FreeGoodsCalcRule: *req.FreeGoodsCalcRule}, req.Version, r.Header.Get(idempotencyKey))
	if err != nil {
		slog.Error("set pricing parameters failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, parametersResponse(p))
}

// legalEntityInScope refuses a legal entity outside the caller's scope (403 AUTH-0003). Row
// security would hide its stored parameters, so a read would show the defaults and a write
// would fail as a version conflict or a policy violation; the scope check says why instead.
func legalEntityInScope(w http.ResponseWriter, r *http.Request, legalEntityID string) bool {
	p, _ := tenant.FromContext(r.Context())
	if tenant.InLegalEntityScope(p, legalEntityID) {
		return true
	}
	httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity is outside the caller's scope"))
	return false
}

// registerParameterRoutes mounts the parameter endpoints.
func (a *API) registerParameterRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyView)).Get(parametersPath, a.getParameters)
	r.With(authz.RequireDuty(dutyAdmin)).Put(parametersPath, a.setParameters)
	r.With(authz.RequireDuty(dutyView)).Get(tenantParametersPath, a.getTenantParameters)
	r.With(authz.RequireDuty(dutyAdmin)).Put(tenantParametersPath, a.setTenantParameters)
}

// uncached is the store behind the replica's cache. The parameter pages read through it, so the
// version an administrator edits is the stored one even when another replica changed it moments
// ago (a cached copy could be up to its lifetime old and make the next PUT a 412).
func (a *API) uncached() store.Store {
	if c, ok := a.store.(*store.Cache); ok && c.Store != nil {
		return c.Store
	}
	return a.store
}

// searchNextDefault is the value of searchNext a new condition record takes when the caller
// sends none: true under the legal entity's price_search_mode SEARCH_NEXT (BR-26.1 step 5).
func (a *API) searchNextDefault(r *http.Request, legalEntityID string) (bool, error) {
	if !ids.Valid(legalEntityID) {
		return false, nil
	}
	p, err := a.entityParameters(r, legalEntityID)
	return p.PriceSearchMode == domain.SearchModeNext, err
}

// entityParameters reads the parameters the engine needs for a legal entity: one primary
// key lookup per resolve. The caller has checked the legal entity is a UUID.
func (a *API) entityParameters(r *http.Request, legalEntityID string) (store.Parameters, error) {
	return a.store.GetParameters(r.Context(), legalEntityID)
}

// pricingDateFor returns the pricing date of a resolve, simulate or compare request (BR-26.2):
// the pricingDate the caller named, else the document date the legal entity's
// pricing_date_basis selects (shipDate, orderDate or deliveryDate). The caller's explicit
// pricingDate always wins. Faults are keyed by the document date field at fault, or by
// pricingDate when the basis date is missing.
func pricingDateFor(req resolveRequest, p store.Parameters) (string, map[string]string) {
	fields := map[string]string{}
	for name, v := range map[string]string{"orderDate": req.OrderDate, "shipDate": req.ShipDate, "deliveryDate": req.DeliveryDate} {
		if v == "" {
			continue
		}
		if _, err := time.Parse(dateLayout, v); err != nil {
			fields[name] = "must be a date in YYYY-MM-DD form"
		}
	}
	date := req.PricingDate
	if date == "" {
		basis, field, v := domain.DateBasisShip, "shipDate", req.ShipDate
		switch p.PricingDateBasis {
		case domain.DateBasisOrder:
			basis, field, v = domain.DateBasisOrder, "orderDate", req.OrderDate
		case domain.DateBasisDelivery:
			basis, field, v = domain.DateBasisDelivery, "deliveryDate", req.DeliveryDate
		}
		if v == "" {
			fields["pricingDate"] = "is required unless " + field + " is sent (pricing date basis " + basis + ")"
		}
		date = v
	}
	if len(fields) == 0 {
		return date, nil
	}
	return date, fields
}

// documentInput validates a resolve, simulate or compare request and builds the document to
// price, with the pricing date chosen by pricingDateFor.
func (a *API) documentInput(req resolveRequest, p store.Parameters) (domain.Header, []domain.Line, *httpx.Problem) {
	date, faults := pricingDateFor(req, p)
	sent := req.PricingDate != ""
	req.PricingDate = date
	header, lines, problem := a.buildInput(req)
	if faults == nil {
		return header, lines, problem
	}
	if problem == nil {
		return header, lines, validation(faults)
	}
	if problem.Errors != nil {
		if !sent {
			// The date came from a document date: its fault is reported on that field.
			delete(problem.Errors, "pricingDate")
		}
		for k, v := range faults {
			problem.Errors[k] = v
		}
	}
	return header, lines, problem
}
