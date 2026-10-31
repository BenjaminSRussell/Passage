package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const (
	freeGoodsPath         = "/v1/pricing/free-goods-rules"
	freeGoodsRulePath     = "/v1/pricing/free-goods-rules/{id}"
	freeGoodsWithdrawPath = "/v1/pricing/free-goods-rules/{id}:withdraw"
)

type freeGoodsRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	CustomerKind  string `json:"customerKind"`
	CustomerKey   string `json:"customerKey"`
	ProductID     string `json:"productId"`
	Unit          string `json:"unit"`
	Kind          string `json:"kind"`
	CalcRule      string `json:"calcRule"`
	EveryQuantity string `json:"everyQuantity"`
	FreeQuantity  string `json:"freeQuantity"`
	MinQuantity   string `json:"minQuantity"`
	ValidFrom     string `json:"validFrom"`
	ValidTo       string `json:"validTo"`
}

type freeGoodsView struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legalEntityId"`
	CustomerKind  string `json:"customerKind"`
	CustomerKey   string `json:"customerKey"`
	ProductID     string `json:"productId"`
	Unit          string `json:"unit"`
	Kind          string `json:"kind"`
	CalcRule      string `json:"calcRule"`
	EveryQuantity string `json:"everyQuantity"`
	FreeQuantity  string `json:"freeQuantity"`
	MinQuantity   string `json:"minQuantity"`
	ValidFrom     string `json:"validFrom"`
	ValidTo       string `json:"validTo"`
	Withdrawn     bool   `json:"withdrawn"`
	Version       int    `json:"version"`
}

func freeGoodsResponse(r domain.FreeGoodsDef) freeGoodsView {
	return freeGoodsView{ID: r.ID, LegalEntityID: r.LegalEntityID, CustomerKind: string(r.CustomerKind), CustomerKey: r.CustomerKey,
		ProductID: r.ProductID, Unit: r.Unit, Kind: r.Kind, CalcRule: r.Calc, EveryQuantity: r.Every.String(),
		FreeQuantity: r.Free.String(), MinQuantity: r.MinQty.String(), ValidFrom: r.ValidFrom.Format(dateLayout),
		ValidTo: r.ValidTo.Format(dateLayout), Withdrawn: r.Withdrawn, Version: r.Version}
}

// registerFreeGoodsRoutes mounts the free goods rule surface (FR-26.22). PricingMaintain
// writes rules; reads take PricingView.
func (a *API) registerFreeGoodsRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyMaintain)).Post(freeGoodsPath, a.createFreeGoods)
	r.With(authz.RequireDuty(dutyView)).Get(freeGoodsPath, a.listFreeGoods)
	r.With(authz.RequireDuty(dutyView)).Get(freeGoodsRulePath, a.getFreeGoods)
	r.With(authz.RequireDuty(dutyMaintain)).Post(freeGoodsWithdrawPath, a.withdrawFreeGoods)
}

func optionalDecimal(raw string, field string, fields map[string]string) decimal.Decimal {
	if raw == "" {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(raw)
	if err != nil {
		fields[field] = "must be a decimal"
	}
	return d
}

func (a *API) createFreeGoods(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req freeGoodsRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	from, err1 := time.Parse(dateLayout, req.ValidFrom)
	to, err2 := time.Parse(dateLayout, req.ValidTo)
	if err1 != nil || err2 != nil {
		fields["validity"] = "validFrom and validTo must be dates in YYYY-MM-DD form"
	}
	every, err := decimal.NewFromString(req.EveryQuantity)
	if err != nil {
		fields["everyQuantity"] = "must be a decimal"
	}
	free, err := decimal.NewFromString(req.FreeQuantity)
	if err != nil {
		fields["freeQuantity"] = "must be a decimal"
	}
	min := optionalDecimal(req.MinQuantity, "minQuantity", fields)
	if req.CalcRule == "" {
		// The PRD's free_goods_calc_rule parameter defaults to WHOLE_UNITS (§5.26.13);
		// there is no per-entity parameter table yet, so the default is fixed.
		req.CalcRule = domain.CalcWholeUnits
	}
	def := domain.FreeGoodsDef{LegalEntityID: req.LegalEntityID, CustomerKind: domain.CustomerKind(req.CustomerKind), CustomerKey: req.CustomerKey,
		ProductID: req.ProductID, Unit: req.Unit, Kind: req.Kind, Calc: req.CalcRule, Every: every, Free: free, MinQty: min,
		ValidFrom: from, ValidTo: to}
	for k, v := range domain.ValidateFreeGoods(def) {
		if _, set := fields[k]; !set {
			fields[k] = v
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.CreateFreeGoodsRule(r.Context(), def, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, freeGoodsResponse(out))
}

func (a *API) listFreeGoods(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if le != "" && !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	rows, err := a.store.ListFreeGoodsRules(r.Context(), le, r.URL.Query().Get("productId"), store.MaxListLimit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]freeGoodsView, 0, len(rows))
	for _, x := range rows {
		items = append(items, freeGoodsResponse(x))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getFreeGoods(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeFreeGoodsRuleNotFound, "free goods rule")
	if !ok {
		return
	}
	rule, err := a.store.GetFreeGoodsRule(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, freeGoodsResponse(rule))
}

func (a *API) withdrawFreeGoods(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeFreeGoodsRuleNotFound, "free goods rule")
	if !ok {
		return
	}
	var req versionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Version < 1 {
		validationFailed(w, r, map[string]string{"version": "is required and must be at least 1"})
		return
	}
	rule, err := a.store.WithdrawFreeGoodsRule(r.Context(), id, req.Version, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, freeGoodsResponse(rule))
}

// freeGoodsRules loads the live free goods rules of the legal entity for the products on
// the document at its pricing date (FR-26.22).
func (a *API) freeGoodsRules(r *http.Request, legalEntityID string, header domain.Header, lines []domain.Line) ([]domain.FreeGoodsDef, error) {
	seen := map[string]bool{}
	var products []string
	for _, l := range lines {
		if !seen[l.ProductID] {
			seen[l.ProductID] = true
			products = append(products, l.ProductID)
		}
	}
	return a.store.ActiveFreeGoodsRules(r.Context(), legalEntityID, products, header.PricingDate)
}
