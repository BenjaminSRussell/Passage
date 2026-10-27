package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

var (
	countryRE  = regexp.MustCompile(`^[A-Z]{2}$`)
	periodRE   = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)
	materialRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)
	typeCodeRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	wholeRE    = regexp.MustCompile(`^[0-9]{1,9}$`)
)

// routesPacking mounts the packing configuration, auto-pack, packaging fee and
// returnable packaging endpoints (FR-28.14 to FR-28.16). Duties follow section
// 5.28.17: PackingRuleMaintain for container types, rules and packaging
// materials; PackagingFeeMaintain for rates and the statement; WarehouseReceive
// for return receipts.
func (a *API) routesPacking(r chi.Router) {
	r.With(authz.RequireDuty("WarehouseView")).Get("/api/v1/fulfillment/container-types", a.listContainerTypes)
	r.With(authz.RequireDuty("PackingRuleMaintain")).Post("/api/v1/fulfillment/container-types", a.saveContainerType)
	r.With(authz.RequireDuty("WarehouseView")).Get("/api/v1/fulfillment/packing-rules", a.listPackingRules)
	r.With(authz.RequireDuty("PackingRuleMaintain")).Post("/api/v1/fulfillment/packing-rules", a.savePackingRule)
	r.With(authz.RequireDuty("WarehouseView")).Get("/api/v1/fulfillment/product-packaging", a.listProductPackaging)
	r.With(authz.RequireDuty("PackingRuleMaintain")).Post("/api/v1/fulfillment/product-packaging", a.saveProductPackaging)
	r.With(authz.RequireDuty("WarehousePack")).Post("/api/v1/fulfillment/shipments/{id}:auto-pack", a.autoPack)
	r.With(authz.RequireDuty("PackagingFeeMaintain")).Get("/api/v1/fulfillment/packaging-fee-rates", a.listFeeRates)
	r.With(authz.RequireDuty("PackagingFeeMaintain")).Post("/api/v1/fulfillment/packaging-fee-rates", a.saveFeeRate)
	r.With(authz.RequireDuty("PackagingFeeMaintain")).Get("/api/v1/fulfillment/packaging-report", a.packagingReport)
	r.With(authz.RequireDuty("WarehouseView")).Get("/api/v1/fulfillment/returnable-packaging", a.listReturnableBalances)
	r.With(authz.RequireDuty("WarehouseReceive")).Post("/api/v1/fulfillment/returnable-packaging:return", a.returnPackaging)
}

// ---- DTOs -------------------------------------------------------------------

type containerTypeRequest struct {
	Code              string  `json:"code"`
	TareKg            string  `json:"tareKg"`
	Returnable        bool    `json:"returnable"`
	PackagingMaterial *string `json:"packagingMaterial"`
}

type packingRuleRequest struct {
	ProductID       string  `json:"productId"`
	CustomerID      *string `json:"customerId"`
	ContainerType   string  `json:"containerType"`
	QtyPerContainer string  `json:"qtyPerContainer"`
	MaxWeightKg     string  `json:"maxWeightKg"`
}

type autoPackRequest struct {
	// UnitWeights maps a shipment line id to the unit weight of its product in kg.
	UnitWeights map[string]string `json:"unitWeights"`
}

type productPackagingRequest struct {
	ProductID       string `json:"productId"`
	MaterialCode    string `json:"materialCode"`
	WeightPerUnitKg string `json:"weightPerUnitKg"`
}

type feeRateRequest struct {
	CountryCode  string `json:"countryCode"`
	MaterialCode string `json:"materialCode"`
	Currency     string `json:"currency"`
	RatePerKg    string `json:"ratePerKg"`
	ValidFrom    string `json:"validFrom"`
}

type packagingReportResponse struct {
	CountryCode   string                     `json:"countryCode"`
	Period        string                     `json:"period"`
	LegalEntityID *string                    `json:"legalEntityId"`
	Items         []store.PackagingReportRow `json:"items"`
}

type returnRequest struct {
	CustomerID    string `json:"customerId"`
	ContainerType string `json:"containerType"`
	Qty           string `json:"qty"`
}

// ---- handlers ---------------------------------------------------------------

