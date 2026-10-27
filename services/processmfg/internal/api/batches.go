package api

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
)

// Duties of the slice-2 batch rules (PRD 4.24.17): "Maintain batch attributes"
// covers manual derivation, "Merge batches" the merge and "Approve batch
// records" the QA approval. Consolidation is under "Maintain batch orders"
// (DutyBatchOrderWrite); the customer requirement check is an inquiry
// (DutyRead).
const (
	DutyBatchAttributeWrite = "production.batch-attribute.write"
	DutyBatchMerge          = "production.batch.merge"
	DutyBatchRecordApprove  = "production.batch-record.approve"
)

// Bounds of one preview request.
const (
	maxBatches   = 200
	maxRefLen    = 80
	maxAttrName  = 40
	maxTextValue = 200
	dateLayout   = "2006-01-02"
)

// registerBatches mounts the slice-2 previews. They store nothing: the batch
// store is the wave-16 InventoryBatch, and these rules run against it once the
// batch-order engine posts (see NOT-IMPLEMENTED.md). The paths stay under
// /v1/production because /v1/inventory belongs to services/inventory.
func registerBatches(r chi.Router) {
	r.With(authz.RequireDuty(DutyBatchAttributeWrite)).Post("/v1/production/batch-derivation:preview", handle(derive))
	r.With(authz.RequireDuty(DutyBatchMerge)).Post("/v1/production/batch-merge:preview", handle(merge))
	r.With(authz.RequireDuty(DutyRead)).Post("/v1/production/batch-requirements:check", handle(checkRequirements))
	r.With(authz.RequireDuty(DutyBatchOrderWrite)).Post("/v1/production/bulk-consolidation:preview", handle(consolidate))
	r.With(authz.RequireDuty(DutyBatchRecordApprove)).Post("/v1/production/batch-records:approve-check", handle(approveCheck))
}

func invalidf(format string, args ...any) error {
	return &domain.Error{Code: domain.CodeInvalid, Detail: fmt.Sprintf(format, args...)}
}

// validRef reports whether s is a usable reference (batch, order or item id as
// the caller knows it).
func validRef(s string) bool {
	return strings.TrimSpace(s) != "" && len(s) <= maxRefLen
}

func parseDate(field, s string) (time.Time, error) {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return time.Time{}, invalidf("%s must be a date (yyyy-mm-dd)", field)
	}
	return t, nil
}

// valueDTO is one attribute value on the wire: exactly one of num, text or
// date (yyyy-mm-dd), or none for a missing value.
type valueDTO struct {
	Num  *decimal.Decimal `json:"num,omitempty"`
	Text *string          `json:"text,omitempty"`
	Date *string          `json:"date,omitempty"`
}

func (v valueDTO) toValue(field string) (domain.Value, error) {
	var out domain.Value
	set := 0
	if v.Num != nil {
		if v.Num.Exponent() < -domain.QtyPrecision || v.Num.Abs().GreaterThanOrEqual(maxQty()) {
			return out, invalidf("%s.num must be below 10^12 with at most 6 decimals", field)
		}
		out.Num = v.Num
		set++
	}
	if v.Text != nil {
		if len(*v.Text) > maxTextValue {
			return out, invalidf("%s.text must be at most %d characters", field, maxTextValue)
		}
		out.Text = v.Text
		set++
	}
	if v.Date != nil {
		t, err := parseDate(field+".date", *v.Date)
		if err != nil {
			return out, err
		}
		out.Date = &t
		set++
	}
	if set > 1 {
		return domain.Value{}, invalidf("%s carries more than one of num, text and date", field)
	}
	return out, nil
}

func fromValue(v domain.Value) valueDTO {
	out := valueDTO{Num: v.Num, Text: v.Text}
	if v.Date != nil {
		s := v.Date.Format(dateLayout)
		out.Date = &s
	}
	return out
}

type ruleDTO struct {
	Attribute   string   `json:"attribute"`
	Aggregation string   `json:"aggregation"`
	Decimals    *int32   `json:"decimals,omitempty"`
	Fixed       valueDTO `json:"fixed"`
}

