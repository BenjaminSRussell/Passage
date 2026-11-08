package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const (
	fundsPath          = "/v1/pricing/funds"
	fundPath           = "/v1/pricing/funds/{id}"
	fundAttributePath  = "/v1/pricing/funds/{id}:attribute"
	fundRevisePath     = "/v1/pricing/funds/{id}:revise"
	fundClosePath      = "/v1/pricing/funds/{id}:close"
	fundReopenPath     = "/v1/pricing/funds/{id}:reopen"
	fundUsagePath      = "/v1/pricing/funds/{id}/usage"
	promotionsPlanPath = "/v1/pricing/promotions"
	promotionPath      = "/v1/pricing/promotions/{id}"
	promotionActPath   = "/v1/pricing/promotions/{id}:actuals"
	// Duty FundMaintain (PRD §5.26.17): funds, promotion plans, fund close. Reads accept
	// FundMaintain or PricingView (the Pricing workspace), since the PRD names no fund view duty.
	dutyFundMaintain = "pricing.fund.maintain"
	maxActualWeeks   = 260
)

// anyDuty allows a request when the principal holds at least one of the duties.
type anyDuty []string

// Allow implements authz.Policy.
func (d anyDuty) Allow(_ context.Context, p tenant.Principal, _, _ string) (bool, error) {
	for _, duty := range d {
		if p.HasDuty(duty) {
			return true, nil
		}
	}
	return false, nil
}

// requireFundRead admits PricingView or FundMaintain.
func requireFundRead() func(http.Handler) http.Handler {
	return authz.Require(anyDuty{dutyView, dutyFundMaintain}, dutyView+" or "+dutyFundMaintain, "")
}

type fundRequest struct {
	LegalEntityID   string `json:"legalEntityId"`
	Name            string `json:"name"`
	Type            string `json:"type"`
	Currency        string `json:"currency"`
	CustomerScope   string `json:"customerScope"`
	Owner           string `json:"owner"`
	BudgetPerPeriod string `json:"budgetPerPeriod"`
	AllowOverspend  bool   `json:"allowOverspend"`
}

type fundView struct {
	ID              string `json:"id"`
	LegalEntityID   string `json:"legalEntityId"`
	Name            string `json:"name"`
	Type            string `json:"type"`
	Currency        string `json:"currency"`
	CustomerScope   string `json:"customerScope"`
	Owner           string `json:"owner"`
	BudgetPerPeriod string `json:"budgetPerPeriod"`
	AllowOverspend  bool   `json:"allowOverspend"`
	Closed          bool   `json:"closed"`
	Version         int    `json:"version"`
}

type reviseRequest struct {
	Version         int    `json:"version"`
	BudgetPerPeriod string `json:"budgetPerPeriod"`
	AllowOverspend  bool   `json:"allowOverspend"`
}

type attributeRequest struct {
	Period    string `json:"period"`
	Source    string `json:"source"`
	Reference string `json:"reference"`
	Amount    string `json:"amount"`
}

type positionView struct {
	FundID    string `json:"fundId"`
	Period    string `json:"period"`
	Budget    string `json:"budget"`
	Used      string `json:"used"`
	Remaining string `json:"remaining"`
	Closed    bool   `json:"closed"`
}

type usageLineView struct {
	Period string `json:"period"`
	Source string `json:"source"`
	Amount string `json:"amount"`
}

type promotionRequest struct {
	LegalEntityID  string   `json:"legalEntityId"`
	Name           string   `json:"name"`
	EventCategory  string   `json:"eventCategory"`
	EventType      string   `json:"eventType"`
	CustomerScope  string   `json:"customerScope"`
	FundID         string   `json:"fundId"`
	CampaignID     string   `json:"campaignId"`
	ProductID      string   `json:"productId"`
	Currency       string   `json:"currency"`
	StartDate      string   `json:"startDate"`
	EndDate        string   `json:"endDate"`
	BaselineWeekly []string `json:"baselineWeeklyUnits"`
	LiftPct        string   `json:"liftPct"`
	PlannedSpend   string   `json:"plannedSpend"`
}

type actualsRequest struct {
	Units string `json:"units"`
	Weeks int    `json:"weeks"`
	Spend string `json:"spend"`
}

type actualsView struct {
	Units string `json:"units"`
	Weeks int    `json:"weeks"`
	Spend string `json:"spend"`
}

