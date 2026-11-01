package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const (
	alternativeRules = "/v1/sales/alternative-item-rules"
	alternativeEval  = "/v1/sales/alternative-items:evaluate"
	// maxAlternatives bounds the alternatives (each with its buckets) of one evaluation.
	maxAlternatives = 20
)

// registerAlternatives mounts the alternative item routes (FR-27.13).
func (a *API) registerAlternatives(r chi.Router) {
	r.With(authz.RequireDuty(dutyListing)).Post(alternativeRules, a.createAlternativeRule)
	r.With(authz.RequireDuty(dutyRead)).Get(alternativeRules, a.listAlternativeRules)
	r.With(authz.RequireDuty(dutyListing)).Delete(alternativeRules+"/{id}", a.deleteAlternativeRule)
	r.With(authz.RequireDuty(dutyRead)).Post(alternativeEval, a.evaluateAlternatives)
}

// alternativeRuleRequest is the body of POST /v1/sales/alternative-item-rules.
// An absent customerId means every customer; an absent priority means 1.
type alternativeRuleRequest struct {
	LegalEntityID        string  `json:"legalEntityId"`
	OrderedProductID     string  `json:"orderedProductId"`
	AlternativeProductID string  `json:"alternativeProductId"`
	CustomerID           *string `json:"customerId,omitempty"`
	Priority             *int    `json:"priority,omitempty"`
	ValidFrom            *string `json:"validFrom,omitempty"`
	ValidTo              *string `json:"validTo,omitempty"`
}

// alternativeSupplyRequest is the ATP of one alternative product.
type alternativeSupplyRequest struct {
	ProductID string             `json:"productId"`
	Periods   []atpPeriodRequest `json:"periods"`
}

// evaluateAlternativesRequest is the body of POST
// /v1/sales/alternative-items:evaluate. Periods are the ordered product's ATP
// buckets; Alternatives hold the buckets of the products the caller can read
// ATP for (the projection is a later slice, as for atp:check).
type evaluateAlternativesRequest struct {
	LegalEntityID string                     `json:"legalEntityId"`
	CustomerID    string                     `json:"customerId"`
	ProductID     string                     `json:"productId"`
	Quantity      string                     `json:"quantity"`
	Requested     string                     `json:"requested"`
	Periods       []atpPeriodRequest         `json:"periods"`
	Alternatives  []alternativeSupplyRequest `json:"alternatives"`
}

type alternativeOrderedView struct {
	ProductID       string          `json:"productId"`
	AvailableOnDate decimal.Decimal `json:"availableOnDate"`
	CoversQuantity  bool            `json:"coversQuantity"`
	Promise         atpPromiseView  `json:"promise"`
}

type alternativeOfferView struct {
	RuleID               string          `json:"ruleId"`
	AlternativeProductID string          `json:"alternativeProductId"`
	Priority             int             `json:"priority"`
	AvailableOnDate      decimal.Decimal `json:"availableOnDate"`
	CoversQuantity       bool            `json:"coversQuantity"`
	Promise              atpPromiseView  `json:"promise"`
}

type uncheckedAlternativeView struct {
	RuleID               string `json:"ruleId"`
	AlternativeProductID string `json:"alternativeProductId"`
	Priority             int    `json:"priority"`
}

// alternativeEvaluation is the response of the evaluate call. Nothing is stored.
type alternativeEvaluation struct {
	Ordered   alternativeOrderedView     `json:"ordered"`
	Needed    bool                       `json:"needed"`
	Offers    []alternativeOfferView     `json:"offers"`
	Unchecked []uncheckedAlternativeView `json:"unchecked"`
}

func promiseView(p domain.Promise) atpPromiseView {
	return atpResponse(p, nil).Promise
}

// buildAlternativeRule validates a rule. Field errors are keyed by JSON name.
func buildAlternativeRule(req alternativeRuleRequest) (domain.AlternativeRule, map[string]string) {
	priority := 1
	if req.Priority != nil {
		priority = *req.Priority
	}
	rule := domain.AlternativeRule{LegalEntityID: req.LegalEntityID, OrderedProductID: req.OrderedProductID,
		AlternativeProductID: req.AlternativeProductID, CustomerID: req.CustomerID, Priority: priority,
		ValidFrom: req.ValidFrom, ValidTo: req.ValidTo}
	f := domain.ValidateAlternativeRule(rule)
	for name, v := range map[string]string{"legalEntityId": req.LegalEntityID, "orderedProductId": req.OrderedProductID,
		"alternativeProductId": req.AlternativeProductID} {
		if !ids.Valid(v) {
			f[name] = "must be a UUID"
		}
	}
	if req.CustomerID != nil && !ids.Valid(*req.CustomerID) {
		f["customerId"] = "must be a UUID"
	}
	return rule, f
}