type senderDTO struct {
	BatchID string          `json:"batchId"`
	Value   valueDTO        `json:"value"`
	Qty     decimal.Decimal `json:"qty"`
}

type deriveRequest struct {
	Rule    ruleDTO     `json:"rule"`
	Senders []senderDTO `json:"senders"`
}

type deriveResponse struct {
	Attribute      string   `json:"attribute"`
	Value          valueDTO `json:"value"`
	MissingSenders []string `json:"missingSenders"`
	WarningCode    string   `json:"warningCode,omitempty"`
}

// derive previews a batch derivation (FR-24.15, BR-24.11): the receiver value
// and the senders without a value, which the derivation log records as
// missing-sender-value (PMF-0007).
func derive(in deriveRequest) (deriveResponse, error) {
	if n := len(in.Rule.Attribute); n < 1 || n > maxAttrName {
		return deriveResponse{}, invalidf("rule.attribute must be 1 to %d characters", maxAttrName)
	}
	if len(in.Senders) > maxBatches {
		return deriveResponse{}, invalidf("at most %d senders", maxBatches)
	}
	fixed, err := in.Rule.Fixed.toValue("rule.fixed")
	if err != nil {
		return deriveResponse{}, err
	}
	rule := domain.Rule{Attribute: in.Rule.Attribute, Aggregation: domain.Aggregation(in.Rule.Aggregation), Fixed: fixed, Decimals: -1}
	if in.Rule.Decimals != nil {
		if *in.Rule.Decimals < 0 {
			return deriveResponse{}, invalidf("rule.decimals must not be negative")
		}
		rule.Decimals = *in.Rule.Decimals
	}
	senders := make([]domain.Sender, len(in.Senders))
	for i, s := range in.Senders {
		field := fmt.Sprintf("senders[%d]", i)
		if !validRef(s.BatchID) {
			return deriveResponse{}, invalidf("%s.batchId must be 1 to %d characters", field, maxRefLen)
		}
		v, err := s.Value.toValue(field + ".value")
		if err != nil {
			return deriveResponse{}, err
		}
		if s.Qty.IsNegative() || !validQtyOrZero(s.Qty) {
			return deriveResponse{}, invalidf("%s.qty must be a quantity from 0 below 10^12 with at most 6 decimals", field)
		}
		senders[i] = domain.Sender{BatchID: s.BatchID, Value: v, Qty: s.Qty}
	}
	d, err := domain.Derive(rule, senders)
	if err != nil {
		return deriveResponse{}, err
	}
	out := deriveResponse{Attribute: rule.Attribute, Value: fromValue(d.Value), MissingSenders: d.Missing}
	if out.MissingSenders == nil {
		out.MissingSenders = []string{}
	}
	if len(d.Missing) > 0 {
		out.WarningCode = domain.CodeMissingSender
	}
	return out, nil
}

func validQtyOrZero(d decimal.Decimal) bool {
	return d.IsZero() || validQty(d)
}

type batchDTO struct {
	ID          string              `json:"id"`
	ItemID      string              `json:"itemId"`
	WarehouseID string              `json:"warehouseId"`
	CWUnit      string              `json:"cwUnit"`
	Disposition string              `json:"disposition"`
	Qty         decimal.Decimal     `json:"qty"`
	UnitCost    decimal.Decimal     `json:"unitCost"`
	Values      map[string]valueDTO `json:"values"`
}

var dispositions = map[string]bool{"available": true, "on-hold": true, "rejected": true, "quarantine": true}

