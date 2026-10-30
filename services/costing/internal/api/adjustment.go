package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/costing/internal/store"
)

const (
	// dutyCostAdjust posts manual cost adjustments (the PRD's CostAdjust).
	dutyCostAdjust = "costing.adjust"
	maxReason      = 500
)

var (
	adjustmentKinds = map[string]bool{domain.AdjustOnHand: true, domain.AdjustReceipt: true}
	adjustmentModes = map[string]bool{domain.ModeUnitCost: true, domain.ModeAmount: true}
)

// adjustmentRequest is a manual adjustment (FR-20.6). An ON_HAND adjustment
// names the product, site and optionally warehouse and valuation type; a
// RECEIPT adjustment names receiptTransactionId and is by amount. unitCost is
// the new unit cost (UNIT_COST), amount the signed change (AMOUNT).
// expectedValue is the current value the caller previewed.
type adjustmentRequest struct {
	LegalEntityID        string  `json:"legalEntityId"`
	Kind                 string  `json:"kind"`
	Mode                 string  `json:"mode"`
	ProductID            string  `json:"productId"`
	SiteID               string  `json:"siteId"`
	WarehouseID          string  `json:"warehouseId"`
	ValuationType        string  `json:"valuationType"`
	ValuationLedgerID    string  `json:"valuationLedgerId"`
	ReceiptTransactionID string  `json:"receiptTransactionId"`
	FinancialDate        string  `json:"financialDate"`
	UnitCost             *string `json:"unitCost"`
	Amount               *string `json:"amount"`
	ExpectedValue        *string `json:"expectedValue"`
	Reason               string  `json:"reason"`
}

// requestHash fingerprints a normalised request for Idempotency-Key reuse.
func requestHash(v any) string {
	raw, _ := json.Marshal(v) // the request structs always marshal
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// validateAdjustment checks a request and returns its store form. A post
// needs a reason; a preview does not. The financial date may not be ahead of
// today (UTC): an adjustment values stock that exists.
func validateAdjustment(fields map[string]string, req adjustmentRequest, post bool, today time.Time) store.AdjustmentRequest {
	out := store.AdjustmentRequest{LegalEntityID: strings.ToLower(req.LegalEntityID), Kind: req.Kind, Mode: req.Mode,
		FinancialDate: req.FinancialDate, Reason: strings.TrimSpace(req.Reason)}
	if !ids.Valid(out.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !adjustmentKinds[req.Kind] {
		fields["kind"] = "must be ON_HAND or RECEIPT"
	}
	if !adjustmentModes[req.Mode] {
		fields["mode"] = "must be UNIT_COST or AMOUNT"
	}
	out.ValuationLedgerID = optionalUUID(fields, "valuationLedgerId", req.ValuationLedgerID)
	switch req.Kind {
	case domain.AdjustOnHand:
		for name, v := range map[string]string{"productId": req.ProductID, "siteId": req.SiteID} {
			if !ids.Valid(v) {
				fields[name] = "must be a UUID"
			}
		}
		out.ProductID, out.SiteID = strings.ToLower(req.ProductID), strings.ToLower(req.SiteID)
		out.WarehouseID = optionalUUID(fields, "warehouseId", req.WarehouseID)
		out.ValuationType = req.ValuationType
		if len(req.ValuationType) > maxValuationType {
			fields["valuationType"] = fmt.Sprintf("must be at most %d characters", maxValuationType)
		}
		if req.ReceiptTransactionID != "" {
			fields["receiptTransactionId"] = "is only allowed on a RECEIPT adjustment"
		}
	case domain.AdjustReceipt:
		out.ReceiptTransactionID = strings.ToLower(req.ReceiptTransactionID)
		if !ids.Valid(out.ReceiptTransactionID) {
			fields["receiptTransactionId"] = "must be a UUID"
		}
		for name, v := range map[string]string{"productId": req.ProductID, "siteId": req.SiteID, "warehouseId": req.WarehouseID,
			"valuationType": req.ValuationType} {
			if v != "" {
				fields[name] = "is taken from the receipt; leave it out"
			}
		}
		if req.Mode == domain.ModeUnitCost {
			fields["mode"] = "a receipt is adjusted by AMOUNT"
		}
	}
	switch req.Mode {
	case domain.ModeUnitCost:
		if req.UnitCost == nil {
			fields["unitCost"] = "is required for UNIT_COST"
		} else if d := parseDecimal(fields, "unitCost", *req.UnitCost, true); fields["unitCost"] == "" {
			if !d.IsPositive() || !domain.ValidUnitCost(d) {
				fields["unitCost"] = "must be greater than 0 with at most 14 integer digits and 6 decimal places"
			}
			out.UnitCost = d.String()
		}
		if req.Amount != nil {
			fields["amount"] = "is only allowed for AMOUNT"
		}
	case domain.ModeAmount:
		if req.Amount == nil {
			fields["amount"] = "is required for AMOUNT"
		} else {
			out.Amount = boundedDecimal(fields, "amount", *req.Amount, amountScale, amountDigits).String()
		}
		if req.UnitCost != nil {
			fields["unitCost"] = "is only allowed for UNIT_COST"
		}
	}
	if d := parseDate(fields, "financialDate", req.FinancialDate); fields["financialDate"] == "" && d.After(today) {
		fields["financialDate"] = "must not be after today"
	}
	if req.ExpectedValue != nil {
		out.ExpectedValue = parseDecimal(fields, "expectedValue", *req.ExpectedValue, false).String()
	}
	if post && (out.Reason == "" || len(out.Reason) > maxReason) {
		fields["reason"] = fmt.Sprintf("is required, at most %d characters", maxReason)
	}
	return out
}

// decodeAdjustment reads and validates an adjustment request, checks the
// caller's scope and resolves the valuation ledger to its stored form.
func (a *API) decodeAdjustment(w http.ResponseWriter, r *http.Request, post bool) (store.AdjustmentRequest, bool) {
	var req adjustmentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return store.AdjustmentRequest{}, false
	}
	fields := map[string]string{}
	if post {
		if key := strings.TrimSpace(r.Header.Get(idempotencyKey)); key == "" || len(key) > maxIdempotencyKey {
			fields[idempotencyKey] = fmt.Sprintf("is required, at most %d characters", maxIdempotencyKey)
		}
	}
	in := validateAdjustment(fields, req, post, time.Now().UTC())
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return store.AdjustmentRequest{}, false
	}
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, in.LegalEntityID) {
		httpx.Fail(w, r, outOfScope())
		return store.AdjustmentRequest{}, false
	}
	sel, err := a.selectLedger(r, in.LegalEntityID, in.ValuationLedgerID)
	if err != nil {
		a.fail(w, r, "cost adjustment", err)
		return store.AdjustmentRequest{}, false
	}
	in.ValuationLedgerID = sel.queryID
	return in, true
}

