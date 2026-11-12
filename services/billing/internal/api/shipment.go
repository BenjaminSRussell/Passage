package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

const (
	// maxSlips bounds the packing slips of one POST /proposals request.
	maxSlips = 200
	// maxExplainRef bounds a price explanation reference (w26 explain id).
	maxExplainRef = 200
)

// slipLineDTO is one posted packing slip line offered for billing (FR-29.1):
// the shipped quantity with the order line's price, discount, revenue account
// and tax code. The packing slip and order services own these rows; until the
// packing-slip.posted consumer exists the caller sends them.
type slipLineDTO struct {
	ID              string `json:"id"`
	OrderLineID     string `json:"orderLineId"`
	ProductID       string `json:"productId"`
	Description     string `json:"description"`
	Qty             string `json:"qty"`
	UnitPrice       string `json:"unitPrice"`
	DiscountPct     string `json:"discountPct,omitempty"`
	RevenueAccount  string `json:"revenueAccount"`
	TaxCode         string `json:"taxCode"`
	PriceExplainRef string `json:"priceExplainRef,omitempty"`
}

// slipDTO is one posted packing slip with its order, posting date and charges.
type slipDTO struct {
	ID           string        `json:"id"`
	OrderID      string        `json:"orderId"`
	Date         string        `json:"date"`
	ChargeAmount string        `json:"chargeAmount,omitempty"`
	Lines        []slipLineDTO `json:"lines"`
}

// shipmentProposalRequest builds invoices from posted packing slips of one
// invoice account, currency and terms: one invoice per order, or one
// consolidated invoice with consolidate (FR-29.1, FR-29.2, BR-29.5).
type shipmentProposalRequest struct {
	LegalEntityID    string    `json:"legalEntityId"`
	CustomerID       string    `json:"customerId"`
	InvoiceAccountID string    `json:"invoiceAccountId"`
	Currency         string    `json:"currency"`
	InvoiceDate      string    `json:"invoiceDate"`
	PaymentTermDays  int       `json:"paymentTermDays"`
	Consolidate      bool      `json:"consolidate"`
	Slips            []slipDTO `json:"slips"`
}

// proposalBatchDTO lists the invoices one POST /proposals request created.
type proposalBatchDTO struct {
	Items []invoiceDTO `json:"items"`
}

func batchDTO(invs []store.Invoice) proposalBatchDTO {
	out := proposalBatchDTO{Items: make([]invoiceDTO, 0, len(invs))}
	for _, inv := range invs {
		out.Items = append(out.Items, toDTO(inv))
	}
	return out
}

// createShipmentProposals builds invoice proposals from posted packing slips
// and stores them in one transaction. The Idempotency-Key names the whole
// request: a replay returns every invoice of the first request (200), a reused
// key with another body is 422. Tax is quoted per invoice; an outage stores
// nothing (BIL-0001). A slip line already on an invoice fails the request with
// 409 BIL-0104 (BR-29.5).
func (a *API) createShipmentProposals(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get(idempotencyKey)
	if invalidIdempotencyKey(key) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", idempotencyKeyDetail))
		return
	}
	var req shipmentProposalRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	slips, invoiceDate, fields := validateSlips(req)
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if p, _ := tenant.FromContext(r.Context()); !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the caller is scoped to another legal entity"))
		return
	}
	if err := a.refuseSelfBilled(r.Context(), req.LegalEntityID, req.CustomerID, invoiceDate); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	raw, _ := json.Marshal(req)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	if batch, existing, found, err := a.store.FindProposalBatch(r.Context(), key); err != nil {
		httpx.Fail(w, r, err)
		return
	} else if found {
		if batch.RequestHash != hash {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "idempotency_key_reuse", "Idempotency-Key was already used with a different request body"))
			return
		}
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, batchDTO(existing))
		return
	}
	plans, err := domain.PlanProposals(slips, req.Consolidate, invoiceDate, a.maxLines)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	batch := store.ProposalBatch{ID: ids.New(), LegalEntityID: req.LegalEntityID, IdempotencyKey: key, RequestHash: hash}
	exp := money.MinorUnits(req.Currency)
	invs := make([]store.Invoice, 0, len(plans))
	for i, plan := range plans {
		taxes, err := a.tax.Quote(r.Context(), req.Currency, plan.Lines)
		if err != nil {
			httpx.Fail(w, r, taxProblem(err))
			return
		}
		nets, totals, err := domain.Compute(req.Currency, plan.Lines, taxes, plan.Charges)
		if err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
		inv := store.Invoice{ID: ids.New(), LegalEntityID: req.LegalEntityID, CustomerID: req.CustomerID, InvoiceAccountID: req.InvoiceAccountID,
			Type: plan.Type, Status: domain.StatusProposal, Currency: req.Currency, InvoiceDate: invoiceDate,
			DueDate: invoiceDate.AddDate(0, 0, req.PaymentTermDays), PaymentTermDays: req.PaymentTermDays, Totals: totals,
			IdempotencyKey: batch.ID + "/" + strconv.Itoa(i+1), RequestHash: hash}
		for j := range plan.Lines {
			inv.Lines = append(inv.Lines, store.Line{Line: plan.Lines[j], Net: nets[j], Tax: taxes[j].RoundBank(exp), Source: plan.Sources[j]})
		}
		invs = append(invs, inv)
	}
	created, err := a.store.CreateShipmentProposals(r.Context(), batch, invs)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.shipmentProposals.Add(int64(len(created)))
	httpx.JSON(w, http.StatusCreated, batchDTO(created))
}

