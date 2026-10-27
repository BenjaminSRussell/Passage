package api

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const (
	// maxProposalProducts bounds the product lists of one evaluate or list call.
	maxProposalProducts = 50
	// maxProposalRules bounds the rules read for one evaluation.
	maxProposalRules = 1000
	maxKeyLength     = 255
)

// proposalRuleRequest is the body of POST /v1/sales/product-proposal-rules.
// An absent customerId means every customer of the legal entity.
type proposalRuleRequest struct {
	LegalEntityID     string  `json:"legalEntityId"`
	OrderedProductID  string  `json:"orderedProductId"`
	ProposedProductID string  `json:"proposedProductId"`
	CustomerID        *string `json:"customerId,omitempty"`
	Reason            string  `json:"reason"`
}

// evaluateProposalsRequest is the body of POST
// /v1/sales/product-proposals:evaluate. ProductIDs are the products being
// committed; CurrentProductIDs are the products already on the order.
type evaluateProposalsRequest struct {
	LegalEntityID     string   `json:"legalEntityId"`
	CustomerID        string   `json:"customerId"`
	ProductIDs        []string `json:"productIds"`
	CurrentProductIDs []string `json:"currentProductIds"`
}

// proposalEvaluation is the response of the evaluate call. Nothing is stored.
type proposalEvaluation struct {
	Items []domain.Proposal `json:"items"`
}

// proposalResponseRequest is the body of POST
// /v1/sales/product-proposals:respond: the user's answer to one proposal.
type proposalResponseRequest struct {
	LegalEntityID string  `json:"legalEntityId"`
	RuleID        string  `json:"ruleId"`
	CustomerID    *string `json:"customerId,omitempty"`
	OrderID       *string `json:"orderId,omitempty"`
	Accepted      *bool   `json:"accepted"`
}

// productList reports the field error for a list that is empty when required,
// too long, or holds a non-UUID.
func productList(list []string, required bool) string {
	switch {
	case required && len(list) == 0:
		return "at least one product is required"
	case len(list) > maxProposalProducts:
		return fmt.Sprintf("at most %d products per call", maxProposalProducts)
	}
	for _, id := range list {
		if !ids.Valid(id) {
			return "every entry must be a UUID"
		}
	}
	return ""
}

// buildProposalRule validates a rule. Field errors are keyed by JSON name.
func buildProposalRule(req proposalRuleRequest) (domain.ProposalRule, map[string]string) {
	rule := domain.ProposalRule{LegalEntityID: req.LegalEntityID, OrderedProductID: req.OrderedProductID,
		ProposedProductID: req.ProposedProductID, CustomerID: req.CustomerID, Reason: req.Reason}
	f := domain.ValidateProposalRule(rule)
	for name, v := range map[string]string{"legalEntityId": req.LegalEntityID, "orderedProductId": req.OrderedProductID,
		"proposedProductId": req.ProposedProductID} {
		if !ids.Valid(v) {
			f[name] = "must be a UUID"
		}
	}
	if req.CustomerID != nil && !ids.Valid(*req.CustomerID) {
		f["customerId"] = "must be a UUID"
	}
	return rule, f
}

// createProposalRule stores a proposal rule for a legal entity in the caller's
// scope (FR-27.21).
func (a *API) createProposalRule(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req proposalRuleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	rule, fields := buildProposalRule(req)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !inScope(w, r, rule.LegalEntityID) {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	saved, err := a.store.CreateProposalRule(r.Context(), rule, p.UserID)
	if err != nil {
		a.fail(w, r, "create proposal rule", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, saved)
}

// listProposalRules returns rules in the caller's scope, optionally for the
// ordered products named by repeated orderedProductId parameters, each with
// its recorded accept and decline counts.
func (a *API) listProposalRules(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.ProposalRuleFilter{LegalEntityID: legalEntity, OrderedProductIDs: q["orderedProductId"]}
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
	items, err := a.store.ListProposalRules(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list proposal rules", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// deleteProposalRule removes a rule; its recorded responses stay.
func (a *API) deleteProposalRule(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeProposalNotFound, "product proposal rule not found"))
		return
	}
	if err := a.store.DeleteProposalRule(r.Context(), id); err != nil {
		a.fail(w, r, "delete proposal rule", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// evaluateProposals returns the products to propose at line commit
// (FR-27.21). It stores nothing; a line is only added when the user accepts.
func (a *API) evaluateProposals(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req evaluateProposalsRequest
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
	if msg := productList(req.ProductIDs, true); msg != "" {
		fields["productIds"] = msg
	}
	if msg := productList(req.CurrentProductIDs, false); msg != "" {
		fields["currentProductIds"] = msg
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !inScope(w, r, req.LegalEntityID) {
		return
	}
	rules, err := a.store.ListProposalRules(r.Context(), store.ProposalRuleFilter{LegalEntityID: req.LegalEntityID,
		OrderedProductIDs: req.ProductIDs, Limit: maxProposalRules})
	if err != nil {
		a.fail(w, r, "list proposal rules", err)
		return
	}
	items := domain.EvaluateProposals(rules, req.CustomerID, req.ProductIDs, req.CurrentProductIDs)
	httpx.JSON(w, http.StatusOK, proposalEvaluation{Items: items})
}

// respondToProposal records a user's accept or decline of a proposal, the
// input of the acceptance rate (FR-27.21). A repeat of Idempotency-Key with the
// same body returns the stored response with 200.
func (a *API) respondToProposal(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key := r.Header.Get(idempotencyKey)
	var req proposalResponseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if key == "" || len(key) > maxKeyLength {
		fields[idempotencyKey] = fmt.Sprintf("is required, at most %d characters", maxKeyLength)
	}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.RuleID) {
		fields["ruleId"] = "must be a UUID"
	}
	if req.CustomerID != nil && !ids.Valid(*req.CustomerID) {
		fields["customerId"] = "must be a UUID"
	}
	if req.OrderID != nil && !ids.Valid(*req.OrderID) {
		fields["orderId"] = "must be a UUID"
	}
	if req.Accepted == nil {
		fields["accepted"] = "is required"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !inScope(w, r, req.LegalEntityID) {
		return
	}
	hash, err := domain.RequestHash(req)
	if err != nil {
		a.fail(w, r, "hash request", err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	saved, created, err := a.store.RecordProposalResponse(r.Context(), store.NewProposalResponse{
		LegalEntityID: req.LegalEntityID, RuleID: req.RuleID, CustomerID: req.CustomerID, OrderID: req.OrderID,
		Accepted: *req.Accepted, IdempotencyKey: key, RequestHash: hash, Actor: p.UserID, At: a.now().UTC(),
	})
	if err != nil {
		a.fail(w, r, "record proposal response", err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, saved)
}