// previewAdjustment shows what an adjustment would post: the current quantity
// and value, the new value and the delta (the PRD's voucher preview).
func (a *API) previewAdjustment(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	in, ok := a.decodeAdjustment(w, r, false)
	if !ok {
		return
	}
	out, err := a.store.PreviewAdjustment(r.Context(), in)
	if err != nil {
		a.fail(w, r, "preview cost adjustment", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// createAdjustment posts a manual adjustment: a MANUAL_ADJ cost transaction
// moves the value report at once and the next close flows it to issues.
func (a *API) createAdjustment(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	in, ok := a.decodeAdjustment(w, r, true)
	if !ok {
		return
	}
	out, created, err := a.store.CreateAdjustment(r.Context(), in, strings.TrimSpace(r.Header.Get(idempotencyKey)), requestHash(in))
	if err != nil {
		a.fail(w, r, "post cost adjustment", err)
		return
	}
	if !created {
		httpx.JSON(w, http.StatusOK, out)
		return
	}
	a.adjustmentsPosted.Add(1)
	httpx.JSON(w, http.StatusCreated, out)
}

type adjustmentListResponse struct {
	Items      []store.Adjustment `json:"items"`
	NextCursor string             `json:"nextCursor,omitempty"`
}

// listAdjustments lists posted adjustments, newest financial date first.
func (a *API) listAdjustments(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, fields := pageParams(r, false)
	if date, _, _ := strings.Cut(after, "|"); after != "" {
		parseDate(fields, "cursor", date)
	}
	q := r.URL.Query()
	f := store.AdjustmentFilter{Kind: q.Get("kind")}
	f.ProductID = optionalUUID(fields, "productId", q.Get("productId"))
	if f.Kind != "" && !adjustmentKinds[f.Kind] {
		fields["kind"] = "must be ON_HAND or RECEIPT"
	}
	le, ok := legalEntityFilter(w, r, fields)
	if !ok {
		return
	}
	f.LegalEntityID = le
	page, err := a.store.ListAdjustments(r.Context(), f, after, limit)
	if err != nil {
		a.fail(w, r, "list cost adjustments", err)
		return
	}
	httpx.JSON(w, http.StatusOK, adjustmentListResponse{Items: page.Items, NextCursor: cursor(page.Next)})
}

func (a *API) getAdjustment(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeAdjustmentNotFound, "cost adjustment not found"))
		return
	}
	adj, err := a.store.GetAdjustment(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get cost adjustment", err)
		return
	}
	httpx.JSON(w, http.StatusOK, adj)
}
