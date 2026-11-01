package api

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

// DutyBatchRequirementWrite is "Maintain customer batch requirements" (FR-24.19).
// It is not in the PRD duty list; the service enforces it and the policy data
// owner adds it (see NOT-IMPLEMENTED.md).
const DutyBatchRequirementWrite = "production.batch-requirement.write"

const (
	customerRequirementsPath = "/v1/production/customer-batch-requirements"
	checkStoredPath          = "/v1/production/batch-requirements:check-stored"
)

// registerCustomerRequirements mounts the stored customer batch requirements
// and the requirement check over the stored batch register (FR-24.19,
// BR-24.13, AC-24.14).
func (a *API) registerCustomerRequirements(r chi.Router) {
	write := authz.RequireDuty(DutyBatchRequirementWrite)
	read := authz.RequireDuty(DutyRead)
	r.With(write).Post(customerRequirementsPath, a.createRequirement)
	r.With(read).Get(customerRequirementsPath, a.listRequirements)
	r.With(read).Get(customerRequirementsPath+"/{id}", a.getRequirement)
	r.With(write).Post(customerRequirementsPath+"/{id}:retire", a.retireRequirement)
	r.With(read).Post(checkStoredPath, a.checkStored)
}

type requirementRequest struct {
	LegalEntityID   string           `json:"legalEntityId"`
	ItemID          string           `json:"itemId"`
	CustomerID      *string          `json:"customerId"`
	CustomerGroupID *string          `json:"customerGroupId"`
	SellableDays    *int             `json:"sellableDays"`
	Attribute       string           `json:"attribute"`
	Min             *decimal.Decimal `json:"min"`
	Max             *decimal.Decimal `json:"max"`
	SameBatch       *bool            `json:"sameBatch"`
	ValidFrom       *string          `json:"validFrom"`
	ValidTo         *string          `json:"validTo"`
}