type promotionView struct {
	ID                  string       `json:"id"`
	LegalEntityID       string       `json:"legalEntityId"`
	Name                string       `json:"name"`
	EventCategory       string       `json:"eventCategory"`
	EventType           string       `json:"eventType"`
	CustomerScope       string       `json:"customerScope"`
	FundID              string       `json:"fundId,omitempty"`
	CampaignID          string       `json:"campaignId,omitempty"`
	ProductID           string       `json:"productId"`
	Currency            string       `json:"currency"`
	StartDate           string       `json:"startDate"`
	EndDate             string       `json:"endDate"`
	BaselineWeeklyUnits []string     `json:"baselineWeeklyUnits"`
	Baseline            string       `json:"baseline"`
	LiftPct             string       `json:"liftPct"`
	PlannedSpend        string       `json:"plannedSpend"`
	ForecastUnits       string       `json:"forecastUnits"`
	Actuals             *actualsView `json:"actuals,omitempty"`
	EffectivenessPct    *string      `json:"effectivenessPct,omitempty"`
}

func (a *API) createFund(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req fundRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	budget, err := decimal.NewFromString(req.BudgetPerPeriod)
	if err != nil {
		fields["budgetPerPeriod"] = "must be a decimal"
	}
	fund := domain.Fund{LegalEntityID: req.LegalEntityID, Name: req.Name, Type: req.Type, Currency: req.Currency,
		CustomerScope: req.CustomerScope, Owner: req.Owner, BudgetPerPeriod: budget, AllowOverspend: req.AllowOverspend}
	if len(fields) == 0 {
		for k, v := range domain.ValidateFund(fund) {
			fields[k] = v
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	f, err := a.store.CreateFund(r.Context(), fund, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, fundResponse(f))
}

func (a *API) listFunds(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if le != "" && !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	rows, err := a.store.ListFunds(r.Context(), le, store.MaxListLimit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]fundView, 0, len(rows))
	for _, f := range rows {
		items = append(items, fundResponse(f))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// fundID returns the {id} path parameter, or writes PRC-0039 when it is not a UUID.
func fundID(w http.ResponseWriter, r *http.Request) (string, bool) {
	return uuidParam(w, r, domain.CodeFundNotFound, "trade fund")
}

// promotionID returns the {id} path parameter, or writes PRC-0040 when it is not a UUID.
func promotionID(w http.ResponseWriter, r *http.Request) (string, bool) {
	return uuidParam(w, r, domain.CodePromoNotFound, "promotion plan")
}

func uuidParam(w http.ResponseWriter, r *http.Request, code, kind string) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, code, "no "+kind+" "+id))
		return "", false
	}
	return id, true
}

func (a *API) getFund(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := fundID(w, r)
	if !ok {
		return
	}
	f, err := a.store.GetFund(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, fundResponse(f))
}

