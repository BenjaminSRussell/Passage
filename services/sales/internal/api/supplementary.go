package api

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

// supplementaryRuleRequest is the body of POST /v1/sales/supplementary-item-rules.
// An absent customerId means every customer of the legal entity; an absent
// unitPrice is zero (FREE rules need none).
type supplementaryRuleRequest struct {
	LegalEntityID        string  `json:"legalEntityId"`
	ParentProductID      string  `json:"parentProductId"`
	CustomerID           *string `json:"customerId,omitempty"`
	SupplementaryProduct string  `json:"supplementaryProductId"`
	Unit                 string  `json:"unit"`
	Requirement          string  `json:"requirement"`
	BaseQuantity         string  `json:"baseQuantity"`
	QuantityPerBase      string  `json:"quantityPerBase"`
	Pricing              string  `json:"pricing"`
	UnitPrice            string  `json:"unitPrice,omitempty"`
}

// evaluateSupplementaryRequest is the body of POST
// /v1/sales/supplementary-items:evaluate: one parent line.
type evaluateSupplementaryRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	ProductID     string `json:"productId"`
	CustomerID    string `json:"customerId"`
	Quantity      string `json:"quantity"`
}

// supplementaryEvaluation lists the lines a parent line triggers. MANDATORY
// lines are to be added with the parent; OPTIONAL lines are offered to the
// user (FR-27.14).
type supplementaryEvaluation struct {
	Items []domain.SupplementaryLine `json:"items"`
}

// buildSupplementaryRule validates a rule. Field errors are keyed by JSON name.
func buildSupplementaryRule(req supplementaryRuleRequest) (domain.SupplementaryRule, map[string]string) {
	f := map[string]string{}
	for name, v := range map[string]string{"legalEntityId": req.LegalEntityID, "parentProductId": req.ParentProductID,
		"supplementaryProductId": req.SupplementaryProduct} {
		if !ids.Valid(v) {
			f[name] = "must be a UUID"
		}
	}
	if req.CustomerID != nil && !ids.Valid(*req.CustomerID) {
		f["customerId"] = "must be a UUID"
	}
	price := req.UnitPrice
	if price == "" {
		price = "0"
	}
	rule := domain.SupplementaryRule{LegalEntityID: req.LegalEntityID, ParentProductID: req.ParentProductID, CustomerID: req.CustomerID,
		SupplementaryProduct: req.SupplementaryProduct, Unit: req.Unit, Requirement: req.Requirement, Pricing: req.Pricing}
	var errBase, errPer, errPrice error
	rule.BaseQuantity, errBase = decimal.NewFromString(req.BaseQuantity)
	rule.QuantityPerBase, errPer = decimal.NewFromString(req.QuantityPerBase)
	rule.UnitPrice, errPrice = decimal.NewFromString(price)
	for k, v := range domain.ValidateSupplementaryRule(rule) {
		f[k] = v
	}
	if errBase != nil {
		f["baseQuantity"] = "must be a decimal"
	}
	if errPer != nil {
		f["quantityPerBase"] = "must be a decimal"
	}
	if errPrice != nil {
		f["unitPrice"] = "must be a decimal"
	} else if !rule.UnitPrice.Round(4).Equal(rule.UnitPrice) {
		f["unitPrice"] = "must have at most 4 decimal places"
	}
	return rule, f
}

// createSupplementaryRule stores a rule for a legal entity in the caller's scope.
func (a *API) createSupplementaryRule(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req supplementaryRuleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	rule, fields := buildSupplementaryRule(req)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !inScope(w, r, rule.LegalEntityID) {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	saved, err := a.store.CreateSupplementaryRule(r.Context(), rule, p.UserID)
	if err != nil {
		a.fail(w, r, "create supplementary rule", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, saved)
}

// listSupplementaryRules returns rules in the caller's scope, optionally for
// one parent product.
func (a *API) listSupplementaryRules(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.SupplementaryRuleFilter{LegalEntityID: legalEntity, ParentProductID: q.Get("parentProductId")}
	fields := map[string]string{}
	if f.ParentProductID != "" && !ids.Valid(f.ParentProductID) {
		fields["parentProductId"] = "must be a UUID"
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
	items, err := a.store.ListSupplementaryRules(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list supplementary rules", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// deleteSupplementaryRule removes a rule.
func (a *API) deleteSupplementaryRule(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeSupplementaryNotFound, "supplementary item rule not found"))
		return
	}
	if err := a.store.DeleteSupplementaryRule(r.Context(), id); err != nil {
		a.fail(w, r, "delete supplementary rule", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// evaluateSupplementary returns the supplementary lines a parent line of the
// given quantity triggers for the customer. It stores nothing.
func (a *API) evaluateSupplementary(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req evaluateSupplementaryRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.ProductID) {
		fields["productId"] = "must be a UUID"
	}
	if req.CustomerID != "" && !ids.Valid(req.CustomerID) {
		fields["customerId"] = "must be a UUID"
	}
	qty, err := decimal.NewFromString(req.Quantity)
	if err != nil {
		fields["quantity"] = "must be a decimal"
	} else if err := domain.ValidateQuantity(qty); err != nil {
		fields["quantity"] = err.Error()
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !inScope(w, r, req.LegalEntityID) {
		return
	}
	rules, err := a.store.ListSupplementaryRules(r.Context(), store.SupplementaryRuleFilter{LegalEntityID: req.LegalEntityID,
		ParentProductID: req.ProductID, Limit: maxSupplementaryRules})
	if err != nil {
		a.fail(w, r, "list supplementary rules", err)
		return
	}
	items, err := domain.EvaluateSupplementary(rules, req.ProductID, req.CustomerID, qty)
	if err != nil {
		validationFailed(w, r, map[string]string{"quantity": err.Error()})
		return
	}
	httpx.JSON(w, http.StatusOK, supplementaryEvaluation{Items: items})
}

// maxSupplementaryRules bounds the rules read for one parent product.
const maxSupplementaryRules = 1000