func (a *API) listContainerTypes(w http.ResponseWriter, r *http.Request) {
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListContainerTypes(r.Context(), limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.ContainerType]{Items: items})
}

func (a *API) saveContainerType(w http.ResponseWriter, r *http.Request) {
	var req containerTypeRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !typeCodeRE.MatchString(req.Code) {
		fail(w, r, "code", "must be 1 to 64 letters, digits, '.', '_' or '-'")
		return
	}
	tare, err := nonNegative(req.TareKg)
	if err != nil {
		fail(w, r, "tareKg", "must be a non-negative decimal with at most 6 decimals")
		return
	}
	material, ok := optionalMaterial(req.PackagingMaterial)
	if !ok {
		fail(w, r, "packagingMaterial", "must be 1 to 32 letters, digits, '.', '_' or '-' when given")
		return
	}
	ct, err := a.store.SaveContainerType(r.Context(), store.ContainerType{Code: req.Code, TareKg: tare,
		Returnable: req.Returnable, PackagingMaterial: material}, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, ct)
}

func (a *API) listPackingRules(w http.ResponseWriter, r *http.Request) {
	productID := r.URL.Query().Get("productId")
	if productID != "" && !ids.Valid(productID) {
		fail(w, r, "productId", "must be a UUID")
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListPackingRules(r.Context(), productID, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.PackingRule]{Items: items})
}

func (a *API) savePackingRule(w http.ResponseWriter, r *http.Request) {
	var req packingRuleRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(req.ProductID) {
		fail(w, r, "productId", "must be a UUID")
		return
	}
	var customer *string
	if req.CustomerID != nil && *req.CustomerID != "" {
		if !ids.Valid(*req.CustomerID) {
			fail(w, r, "customerId", "must be a UUID when given")
			return
		}
		customer = req.CustomerID
	}
	if !typeCodeRE.MatchString(req.ContainerType) {
		fail(w, r, "containerType", "must be the code of a container type")
		return
	}
	qty, err := positive(req.QtyPerContainer)
	if err != nil {
		fail(w, r, "qtyPerContainer", "must be a positive decimal with at most 6 decimals")
		return
	}
	maxKg, err := positive(req.MaxWeightKg)
	if err != nil {
		fail(w, r, "maxWeightKg", "must be a positive decimal with at most 6 decimals")
		return
	}
	rule, err := a.store.SavePackingRule(r.Context(), store.PackingRule{ProductID: req.ProductID, CustomerID: customer,
		ContainerType: req.ContainerType, QtyPerContainer: qty, MaxWeightKg: maxKg}, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, rule)
}

func (a *API) autoPack(w http.ResponseWriter, r *http.Request) {
	id, version, ok := a.shipmentCommand(w, r)
	if !ok {
		return
	}
	var req autoPackRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	weights := make(map[string]decimal.Decimal, len(req.UnitWeights))
	for lineID, raw := range req.UnitWeights {
		if !ids.Valid(lineID) {
			fail(w, r, "unitWeights", "keys must be shipment line UUIDs")
			return
		}
		kg, err := nonNegative(raw)
		if err != nil {
			fail(w, r, fmt.Sprintf("unitWeights.%s", lineID), "must be a non-negative decimal with at most 6 decimals")
			return
		}
		weights[lineID] = kg
	}
	res, err := a.store.AutoPack(r.Context(), id, version, weights, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, res.Shipment.Version)
	httpx.JSON(w, http.StatusCreated, res)
}

func (a *API) listProductPackaging(w http.ResponseWriter, r *http.Request) {
	productID := r.URL.Query().Get("productId")
	if productID != "" && !ids.Valid(productID) {
		fail(w, r, "productId", "must be a UUID")
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListProductPackaging(r.Context(), productID, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.ProductPackaging]{Items: items})
}

func (a *API) saveProductPackaging(w http.ResponseWriter, r *http.Request) {
	var req productPackagingRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(req.ProductID) {
		fail(w, r, "productId", "must be a UUID")
		return
	}
	if !materialRE.MatchString(req.MaterialCode) {
		fail(w, r, "materialCode", "must be 1 to 32 letters, digits, '.', '_' or '-'")
		return
	}
	kg, err := nonNegative(req.WeightPerUnitKg)
	if err != nil {
		fail(w, r, "weightPerUnitKg", "must be a non-negative decimal with at most 6 decimals")
		return
	}
	pp, err := a.store.SaveProductPackaging(r.Context(), store.ProductPackaging{ProductID: req.ProductID,
		MaterialCode: req.MaterialCode, WeightPerUnitKg: kg}, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, pp)
}