// attributeFund charges usage to a fund for one period (BR-26.15). 201 with the period position.
func (a *API) attributeFund(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := fundID(w, r)
	if !ok {
		return
	}
	var req attributeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !domain.ValidPeriod(req.Period) {
		fields["period"] = "must be a fiscal period in YYYY-MM form"
	}
	if !domain.ValidUsageSource(req.Source) {
		fields["source"] = "must be CAMPAIGN_ACCRUAL, REBATE_ACCRUAL, CLAIM or DEDUCTION"
	}
	if req.Reference == "" || len(req.Reference) > maxInvoiceLineID {
		fields["reference"] = "must be 1 to 128 characters"
	}
	amount, err := decimal.NewFromString(req.Amount)
	if err != nil {
		fields["amount"] = "must be a decimal"
	} else if !amount.IsPositive() {
		fields["amount"] = "must be greater than zero"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	pos, err := a.store.AttributeFund(r.Context(), store.FundAttribution{FundID: id, Period: req.Period, Source: req.Source,
		Reference: req.Reference, Amount: amount}, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.fundUsages.Add(1)
	httpx.JSON(w, http.StatusCreated, positionResponse(pos))
}

// reviseFund changes the budget per period and the overspend flag (the PRC-0012 remedy).
func (a *API) reviseFund(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := fundID(w, r)
	if !ok {
		return
	}
	var req reviseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.Version < 1 {
		fields["version"] = "is required and must be at least 1"
	}
	budget, err := decimal.NewFromString(req.BudgetPerPeriod)
	if err != nil {
		fields["budgetPerPeriod"] = "must be a decimal"
	} else if budget.IsNegative() {
		fields["budgetPerPeriod"] = "must not be negative"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	f, err := a.store.ReviseFund(r.Context(), store.FundRevision{ID: id, Version: req.Version, BudgetPerPeriod: budget,
		AllowOverspend: req.AllowOverspend}, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, fundResponse(f))
}

// fundState returns the close or reopen handler: both take {"version"} and an Idempotency-Key.
func (a *API) fundState(closeIt bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id, ok := fundID(w, r)
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
		change := a.store.ReopenFund
		if closeIt {
			change = a.store.CloseFund
		}
		f, err := change(r.Context(), id, req.Version, r.Header.Get(idempotencyKey))
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, fundResponse(f))
	}
}

// fundUsage lists usage per period and source with the remaining budget per period (§5.26.14).
func (a *API) fundUsage(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := fundID(w, r)
	if !ok {
		return
	}
	f, lines, err := a.store.FundUsage(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]usageLineView, 0, len(lines))
	order := []string{}
	sums := map[string]decimal.Decimal{}
	for _, l := range lines {
		items = append(items, usageLineView{Period: l.Period, Source: l.Source, Amount: l.Amount.String()})
		if _, seen := sums[l.Period]; !seen {
			order = append(order, l.Period)
		}
		sums[l.Period] = sums[l.Period].Add(l.Amount)
	}
	periods := make([]positionView, 0, len(order))
	for _, per := range order {
		periods = append(periods, positionResponse(store.FundPosition{Fund: f, Period: per, Used: sums[per]}))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"fund": fundResponse(f), "items": items, "periods": periods})
}