// toBatch validates one batch. An omitted disposition is available.
func (b batchDTO) toBatch(field string) (domain.Batch, error) {
	if !validRef(b.ID) {
		return domain.Batch{}, invalidf("%s.id must be 1 to %d characters", field, maxRefLen)
	}
	if b.Disposition == "" {
		b.Disposition = domain.DispositionAvailable
	}
	if !dispositions[b.Disposition] {
		return domain.Batch{}, invalidf("%s.disposition must be available, on-hold, rejected or quarantine", field)
	}
	if !validQty(b.Qty) {
		return domain.Batch{}, invalidf("%s.qty must be a positive quantity below 10^12 with at most 6 decimals", field)
	}
	if b.UnitCost.IsNegative() || b.UnitCost.Exponent() < -domain.QtyPrecision || b.UnitCost.GreaterThanOrEqual(maxQty()) {
		return domain.Batch{}, invalidf("%s.unitCost must be from 0 below 10^12 with at most 6 decimals", field)
	}
	if len(b.Values) > domain.MaxFormulaLines {
		return domain.Batch{}, invalidf("%s.values has more than %d attributes", field, domain.MaxFormulaLines)
	}
	vals := make(map[string]domain.Value, len(b.Values))
	for name, v := range b.Values {
		if n := len(name); n < 1 || n > maxAttrName {
			return domain.Batch{}, invalidf("%s.values: attribute names must be 1 to %d characters", field, maxAttrName)
		}
		dv, err := v.toValue(field + ".values." + name)
		if err != nil {
			return domain.Batch{}, err
		}
		vals[name] = dv
	}
	return domain.Batch{ID: b.ID, ItemID: b.ItemID, WarehouseID: b.WarehouseID, CWUnit: b.CWUnit,
		Disposition: b.Disposition, Qty: b.Qty, UnitCost: b.UnitCost, Values: vals}, nil
}

func toBatches(in []batchDTO) ([]domain.Batch, error) {
	if len(in) > maxBatches {
		return nil, invalidf("at most %d batches", maxBatches)
	}
	out := make([]domain.Batch, len(in))
	for i, b := range in {
		db, err := b.toBatch(fmt.Sprintf("batches[%d]", i))
		if err != nil {
			return nil, err
		}
		out[i] = db
	}
	return out, nil
}

type mergeRequest struct {
	Batches       []batchDTO `json:"batches"`
	MergeOverride []string   `json:"mergeOverride"`
}

type mergeResponse struct {
	Qty         decimal.Decimal     `json:"qty"`
	UnitCost    decimal.Decimal     `json:"unitCost"`
	Disposition string              `json:"disposition"`
	Values      map[string]valueDTO `json:"values"`
	Parents     []string            `json:"parents"`
}

// merge previews a batch merge (FR-24.16): the target quantity, cost per unit,
// attributes and genealogy parents. Refusals are PMF-0006.
func merge(in mergeRequest) (mergeResponse, error) {
	srcs, err := toBatches(in.Batches)
	if err != nil {
		return mergeResponse{}, err
	}
	override := make(map[string]bool, len(in.MergeOverride))
	for _, n := range in.MergeOverride {
		override[n] = true
	}
	res, err := domain.Merge(srcs, override)
	if err != nil {
		return mergeResponse{}, err
	}
	out := mergeResponse{Qty: res.Qty, UnitCost: res.UnitCost, Disposition: res.Disposition,
		Values: make(map[string]valueDTO, len(res.Values)), Parents: res.Parents}
	for k, v := range res.Values {
		out.Values[k] = fromValue(v)
	}
	return out, nil
}

type requirementDTO struct {
	Level        int              `json:"level"`
	SellableDays *int             `json:"sellableDays,omitempty"`
	Attribute    string           `json:"attribute,omitempty"`
	Min          *decimal.Decimal `json:"min,omitempty"`
	Max          *decimal.Decimal `json:"max,omitempty"`
	SameBatch    *bool            `json:"sameBatch,omitempty"`
}

type checkRequest struct {
	Requirements []requirementDTO `json:"requirements"`
	ShipDate     string           `json:"shipDate"`
	Qty          decimal.Decimal  `json:"qty"`
	Batches      []batchDTO       `json:"batches"`
}

type rangeDTO struct {
	Min *decimal.Decimal `json:"min,omitempty"`
	Max *decimal.Decimal `json:"max,omitempty"`
}

type resolvedDTO struct {
	SellableDays int                 `json:"sellableDays"`
	SameBatch    bool                `json:"sameBatch"`
	Ranges       map[string]rangeDTO `json:"ranges"`
}

type qualifyingDTO struct {
	BatchID string          `json:"batchId"`
	Qty     decimal.Decimal `json:"qty"`
	Expiry  *string         `json:"expiry,omitempty"`
}