// createAlternativeRule stores an alternative item rule for a legal entity in
// the caller's scope (FR-27.13).
func (a *API) createAlternativeRule(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req alternativeRuleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	rule, fields := buildAlternativeRule(req)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !inScope(w, r, rule.LegalEntityID) {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	saved, err := a.store.CreateAlternativeRule(r.Context(), rule, p.UserID)
	if err != nil {
		a.fail(w, r, "create alternative rule", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, saved)
}

// listAlternativeRules returns rules in the caller's scope, optionally for the
// ordered products named by repeated orderedProductId parameters.
func (a *API) listAlternativeRules(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.AlternativeRuleFilter{LegalEntityID: legalEntity, OrderedProductIDs: q["orderedProductId"]}
	fields := map[string]string{}
	if msg := productList(f.OrderedProductIDs, false); msg != "" {
		fields["orderedProductId"] = msg
	}
	limit, ok := pageLimit(q.Get("limit"))
	if !ok {
		fields["limit"] = fmt.Sprintf("must be 1 to %d", maxPageSize)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	f.Limit = limit
	items, err := a.store.ListAlternativeRules(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list alternative rules", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// deleteAlternativeRule removes a rule.
func (a *API) deleteAlternativeRule(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeAlternativeNotFound, "alternative item rule not found"))
		return
	}
	if err := a.store.DeleteAlternativeRule(r.Context(), id); err != nil {
		a.fail(w, r, "delete alternative rule", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// evaluateAlternatives answers FR-27.13: when the ordered product cannot ship
// the whole quantity by the requested date, which configured alternatives can,
// with the ATP of each. It stores nothing; the user decides whether to swap
// the product on the line.
func (a *API) evaluateAlternatives(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req evaluateAlternativesRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if req.CustomerID != "" && !ids.Valid(req.CustomerID) {
		fields["customerId"] = "must be a UUID"
	}
	if !ids.Valid(req.ProductID) {
		fields["productId"] = "must be a UUID"
	}
	requested, err := time.Parse(dayLayout, req.Requested)
	if err != nil {
		fields["requested"] = "must be a date such as 2027-03-01"
	}
	qty, err := decimal.NewFromString(req.Quantity)
	if err != nil {
		fields["quantity"] = "must be a decimal"
	} else if err := domain.ValidateQuantity(qty); err != nil {
		fields["quantity"] = err.Error()
	}
	if n := len(req.Periods); n == 0 || n > maxPeriods {
		fields["periods"] = "must have 1 to 1000 buckets"
	}
	periods := parsePeriods(req.Periods, fields)
	if len(req.Alternatives) > maxAlternatives {
		fields["alternatives"] = fmt.Sprintf("at most %d alternatives per call", maxAlternatives)
	}
	supplies := map[string][]domain.Period{}
	for i, alt := range req.Alternatives {
		path := fmt.Sprintf("alternatives[%d]", i)
		if !ids.Valid(alt.ProductID) {
			fields[path+".productId"] = "must be a UUID"
		} else if _, dup := supplies[alt.ProductID]; dup {
			fields[path+".productId"] = "is listed more than once"
		}
		if n := len(alt.Periods); n == 0 || n > maxPeriods {
			fields[path+".periods"] = "must have 1 to 1000 buckets"
			continue
		}
		sub := map[string]string{}
		supplies[alt.ProductID] = parsePeriods(alt.Periods, sub)
		for k, v := range sub {
			fields[path+"."+k] = v
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !inScope(w, r, req.LegalEntityID) {
		return
	}
	ordered, err := domain.PromiseDate(periods, requested, qty, false)
	if err != nil {
		validationFailed(w, r, map[string]string{"periods": err.Error()})
		return
	}
	avail := domain.AvailableOnDate(ordered, requested)
	out := alternativeEvaluation{
		Ordered: alternativeOrderedView{ProductID: req.ProductID, AvailableOnDate: avail, CoversQuantity: avail.GreaterThanOrEqual(qty),
			Promise: promiseView(ordered)},
		Offers: []alternativeOfferView{}, Unchecked: []uncheckedAlternativeView{},
	}
	out.Needed = !out.Ordered.CoversQuantity
	if out.Needed {
		rules, err := a.store.ListAlternativeRules(r.Context(), store.AlternativeRuleFilter{LegalEntityID: req.LegalEntityID,
			OrderedProductIDs: []string{req.ProductID}, Limit: maxProposalRules})
		if err != nil {
			a.fail(w, r, "list alternative rules", err)
			return
		}
		applicable := domain.ApplicableAlternatives(rules, req.CustomerID, req.ProductID, requested)
		offers, unchecked := domain.OfferAlternatives(applicable, supplies, requested, qty)
		for _, o := range offers {
			out.Offers = append(out.Offers, alternativeOfferView{RuleID: o.RuleID, AlternativeProductID: o.AlternativeProductID,
				Priority: o.Priority, AvailableOnDate: o.AvailableOnDate, CoversQuantity: o.CoversQuantity, Promise: promiseView(o.Promise)})
		}
		for _, u := range unchecked {
			out.Unchecked = append(out.Unchecked, uncheckedAlternativeView{RuleID: u.ID, AlternativeProductID: u.AlternativeProductID, Priority: u.Priority})
		}
	}
	a.atp.Add(1)
	httpx.JSON(w, http.StatusOK, out)
}