func (a *API) createPromotion(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req promotionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if req.FundID != "" && !ids.Valid(req.FundID) {
		fields["fundId"] = "must be a UUID"
	}
	if req.CampaignID != "" && !ids.Valid(req.CampaignID) {
		fields["campaignId"] = "must be a UUID"
	}
	start, err1 := time.Parse(dateLayout, req.StartDate)
	end, err2 := time.Parse(dateLayout, req.EndDate)
	if err1 != nil {
		fields["startDate"] = "must be a date in YYYY-MM-DD form"
	}
	if err2 != nil {
		fields["endDate"] = "must be a date in YYYY-MM-DD form"
	}
	lift, err := decimal.NewFromString(orDefault(req.LiftPct, "0"))
	if err != nil {
		fields["liftPct"] = "must be a decimal"
	}
	spend, err := decimal.NewFromString(orDefault(req.PlannedSpend, "0"))
	if err != nil {
		fields["plannedSpend"] = "must be a decimal"
	}
	weekly := make([]decimal.Decimal, 0, len(req.BaselineWeekly))
	for i, s := range req.BaselineWeekly {
		v, err := decimal.NewFromString(s)
		if err != nil {
			fields["baselineWeeklyUnits["+strconv.Itoa(i)+"]"] = "must be a decimal"
			continue
		}
		weekly = append(weekly, v)
	}
	plan := domain.PromotionPlan{LegalEntityID: req.LegalEntityID, Name: req.Name, EventCategory: req.EventCategory,
		EventType: req.EventType, CustomerScope: req.CustomerScope, FundID: req.FundID, CampaignID: req.CampaignID, ProductID: req.ProductID,
		Currency: req.Currency, Start: start, End: end, BaselineWeekly: weekly, LiftPct: lift, PlannedSpend: spend}
	if len(fields) == 0 {
		for k, v := range domain.ValidatePromotion(plan) {
			fields[k] = v
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	p, err := a.store.CreatePromotion(r.Context(), plan, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := promotionResponse(p)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) listPromotions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if le != "" && !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	campaign := r.URL.Query().Get("campaignId")
	if campaign != "" && !ids.Valid(campaign) {
		validationFailed(w, r, map[string]string{"campaignId": "must be a UUID"})
		return
	}
	rows, err := a.store.ListPromotions(r.Context(), le, campaign, store.MaxListLimit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]promotionView, 0, len(rows))
	for _, p := range rows {
		v, err := promotionResponse(p)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		items = append(items, v)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getPromotion(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := promotionID(w, r)
	if !ok {
		return
	}
	p, err := a.store.GetPromotion(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := promotionResponse(p)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// recordActuals stores units and spend posted in the promotion window and returns the
// effectiveness per FR-26.20. The nightly actuals job writes through the same store call.
func (a *API) recordActuals(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := promotionID(w, r)
	if !ok {
		return
	}
	var req actualsRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	units, err1 := decimal.NewFromString(req.Units)
	spend, err2 := decimal.NewFromString(orDefault(req.Spend, "0"))
	if err1 != nil {
		fields["units"] = "must be a decimal"
	}
	if err2 != nil {
		fields["spend"] = "must be a decimal"
	}
	if err1 == nil && units.IsNegative() {
		fields["units"] = "must not be negative"
	}
	if err2 == nil && spend.IsNegative() {
		fields["spend"] = "must not be negative"
	}
	if req.Weeks < 1 || req.Weeks > maxActualWeeks {
		fields["weeks"] = "must be between 1 and 260"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	p, err := a.store.RecordPromotionActuals(r.Context(), id, domain.PromotionActuals{Units: units, Weeks: req.Weeks, Spend: spend})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := promotionResponse(p)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// registerFundRoutes mounts trade funds (FR-26.19) and promotion planning (FR-26.20).
func (a *API) registerFundRoutes(r chi.Router) {
	maintain := authz.RequireDuty(dutyFundMaintain)
	r.With(maintain).Post(fundsPath, a.createFund)
	r.With(requireFundRead()).Get(fundsPath, a.listFunds)
	r.With(requireFundRead()).Get(fundPath, a.getFund)
	r.With(maintain).Post(fundAttributePath, a.attributeFund)
	r.With(maintain).Post(fundRevisePath, a.reviseFund)
	r.With(maintain).Post(fundClosePath, a.fundState(true))
	r.With(maintain).Post(fundReopenPath, a.fundState(false))
	r.With(requireFundRead()).Get(fundUsagePath, a.fundUsage)
	r.With(maintain).Post(promotionsPlanPath, a.createPromotion)
	r.With(requireFundRead()).Get(promotionsPlanPath, a.listPromotions)
	r.With(requireFundRead()).Get(promotionPath, a.getPromotion)
	r.With(maintain).Post(promotionActPath, a.recordActuals)
}

func fundResponse(f domain.Fund) fundView {
	return fundView{ID: f.ID, LegalEntityID: f.LegalEntityID, Name: f.Name, Type: f.Type, Currency: f.Currency,
		CustomerScope: f.CustomerScope, Owner: f.Owner, BudgetPerPeriod: f.BudgetPerPeriod.String(),
		AllowOverspend: f.AllowOverspend, Closed: f.Closed, Version: f.Version}
}

func positionResponse(p store.FundPosition) positionView {
	return positionView{FundID: p.Fund.ID, Period: p.Period, Budget: p.Fund.BudgetPerPeriod.String(), Used: p.Used.String(),
		Remaining: domain.Remaining(p.Fund, p.Used).String(), Closed: p.Fund.Closed}
}

// promotionResponse derives baseline, forecast and (when actuals exist) effectiveness.
func promotionResponse(p domain.PromotionPlan) (promotionView, error) {
	base, err := domain.Baseline(p.BaselineWeekly)
	if err != nil {
		return promotionView{}, err
	}
	weekly := make([]string, 0, len(p.BaselineWeekly))
	for _, w := range p.BaselineWeekly {
		weekly = append(weekly, w.String())
	}
	weeks := domain.PlanWeeks(p.Start, p.End)
	out := promotionView{ID: p.ID, LegalEntityID: p.LegalEntityID, Name: p.Name, EventCategory: p.EventCategory,
		EventType: p.EventType, CustomerScope: p.CustomerScope, FundID: p.FundID, CampaignID: p.CampaignID, ProductID: p.ProductID,
		Currency: p.Currency, StartDate: p.Start.Format(dateLayout), EndDate: p.End.Format(dateLayout),
		BaselineWeeklyUnits: weekly, Baseline: base.String(), LiftPct: p.LiftPct.String(), PlannedSpend: p.PlannedSpend.String(),
		ForecastUnits: domain.ForecastUnits(base, p.LiftPct, weeks).String()}
	if p.Actuals != nil {
		out.Actuals = &actualsView{Units: p.Actuals.Units.String(), Weeks: p.Actuals.Weeks, Spend: p.Actuals.Spend.String()}
		if eff, err := domain.Effectiveness(base, *p.Actuals); err == nil {
			s := eff.String()
			out.EffectivenessPct = &s
		}
	}
	return out, nil
}