type excludedDTO struct {
	BatchID     string `json:"batchId"`
	Reason      string `json:"reason"`
	WarningCode string `json:"warningCode"`
}

type checkResponse struct {
	Requirement  resolvedDTO     `json:"requirement"`
	Qualifying   []qualifyingDTO `json:"qualifying"`
	Excluded     []excludedDTO   `json:"excluded"`
	QtyAvailable decimal.Decimal `json:"qtyAvailable"`
	SameBatchID  *string         `json:"sameBatchId"`
}

// checkRequirements is the customer batch requirement check (FR-24.19,
// BR-24.13, PRD 4.24.14): it resolves the rows that apply by precedence,
// splits the candidate batches into qualifying and excluded (PMF-0009 with the
// reason) and, for a same-batch line, picks the batch that supplies the whole
// quantity or answers 409 PMF-0008.
func checkRequirements(in checkRequest) (checkResponse, error) {
	ship, err := parseDate("shipDate", in.ShipDate)
	if err != nil {
		return checkResponse{}, err
	}
	if !validQty(in.Qty) {
		return checkResponse{}, invalidf("qty must be a positive quantity below 10^12 with at most 6 decimals")
	}
	if len(in.Requirements) > maxBatches {
		return checkResponse{}, invalidf("at most %d requirements", maxBatches)
	}
	rows := make([]domain.Requirement, len(in.Requirements))
	for i, r := range in.Requirements {
		if len(r.Attribute) > maxAttrName {
			return checkResponse{}, invalidf("requirements[%d].attribute must be at most %d characters", i, maxAttrName)
		}
		rows[i] = domain.Requirement{Level: r.Level, SellableDays: r.SellableDays, Attribute: r.Attribute, Min: r.Min, Max: r.Max, SameBatch: r.SameBatch}
	}
	req, err := domain.Resolve(rows)
	if err != nil {
		return checkResponse{}, err
	}
	batches, err := toBatches(in.Batches)
	if err != nil {
		return checkResponse{}, err
	}
	out := checkResponse{
		Requirement: resolvedDTO{SellableDays: req.SellableDays, SameBatch: req.SameBatch, Ranges: map[string]rangeDTO{}},
		Qualifying:  []qualifyingDTO{},
		Excluded:    []excludedDTO{},
	}
	for n, rg := range req.Ranges {
		out.Requirement.Ranges[n] = rangeDTO{Min: rg.Min, Max: rg.Max}
	}
	var ok []domain.Batch
	for _, b := range batches {
		if reason := domain.Check(b, req, ship); reason != "" {
			out.Excluded = append(out.Excluded, excludedDTO{BatchID: b.ID, Reason: reason, WarningCode: domain.CodeRequirementNotMet})
			continue
		}
		ok = append(ok, b)
		q := qualifyingDTO{BatchID: b.ID, Qty: b.Qty}
		if e := b.Values["expiry"]; e.Date != nil {
			s := e.Date.Format(dateLayout)
			q.Expiry = &s
		}
		out.Qualifying = append(out.Qualifying, q)
		out.QtyAvailable = out.QtyAvailable.Add(b.Qty)
	}
	if req.SameBatch {
		pick, err := domain.SelectSameBatch(ok, in.Qty)
		if err != nil {
			return checkResponse{}, err
		}
		out.SameBatchID = &pick.ID
	}
	return out, nil
}

type packedOrderDTO struct {
	OrderID string          `json:"orderId"`
	ItemID  string          `json:"itemId"`
	Units   decimal.Decimal `json:"units"`
	Due     string          `json:"due"`
}

type relationDTO struct {
	PackQtyPerUnit decimal.Decimal `json:"packQtyPerUnit"`
	Multiple       decimal.Decimal `json:"multiple"`
	Max            decimal.Decimal `json:"max"`
}

type consolidateRequest struct {
	Relation     relationDTO      `json:"relation"`
	PackedOrders []packedOrderDTO `json:"packedOrders"`
}