// validateSlips checks the header and every slip and converts the slips to
// domain slips. Field problems are returned in fields under their JSON path.
func validateSlips(req shipmentProposalRequest) ([]domain.Slip, time.Time, map[string]string) {
	fields := map[string]string{}
	for name, v := range map[string]string{"legalEntityId": req.LegalEntityID, "customerId": req.CustomerID, "invoiceAccountId": req.InvoiceAccountID} {
		if !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	invoiceDate, err := time.Parse(dateLayout, req.InvoiceDate)
	if err != nil {
		fields["invoiceDate"] = "must be a YYYY-MM-DD date"
	}
	if req.PaymentTermDays < 0 || req.PaymentTermDays > 365 {
		fields["paymentTermDays"] = "must be 0 to 365"
	}
	if n := len(req.Slips); n == 0 || n > maxSlips {
		fields["slips"] = fmt.Sprintf("must hold 1 to %d packing slips", maxSlips)
	}
	total := 0
	for _, s := range req.Slips {
		total += len(s.Lines)
	}
	if total > maxLinesCeiling {
		fields["slips"] = fmt.Sprintf("must hold at most %d packing slip lines in all", maxLinesCeiling)
		return nil, invoiceDate, fields
	}
	slips := make([]domain.Slip, 0, len(req.Slips))
	for i, s := range req.Slips {
		at := func(name string) string { return fmt.Sprintf("slips[%d].%s", i, name) }
		ds := domain.Slip{ID: s.ID, OrderID: s.OrderID, Charges: decimal.Zero}
		if !ids.Valid(s.ID) {
			fields[at("id")] = "must be a UUID"
		}
		if !ids.Valid(s.OrderID) {
			fields[at("orderId")] = "must be a UUID"
		}
		if ds.Date, err = time.Parse(dateLayout, s.Date); err != nil {
			fields[at("date")] = "must be the packing slip's posting date as YYYY-MM-DD"
		}
		if s.ChargeAmount != "" {
			if !decimalText.MatchString(s.ChargeAmount) {
				fields[at("chargeAmount")] = "must be a non-negative decimal with at most 6 fraction digits"
			} else {
				ds.Charges = decimal.RequireFromString(s.ChargeAmount)
			}
		}
		if len(s.Lines) == 0 {
			fields[at("lines")] = "must hold at least one packing slip line"
		}
		for j, l := range s.Lines {
			prefix := fmt.Sprintf("slips[%d].lines[%d]", i, j)
			for name, v := range map[string]string{"id": l.ID, "orderLineId": l.OrderLineID, "productId": l.ProductID} {
				if !ids.Valid(v) {
					fields[prefix+"."+name] = "must be a UUID"
				}
			}
			if len(l.PriceExplainRef) > maxExplainRef {
				fields[prefix+".priceExplainRef"] = fmt.Sprintf("must be at most %d characters", maxExplainRef)
			}
			line := toLineAt(prefix, j+1, lineDTO{Description: l.Description, Qty: l.Qty, UnitPrice: l.UnitPrice,
				DiscountPct: l.DiscountPct, RevenueAccount: l.RevenueAccount, TaxCode: l.TaxCode}, fields)
			ds.Lines = append(ds.Lines, domain.SlipLine{Line: line, Source: domain.Source{PackingSlipLineID: l.ID,
				OrderLineID: l.OrderLineID, ProductID: l.ProductID, PriceExplainRef: l.PriceExplainRef}})
		}
		slips = append(slips, ds)
	}
	return slips, invoiceDate, fields
}

// traceLineDTO is one invoice line with the documents behind it (FR-29.12).
type traceLineDTO struct {
	LineNo            int    `json:"lineNo"`
	Description       string `json:"description"`
	Qty               string `json:"qty"`
	Net               string `json:"net"`
	RevenueAccount    string `json:"revenueAccount"`
	OriginalLineNo    int    `json:"originalLineNo,omitempty"`
	PackingSlipID     string `json:"packingSlipId,omitempty"`
	PackingSlipLineID string `json:"packingSlipLineId,omitempty"`
	OrderID           string `json:"orderId,omitempty"`
	OrderLineID       string `json:"orderLineId,omitempty"`
	ProductID         string `json:"productId,omitempty"`
	PriceExplainRef   string `json:"priceExplainRef,omitempty"`
}

// traceSlipDTO is one packing slip an invoice bills, with its order.
type traceSlipDTO struct {
	ID      string `json:"id"`
	Date    string `json:"date"`
	OrderID string `json:"orderId"`
}

// traceDTO walks an invoice back to its packing slips, order lines and price
// explanations (FR-29.12). A credit note's lines resolve through the original
// invoice's lines.
type traceDTO struct {
	Invoice      summaryDTO     `json:"invoice"`
	Original     *summaryDTO    `json:"original,omitempty"`
	PackingSlips []traceSlipDTO `json:"packingSlips"`
	Lines        []traceLineDTO `json:"lines"`
}

// invoiceTrace serves GET /invoices/{id}/trace.
func (a *API) invoiceTrace(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "invoice not found"))
		return
	}
	inv, err := a.store.GetInvoice(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var original *store.Invoice
	if inv.OriginalInvoiceID != "" {
		orig, err := a.store.GetInvoice(r.Context(), inv.OriginalInvoiceID)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		original = &orig
	}
	httpx.JSON(w, http.StatusOK, toTrace(inv, original))
}