func (a *API) createRequirement(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req requirementRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	if req.LegalEntityID == "" {
		req.LegalEntityID = p.LegalEntityID
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID (defaults to the caller's legal entity)"
	}
	if !ids.Valid(req.ItemID) {
		fields["itemId"] = "must be a UUID"
	}
	for name, v := range map[string]*string{"customerId": req.CustomerID, "customerGroupId": req.CustomerGroupID} {
		if v != nil && !ids.Valid(*v) {
			fields[name] = "must be a UUID"
		}
	}
	if req.CustomerID != nil && req.CustomerGroupID != nil {
		fields["customerGroupId"] = "a requirement is for a customer or a customer group, not both"
	}
	if n := len(req.Attribute); n > maxAttrName {
		fields["attribute"] = fmt.Sprintf("at most %d characters", maxAttrName)
	}
	for name, v := range map[string]*decimal.Decimal{"min": req.Min, "max": req.Max} {
		if boundedDecimal(name, v) != nil {
			fields[name] = "must be below 10^12 with at most 6 decimals"
		}
	}
	for name, v := range map[string]*string{"validFrom": req.ValidFrom, "validTo": req.ValidTo} {
		if v == nil {
			continue
		}
		if _, err := parseDate(name, *v); err != nil {
			fields[name] = "must be a date (yyyy-mm-dd)"
		}
	}
	if from, to := req.ValidFrom, req.ValidTo; from != nil && to != nil && fields["validFrom"] == "" && fields["validTo"] == "" && *from > *to {
		fields["validTo"] = "must not be before validFrom"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	level := domain.ScopeItemDefault
	if req.CustomerID != nil {
		level = domain.ScopeCustomer
	} else if req.CustomerGroupID != nil {
		level = domain.ScopeGroup
	}
	if err := domain.ValidateRequirement(domain.Requirement{Level: level, SellableDays: req.SellableDays, Attribute: req.Attribute,
		Min: req.Min, Max: req.Max, SameBatch: req.SameBatch}); err != nil {
		fail(w, r, err)
		return
	}
	if !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity "+req.LegalEntityID+" is outside your scope"))
		return
	}
	rec, fresh, err := a.store.CreateCustomerRequirement(r.Context(), store.CustomerRequirement{LegalEntityID: req.LegalEntityID, ItemID: req.ItemID,
		CustomerID: req.CustomerID, CustomerGroupID: req.CustomerGroupID, SellableDays: req.SellableDays, Attribute: req.Attribute,
		Min: req.Min, Max: req.Max, SameBatch: req.SameBatch, ValidFrom: req.ValidFrom, ValidTo: req.ValidTo}, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusCreated, rec)
}

func (a *API) listRequirements(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.RequirementFilter{ActiveOn: q.Get("activeOn"), Limit: limit}
	fields := map[string]string{}
	// Canonical form, so an upper-case id finds the stored (lower-case) rows.
	for name, dst := range map[string]*string{"legalEntityId": &f.LegalEntityID, "itemId": &f.ItemID, "customerId": &f.CustomerID, "customerGroupId": &f.CustomerGroupID} {
		v := q.Get(name)
		if v == "" {
			continue
		}
		c, ok := ids.Canonical(v)
		if !ok {
			fields[name] = "must be a UUID"
		}
		*dst = c
	}
	if f.ActiveOn != "" {
		if _, err := parseDate("activeOn", f.ActiveOn); err != nil {
			fields["activeOn"] = "must be a date (yyyy-mm-dd)"
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	items, err := a.store.ListCustomerRequirements(r.Context(), f)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getRequirement(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rec, err := a.store.GetCustomerRequirement(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rec)
}

type retireRequest struct {
	Version int `json:"version"`
}

func (a *API) retireRequirement(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req retireRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	if req.Version < 1 {
		invalid(w, r, map[string]string{"version": "must be the version you read (1 or more)"})
		return
	}
	rec, fresh, err := a.store.RetireCustomerRequirement(r.Context(), id, req.Version, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusOK, rec)
}

type checkStoredRequest struct {
	LegalEntityID   string          `json:"legalEntityId"`
	ItemID          string          `json:"itemId"`
	CustomerID      string          `json:"customerId"`
	CustomerGroupID string          `json:"customerGroupId"`
	ShipDate        string          `json:"shipDate"`
	Qty             decimal.Decimal `json:"qty"`
	OnHand          []onHandDTO     `json:"onHand"`
}

type checkStoredResponse struct {
	checkResponse
	// Applied lists the stored rules the requirement took a value from, in
	// precedence order; rules a more specific or later rule overrides are left
	// out.
	Applied []string `json:"applied"`
	// BatchNumbers maps each batch id in the answer to its batch number.
	BatchNumbers map[string]string `json:"batchNumbers"`
}

// checkStored is the customer batch requirement check over the stored batch
// register (FR-24.19, BR-24.13, AC-24.14): the rules come from the stored
// customer batch requirements of the legal entity for the item, the customer
// and the customer's group in force on the ship date, and the disposition,
// expiry and attribute values of each batch from the register; only the
// on-hand quantity per batch is passed. The customer and group ids are
// references to party and sales master data: they are checked for form only
// and the caller names the customer's group. The ship date and the expiry are
// calendar days compared as such (BR-24.13), with no time-zone conversion: the
// ship date is the shipping site's local day. The answer and its codes are
// those of batch-requirements:check.
func (a *API) checkStored(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in checkStoredRequest
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	item, itemOK := ids.Canonical(in.ItemID)
	if !itemOK {
		fields["itemId"] = "must be a UUID"
	}
	if in.LegalEntityID == "" {
		in.LegalEntityID = p.LegalEntityID
	}
	entity, entityOK := ids.Canonical(in.LegalEntityID)
	if !entityOK {
		fields["legalEntityId"] = "must be a UUID (defaults to the caller's legal entity)"
	}
	who := store.ReqScope{}
	for name, v := range map[string]*string{"customerId": &in.CustomerID, "customerGroupId": &in.CustomerGroupID} {
		if *v == "" {
			continue
		}
		c, ok := ids.Canonical(*v)
		if !ok {
			fields[name] = "must be a UUID"
		}
		*v = c
	}
	who.CustomerID, who.CustomerGroupID = in.CustomerID, in.CustomerGroupID
	ship, err := parseDate("shipDate", in.ShipDate)
	if err != nil {
		fields["shipDate"] = "must be a date (yyyy-mm-dd)"
	}
	if !validQty(in.Qty) {
		fields["qty"] = "must be a positive quantity below 10^12 with at most 6 decimals"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	onHand, ok := parseOnHand(w, r, in.OnHand)
	if !ok {
		return
	}
	if !tenant.InLegalEntityScope(p, entity) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity "+entity+" is outside your scope"))
		return
	}
	rules, err := a.store.ApplicableRequirements(r.Context(), entity, item, who, in.ShipDate)
	if err != nil {
		fail(w, r, err)
		return
	}
	rows := make([]domain.Requirement, len(rules))
	for i, rule := range rules {
		rows[i] = rule.DomainReq()
	}
	req, err := domain.Resolve(rows)
	if err != nil {
		fail(w, r, err)
		return
	}
	applied := []string{}
	for _, i := range domain.Deciding(rows) {
		applied = append(applied, rules[i].ID)
	}
	cands, _, err := a.store.PickCandidates(r.Context(), item, onHand)
	if err != nil {
		fail(w, r, err)
		return
	}
	batches := make([]domain.Batch, len(cands))
	numbers := make(map[string]string, len(cands))
	for i, c := range cands {
		batches[i] = c.Batch
		numbers[c.Batch.ID] = c.BatchNumber
	}
	out, err := evaluateRequirement(req, ship, in.Qty, batches)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, checkStoredResponse{checkResponse: out, Applied: applied, BatchNumbers: numbers})
}

// parseOnHand validates the on-hand list of a stored-batch request and puts the
// batch ids in canonical form, so an upper-case id matches the register and
// cannot be listed twice under two spellings.
func parseOnHand(w http.ResponseWriter, r *http.Request, in []onHandDTO) ([]store.OnHand, bool) {
	if len(in) == 0 || len(in) > maxBatches {
		invalid(w, r, map[string]string{"onHand": fmt.Sprintf("must list 1 to %d batches", maxBatches)})
		return nil, false
	}
	seen := map[string]bool{}
	out := make([]store.OnHand, len(in))
	for i, oh := range in {
		id, ok := ids.Canonical(oh.BatchID)
		if !ok || seen[id] {
			invalid(w, r, map[string]string{"onHand": "batchId must be a distinct UUID per entry"})
			return nil, false
		}
		if !validQtyOrZero(oh.Qty) {
			invalid(w, r, map[string]string{"onHand": "qty must be from 0 below 10^12 with at most 6 decimals"})
			return nil, false
		}
		seen[id] = true
		out[i] = store.OnHand{BatchID: id, Qty: oh.Qty}
	}
	return out, true
}