type pegDTO struct {
	OrderID string          `json:"orderId"`
	ItemID  string          `json:"itemId"`
	BulkQty decimal.Decimal `json:"bulkQty"`
}

type bulkOrderDTO struct {
	Qty  decimal.Decimal `json:"qty"`
	Pegs []pegDTO        `json:"pegs"`
}

type consolidateResponse struct {
	BulkOrders []bulkOrderDTO `json:"bulkOrders"`
}

// consolidate previews the bulk batch orders for packed demand and the pegging
// of the packing orders (FR-24.17, BR-24.12, AC-24.13).
func consolidate(in consolidateRequest) (consolidateResponse, error) {
	if len(in.PackedOrders) > maxBatches {
		return consolidateResponse{}, invalidf("at most %d packed orders", maxBatches)
	}
	rel := domain.BulkPack{PackQtyPerUnit: in.Relation.PackQtyPerUnit, Multiple: in.Relation.Multiple, Max: in.Relation.Max}
	for name, v := range map[string]decimal.Decimal{"packQtyPerUnit": rel.PackQtyPerUnit, "multiple": rel.Multiple} {
		if !validQty(v) {
			return consolidateResponse{}, invalidf("relation.%s must be a positive quantity below 10^12 with at most 6 decimals", name)
		}
	}
	if !validQtyOrZero(rel.Max) {
		return consolidateResponse{}, invalidf("relation.max must be 0 (no maximum) or a quantity below 10^12 with at most 6 decimals")
	}
	demands := make([]domain.PackedDemand, len(in.PackedOrders))
	for i, p := range in.PackedOrders {
		field := fmt.Sprintf("packedOrders[%d]", i)
		if !validRef(p.OrderID) {
			return consolidateResponse{}, invalidf("%s.orderId must be 1 to %d characters", field, maxRefLen)
		}
		if !validQty(p.Units) {
			return consolidateResponse{}, invalidf("%s.units must be a positive quantity below 10^12 with at most 6 decimals", field)
		}
		due, err := parseDate(field+".due", p.Due)
		if err != nil {
			return consolidateResponse{}, err
		}
		demands[i] = domain.PackedDemand{OrderID: p.OrderID, ItemID: p.ItemID, Units: p.Units, Due: due}
	}
	orders, err := domain.Consolidate(demands, rel)
	if err != nil {
		return consolidateResponse{}, err
	}
	out := consolidateResponse{BulkOrders: make([]bulkOrderDTO, len(orders))}
	for i, o := range orders {
		pegs := make([]pegDTO, len(o.Pegs))
		for j, p := range o.Pegs {
			pegs[j] = pegDTO{OrderID: p.OrderID, ItemID: p.ItemID, BulkQty: p.BulkQty}
		}
		out.BulkOrders[i] = bulkOrderDTO{Qty: o.Qty, Pegs: pegs}
	}
	return out, nil
}

type exceptionDTO struct {
	ID          string `json:"id"`
	Disposition string `json:"disposition"`
	SignatureID string `json:"signatureId"`
}

type approveRequest struct {
	Exceptions []exceptionDTO `json:"exceptions"`
}

type approvableResponse struct {
	Approvable bool `json:"approvable"`
	Exceptions int  `json:"exceptions"`
}

// approveCheck applies BR-24.15: QA approval is refused while any exception
// lacks a disposition or a signature (PMF-0014).
func approveCheck(in approveRequest) (approvableResponse, error) {
	if len(in.Exceptions) > maxBatches {
		return approvableResponse{}, invalidf("at most %d exceptions", maxBatches)
	}
	exc := make([]domain.Exception, len(in.Exceptions))
	for i, e := range in.Exceptions {
		if !validRef(e.ID) {
			return approvableResponse{}, invalidf("exceptions[%d].id must be 1 to %d characters", i, maxRefLen)
		}
		exc[i] = domain.Exception{ID: e.ID, Disposition: e.Disposition, SignatureID: e.SignatureID}
	}
	if err := domain.CheckApprove(exc); err != nil {
		return approvableResponse{}, err
	}
	return approvableResponse{Approvable: true, Exceptions: len(exc)}, nil
}