// toTrace builds the trace; packing slips are listed once, in line order.
func toTrace(inv store.Invoice, original *store.Invoice) traceDTO {
	exp := money.MinorUnits(inv.Currency)
	out := traceDTO{Invoice: toSummary(inv), PackingSlips: []traceSlipDTO{}, Lines: []traceLineDTO{}}
	sources := map[int]domain.Source{}
	if original != nil {
		s := toSummary(*original)
		out.Original = &s
		for _, l := range original.Lines {
			sources[l.No] = l.Source
		}
	}
	seen := map[string]bool{}
	for _, l := range inv.Lines {
		src := l.Source
		if original != nil {
			src = sources[l.OriginalLineNo]
		}
		out.Lines = append(out.Lines, traceLineDTO{LineNo: l.No, Description: l.Description, Qty: l.Qty.String(), Net: fixed(l.Net, exp),
			RevenueAccount: l.RevenueAccount, OriginalLineNo: l.OriginalLineNo, PackingSlipID: src.PackingSlipID,
			PackingSlipLineID: src.PackingSlipLineID, OrderID: src.OrderID, OrderLineID: src.OrderLineID, ProductID: src.ProductID,
			PriceExplainRef: src.PriceExplainRef})
		if src.PackingSlipID != "" && !seen[src.PackingSlipID] {
			seen[src.PackingSlipID] = true
			out.PackingSlips = append(out.PackingSlips, traceSlipDTO{ID: src.PackingSlipID, Date: src.PackingSlipDate.Format(dateLayout), OrderID: src.OrderID})
		}
	}
	return out
}
