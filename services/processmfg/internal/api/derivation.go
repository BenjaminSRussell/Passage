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

// DutyInheritanceRuleWrite is "Maintain batch inheritance rules" (FR-24.15). It
// is not in the PRD duty list; the service enforces it and the policy data owner
// adds it (see NOT-IMPLEMENTED.md).
const DutyInheritanceRuleWrite = "production.inheritance-rule.write"

const (
	inheritanceRulesPath = "/v1/production/inheritance-rules"
	derivationsPath      = "/v1/production/batch-derivations"
)

// registerDerivation mounts the attribute inheritance rules, the derivation of
// a stored batch from its senders and the derivation log (FR-24.15, AC-24.11).
// Deriving writes attribute values, so it takes the duty of recording them.
func (a *API) registerDerivation(r chi.Router) {
	write := authz.RequireDuty(DutyInheritanceRuleWrite)
	read := authz.RequireDuty(DutyRead)
	r.With(write).Post(inheritanceRulesPath, a.createRule)
	r.With(read).Get(inheritanceRulesPath, a.listRules)
	r.With(read).Get(inheritanceRulesPath+"/{id}", a.getRule)
	r.With(write).Post(inheritanceRulesPath+"/{id}:retire", a.retireRule)
	r.With(authz.RequireDuty(DutyBatchAttributeWrite)).Post("/v1/production/batches/{id}:derive", a.deriveStored)
	r.With(read).Get(derivationsPath, a.listDerivations)
}

type ruleRequest struct {
	LegalEntityID string    `json:"legalEntityId"`
	ItemID        string    `json:"itemId"`
	Attribute     string    `json:"attribute"`
	Aggregation   string    `json:"aggregation"`
	Decimals      *int      `json:"decimals"`
	Fixed         *valueDTO `json:"fixed"`
}

func (a *API) createRule(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req ruleRequest
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
	if n := len(req.Attribute); n < 1 || n > maxAttrName {
		fields["attribute"] = fmt.Sprintf("must be 1 to %d characters", maxAttrName)
	}
	if req.Decimals != nil && (*req.Decimals < 0 || int32(*req.Decimals) > domain.StoredDecimals) {
		fields["decimals"] = fmt.Sprintf("must be 0 to %d", domain.StoredDecimals)
	}
	var fixed domain.Value
	if req.Fixed != nil {
		v, err := req.Fixed.toValue("fixed")
		if err != nil {
			fail(w, r, err)
			return
		}
		fixed = v
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	rule := domain.Rule{Attribute: req.Attribute, Aggregation: domain.Aggregation(req.Aggregation), Decimals: -1, Fixed: fixed}
	if req.Decimals != nil {
		rule.Decimals = int32(*req.Decimals)
	}
	// The kind of the attribute is checked against the item's definition in the
	// store; here the checks that need no kind.
	switch rule.Aggregation {
	case domain.AggCopy, domain.AggMin, domain.AggMax, domain.AggWeightedAverage, domain.AggEarliestDate, domain.AggLatestDate, domain.AggFixed:
	default:
		invalid(w, r, map[string]string{"aggregation": "must be copy, min, max, weighted-average, earliest-date, latest-date or fixed"})
		return
	}
	if !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity "+req.LegalEntityID+" is outside your scope"))
		return
	}
	in := store.InheritanceRule{LegalEntityID: req.LegalEntityID, ItemID: req.ItemID, Attribute: req.Attribute, Aggregation: req.Aggregation,
		Decimals: req.Decimals, FixedNum: fixed.Num, FixedText: fixed.Text}
	if fixed.Date != nil {
		d := fixed.Date.Format(dateLayout)
		in.FixedDate = &d
	}
	rec, fresh, err := a.store.CreateInheritanceRule(r.Context(), in, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusCreated, rec)
}

func (a *API) listRules(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.RuleFilter{Limit: limit, Active: q.Get("active") == "true"}
	fields := map[string]string{}
	for name, dst := range map[string]*string{"legalEntityId": &f.LegalEntityID, "itemId": &f.ItemID} {
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
	if v := q.Get("active"); v != "" && v != "true" && v != "false" {
		fields["active"] = "must be true or false"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	items, err := a.store.ListInheritanceRules(r.Context(), f)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getRule(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	rec, err := a.store.GetInheritanceRule(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rec)
}

func (a *API) retireRule(w http.ResponseWriter, r *http.Request) {
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
	rec, fresh, err := a.store.RetireInheritanceRule(r.Context(), id, req.Version, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusOK, rec)
}

type deriveSenderDTO struct {
	BatchID string          `json:"batchId"`
	Qty     decimal.Decimal `json:"qty"`
}

type deriveStoredRequest struct {
	Version int               `json:"version"`
	Senders []deriveSenderDTO `json:"senders"`
}

// deriveStored derives the attributes of a stored receiver batch from its
// sender batches with the item's active inheritance rules (FR-24.15, BR-24.11).
// The senders' values come from the batch register; the caller passes the
// quantity consumed from each (the weight of a weighted average) and the
// receiver version it read. Running it again re-derives and moves the version.
func (a *API) deriveStored(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req deriveStoredRequest
	idem, ok := decodeCommand(w, r, &req)
	if !ok {
		return
	}
	fields := map[string]string{}
	if req.Version < 1 {
		fields["version"] = "must be the version you read (1 or more)"
	}
	if n := len(req.Senders); n < 1 || n > store.MaxSenders {
		fields["senders"] = fmt.Sprintf("must list 1 to %d batches", store.MaxSenders)
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	seen := map[string]bool{}
	in := store.DeriveInput{BatchID: id, Version: req.Version, Senders: make([]store.DeriveSender, len(req.Senders))}
	for i, s := range req.Senders {
		sid, ok := ids.Canonical(s.BatchID)
		if !ok || seen[sid] {
			invalid(w, r, map[string]string{"senders": "batchId must be a distinct UUID per entry"})
			return
		}
		if !validQtyOrZero(s.Qty) {
			invalid(w, r, map[string]string{"senders": "qty must be from 0 below 10^12 with at most 6 decimals"})
			return
		}
		seen[sid] = true
		in.Senders[i] = store.DeriveSender{BatchID: sid, Qty: s.Qty}
	}
	res, fresh, err := a.store.DeriveBatch(r.Context(), in, idem)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCommand(w, fresh, http.StatusOK, res)
}

// listDerivations is the derivation monitor (FR-24.15): the log, newest first,
// by receiver batch or item, or only the derivations with senders that had no
// value (PMF-0007).
func (a *API) listDerivations(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.DerivationFilter{Limit: limit, WithMissing: q.Get("withMissing") == "true"}
	fields := map[string]string{}
	for name, dst := range map[string]*string{"batchId": &f.BatchID, "itemId": &f.ItemID} {
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
	if v := q.Get("withMissing"); v != "" && v != "true" && v != "false" {
		fields["withMissing"] = "must be true or false"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	items, err := a.store.ListDerivations(r.Context(), f)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}