func (a *API) listFeeRates(w http.ResponseWriter, r *http.Request) {
	country := r.URL.Query().Get("countryCode")
	if country != "" && !countryRE.MatchString(country) {
		fail(w, r, "countryCode", "must be a 2-letter ISO 3166 code")
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListFeeRates(r.Context(), country, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.FeeRate]{Items: items})
}

func (a *API) saveFeeRate(w http.ResponseWriter, r *http.Request) {
	var req feeRateRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !countryRE.MatchString(req.CountryCode) {
		fail(w, r, "countryCode", "must be a 2-letter ISO 3166 code")
		return
	}
	if !materialRE.MatchString(req.MaterialCode) {
		fail(w, r, "materialCode", "must be 1 to 32 letters, digits, '.', '_' or '-'")
		return
	}
	if !currencyCode.MatchString(req.Currency) {
		fail(w, r, "currency", "must be an ISO 4217 alpha-3 code")
		return
	}
	rate, err := nonNegative(req.RatePerKg)
	if err != nil {
		fail(w, r, "ratePerKg", "must be a non-negative decimal with at most 6 decimals")
		return
	}
	if _, err := time.Parse(dateLayout, req.ValidFrom); err != nil {
		fail(w, r, "validFrom", "must be a date as YYYY-MM-DD")
		return
	}
	saved, err := a.store.SaveFeeRate(r.Context(), store.FeeRate{CountryCode: req.CountryCode, MaterialCode: req.MaterialCode,
		Currency: req.Currency, RatePerKg: rate, ValidFrom: req.ValidFrom}, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, saved)
}

func (a *API) packagingReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ReportFilter{CountryCode: q.Get("countryCode"), Period: q.Get("period"), LegalEntityID: q.Get("legalEntityId")}
	if !countryRE.MatchString(f.CountryCode) {
		fail(w, r, "countryCode", "must be a 2-letter ISO 3166 code")
		return
	}
	if !periodRE.MatchString(f.Period) {
		fail(w, r, "period", "must be a month as YYYY-MM")
		return
	}
	if f.LegalEntityID != "" && !ids.Valid(f.LegalEntityID) {
		fail(w, r, "legalEntityId", "must be a UUID")
		return
	}
	rows, err := a.store.PackagingReport(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := packagingReportResponse{CountryCode: f.CountryCode, Period: f.Period, Items: rows}
	if f.LegalEntityID != "" {
		out.LegalEntityID = &f.LegalEntityID
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listReturnableBalances(w http.ResponseWriter, r *http.Request) {
	customerID := r.URL.Query().Get("customerId")
	if customerID != "" && !ids.Valid(customerID) {
		fail(w, r, "customerId", "must be a UUID")
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListReturnableBalances(r.Context(), customerID, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.ReturnableBalance]{Items: items})
}

func (a *API) returnPackaging(w http.ResponseWriter, r *http.Request) {
	var req returnRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ids.Valid(req.CustomerID) {
		fail(w, r, "customerId", "must be a UUID")
		return
	}
	if !typeCodeRE.MatchString(req.ContainerType) {
		fail(w, r, "containerType", "must be the code of a container type")
		return
	}
	// Balances count containers, so a return is a whole number of them.
	qty, err := positive(req.Qty)
	if err != nil || !wholeRE.MatchString(req.Qty) {
		fail(w, r, "qty", "must be a positive whole number of containers")
		return
	}
	bal, err := a.store.ReturnPackaging(r.Context(), store.ReturnInput{CustomerID: req.CustomerID, ContainerType: req.ContainerType, Qty: qty}, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, bal)
}

// optionalMaterial trims an optional packaging material code; an empty or
// absent value means none.
func optionalMaterial(v *string) (*string, bool) {
	if v == nil || strings.TrimSpace(*v) == "" {
		return nil, true
	}
	m := strings.TrimSpace(*v)
	if !materialRE.MatchString(m) {
		return nil, false
	}
	return &m, true
}
