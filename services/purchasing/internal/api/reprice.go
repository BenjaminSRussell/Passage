package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// Subsequent price change (FR-17.22, BR-17.18, AC-17.16). POST
// /purchase-orders/{id}:reprice previews or applies the run on one PO; POST
// /purchase-orders:reprice runs it over a page of POs. Preview is read-only and
// needs procurement.purchase-order.inquire; apply needs procurement.purchase-order.confirm (PRD 3.17.17)
// and an Idempotency-Key, and on one PO also If-Match.

// repriceAction is the PO custom method name.
const repriceAction = "reprice"

// Run page sizes: a run plans every PO it visits, so pages are smaller than lists.
const (
	defaultRepriceRunLimit = 25
	maxRepriceRunLimit     = 100
)

// repriceBody is the single-PO request. apply=false (the default) previews.
type repriceBody struct {
	Apply bool `json:"apply"`
}

// repriceRunBody is the request of the run over many POs.
type repriceRunBody struct {
	Apply            bool   `json:"apply"`
	LegalEntityID    string `json:"legalEntityId,omitempty"`
	VendorID         string `json:"vendorId,omitempty"`
	TradeAgreementID string `json:"tradeAgreementId,omitempty"`
	Limit            int    `json:"limit,omitempty"`
	Cursor           string `json:"cursor,omitempty"`
}

// repriceChangeDTO is one line's move on the wire. Amounts are for qty.
type repriceChangeDTO struct {
	LineNo         int    `json:"lineNo"`
	Qty            string `json:"qty"`
	OldPrice       string `json:"oldPrice"`
	OldPriceUnit   string `json:"oldPriceUnit"`
	NewPrice       string `json:"newPrice"`
	NewPriceUnit   string `json:"newPriceUnit"`
	OldAmount      string `json:"oldAmount"`
	NewAmount      string `json:"newAmount"`
	Delta          string `json:"delta"`
	OldPriceSource string `json:"oldPriceSource"`
	NewPriceSource string `json:"newPriceSource"`
}

// repriceSkipDTO is a line the run left alone and why.
type repriceSkipDTO struct {
	LineNo int    `json:"lineNo"`
	Reason string `json:"reason"`
}

// repriceFailure is why the run could not plan or apply one PO.
type repriceFailure struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// repriceResult is the preview or the applied run of one PO. Changes move;
// Invoiced lines keep the old price and are handed to payables (BR-17.18).
// VersionNo is the PO version: the current one on a preview, the one written
// on apply. Error is set only in a run over many POs.
type repriceResult struct {
	PurchaseOrderID    string             `json:"purchaseOrderId"`
	Number             string             `json:"number"`
	Currency           string             `json:"currency"`
	Applied            bool               `json:"applied"`
	Replayed           bool               `json:"replayed,omitempty"`
	VersionNo          int                `json:"versionNo"`
	ReapprovalRequired bool               `json:"reapprovalRequired"`
	PricingDate        domain.Day         `json:"pricingDate"`
	TotalDelta         string             `json:"totalDelta"`
	Changes            []repriceChangeDTO `json:"changes"`
	Invoiced           []repriceChangeDTO `json:"invoiced"`
	Skipped            []repriceSkipDTO   `json:"skipped"`
	Error              *repriceFailure    `json:"error,omitempty"`
}

// repriceRunResult is the run over a page of POs. Only POs with something to
// move or hand over are listed; Visited counts every PO the page planned.
type repriceRunResult struct {
	Applied    bool            `json:"applied"`
	Visited    int             `json:"visited"`
	Items      []repriceResult `json:"items"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

// repriceOrder handles POST /purchase-orders/{id}:reprice.
func (a *API) repriceOrder(w http.ResponseWriter, r *http.Request, p tenant.Principal, id string) {
	var body repriceBody
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &body); err != nil {
			fail(w, r, err)
			return
		}
	}
	if err := requireRepriceDuty(p, body.Apply); err != nil {
		fail(w, r, err)
		return
	}
	var key string
	var want int
	if body.Apply {
		var err error
		if key, err = idempotencyKey(r); err != nil {
			fail(w, r, err)
			return
		}
		if want, err = ifMatch(r); err != nil {
			fail(w, r, err)
			return
		}
	}
	cur, err := a.store.GetPurchaseOrder(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	if body.Apply {
		if err := inScope(p, cur.LegalEntityID); err != nil {
			fail(w, r, err)
			return
		}
	}
	// On apply the store checks the state and If-Match again under the row
	// lock, after it has looked the Idempotency-Key up, so a replay is answered
	// rather than refused as stale.
	if err := domain.CheckRepriceable(cur.ApprovalStatus); err != nil && (!body.Apply || cur.RowVersion == want) {
		fail(w, r, err)
		return
	}
	plan, err := a.planReprice(r.Context(), cur)
	if err != nil {
		fail(w, r, err)
		return
	}
	res := toRepriceResult(cur, plan)
	if !body.Apply {
		w.Header().Set("ETag", etag(cur.RowVersion))
		httpx.JSON(w, http.StatusOK, res)
		return
	}
	res, rowVersion, err := a.applyReprice(r.Context(), cur, want, plan, store.Idem{Key: key,
		Hash: hashBody("purchase-order.reprice", map[string]any{"id": id, "ifMatch": want})})
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(rowVersion))
	httpx.JSON(w, http.StatusOK, res)
}

// repriceRun handles POST /purchase-orders:reprice: one page of CONFIRMED POs
// with open trade agreement lines, filtered by legal entity, vendor or the
// trade agreement row their lines were priced from. Each PO is planned and, on
// apply, written in its own transaction at the row version this run read, so a
// failing PO is reported in its item and does not stop the others. A retry
// with the same Idempotency-Key does not write a PO twice.
func (a *API) repriceRun(w http.ResponseWriter, r *http.Request) {
	p, err := principal(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	var body repriceRunBody
	if r.ContentLength != 0 {
		if err := httpx.Decode(r, &body); err != nil {
			fail(w, r, err)
			return
		}
	}
	if err := requireRepriceDuty(p, body.Apply); err != nil {
		fail(w, r, err)
		return
	}
	for field, v := range map[string]string{"legalEntityId": body.LegalEntityID, "vendorId": body.VendorID,
		"tradeAgreementId": body.TradeAgreementID, "cursor": body.Cursor} {
		if err := uuidField(field, v, false); err != nil {
			fail(w, r, err)
			return
		}
	}
	if body.Limit < 0 || body.Limit > maxRepriceRunLimit {
		fail(w, r, invalid("limit", fmt.Sprintf("must be from 1 to %d", maxRepriceRunLimit)))
		return
	}
	if body.Limit == 0 {
		body.Limit = defaultRepriceRunLimit
	}
	var key string
	if body.Apply {
		if key, err = idempotencyKey(r); err != nil {
			fail(w, r, err)
			return
		}
	}
	page, err := a.store.RepriceCandidates(r.Context(), store.RepriceFilter{LegalEntityID: body.LegalEntityID,
		VendorID: body.VendorID, TradeAgreementID: body.TradeAgreementID}, body.Cursor, body.Limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := repriceRunResult{Applied: body.Apply, Visited: len(page.Items), Items: []repriceResult{}, NextCursor: page.Next}
	for _, id := range page.Items {
		res, include, err := a.repriceOne(r.Context(), p, id, body.Apply, key)
		if err != nil {
			// Infrastructure failures stop the run; rule failures are per PO.
			fail(w, r, err)
			return
		}
		if include {
			out.Items = append(out.Items, res)
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

// repriceOne plans one PO of a run and applies it when apply is set. It
// reports whether the PO belongs in the result (it has something to move or
// hand over, or failed). A problem or rule violation is the PO's own failure;
// any other error is returned.
func (a *API) repriceOne(ctx context.Context, p tenant.Principal, id string, apply bool, key string) (repriceResult, bool, error) {
	cur, err := a.store.GetPurchaseOrder(ctx, id)
	if err != nil {
		return a.repriceFailed(store.PurchaseOrder{ID: id}, err)
	}
	if err := domain.CheckRepriceable(cur.ApprovalStatus); err != nil {
		return a.repriceFailed(cur, err) // changed since the candidate query
	}
	plan, err := a.planReprice(ctx, cur)
	if err != nil {
		return a.repriceFailed(cur, err)
	}
	res := toRepriceResult(cur, plan)
	if len(plan.Changes) == 0 && len(plan.Invoiced) == 0 {
		return res, false, nil
	}
	if !apply {
		return res, true, nil
	}
	if err := inScope(p, cur.LegalEntityID); err != nil {
		return a.repriceFailed(cur, err)
	}
	idem := store.Idem{Key: key + "/" + id, Hash: hashBody("purchase-order.reprice-run", map[string]any{"id": id})}
	applied, _, err := a.applyReprice(ctx, cur, cur.RowVersion, plan, idem)
	if err != nil {
		return a.repriceFailed(cur, err)
	}
	return applied, true, nil
}

// repriceFailed turns a problem or rule violation into a failed item and
// passes any other error up.
func (a *API) repriceFailed(cur store.PurchaseOrder, err error) (repriceResult, bool, error) {
	res := repriceResult{PurchaseOrderID: cur.ID, Number: cur.Number, Currency: cur.Currency, TotalDelta: "0",
		Changes: []repriceChangeDTO{}, Invoiced: []repriceChangeDTO{}, Skipped: []repriceSkipDTO{}}
	var de *domain.Error
	var pr *httpx.Problem
	switch {
	case errors.As(err, &de):
		res.Error = &repriceFailure{Code: de.Code, Detail: de.Detail}
	case errors.As(err, &pr):
		res.Error = &repriceFailure{Code: pr.Code, Detail: pr.Detail}
	default:
		return repriceResult{}, false, err
	}
	return res, true, nil
}

// applyReprice writes plan on cur under If-Match ifMatch and returns the
// result and the PO's row version afterwards. An empty plan writes nothing but
// still claims the key and checks the version, so a retried apply is answered
// as a replay.
func (a *API) applyReprice(ctx context.Context, cur store.PurchaseOrder, ifMatch int, plan domain.RepricePlan, idem store.Idem) (repriceResult, int, error) {
	next, err := repriceDocument(cur, plan)
	if err != nil {
		return repriceResult{}, 0, err
	}
	out, err := a.store.RepricePurchaseOrder(ctx, cur.ID, ifMatch, next, plan, idem)
	if err != nil {
		return repriceResult{}, 0, err
	}
	if out.Replayed {
		// The key already applied a run to this PO; report the PO as it is
		// now and what a run would still move.
		replan, err := a.planReprice(ctx, out.Order)
		if err != nil {
			return repriceResult{}, 0, err
		}
		res := toRepriceResult(out.Order, replan)
		res.Applied, res.Replayed = true, true
		return res, out.Order.RowVersion, nil
	}
	res := toRepriceResult(cur, plan)
	if len(plan.Changes) == 0 && len(plan.Invoiced) == 0 {
		return res, out.Order.RowVersion, nil
	}
	a.repricesApplied.Add(1)
	res.Applied, res.VersionNo, res.ReapprovalRequired = true, out.VersionNo, out.ReapprovalRequired
	return res, out.Order.RowVersion, nil
}

// requireRepriceDuty checks procurement.purchase-order.inquire for a preview and
// procurement.purchase-order.confirm for apply (PRD 3.17.17).
func requireRepriceDuty(p tenant.Principal, apply bool) error {
	duty := dutyPurchaseOrderInquire
	if apply {
		duty = dutyPurchaseOrderConfirm
	}
	if !p.HasDuty(duty) {
		return httpx.NewProblem(http.StatusForbidden, codeScope, "missing duty "+duty)
	}
	return nil
}

// pricingDate is the date a PO's lines are priced on: the order date, its
// creation day in UTC (BR-17.3; delivery-date pricing is a later slice).
func pricingDate(po store.PurchaseOrder) domain.Day {
	t := po.CreatedAt.UTC()
	return domain.Day{Time: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)}
}

// planReprice resolves, for each line the run may move, the trade agreement
// row that wins now for the PO's vendor and pricing date, with the same rules
// as PO creation (FR-17.6), and hands the quotes to the domain plan.
func (a *API) planReprice(ctx context.Context, cur store.PurchaseOrder) (domain.RepricePlan, error) {
	lines := repriceLines(cur)
	date := pricingDate(cur)
	quotes := map[int]domain.RepriceQuote{}
	for _, l := range lines {
		if domain.RepriceSkipReason(l) != "" {
			continue
		}
		res, err := a.resolve(ctx, domain.Query{
			Relation:  domain.RelationPrice,
			VendorID:  cur.VendorID,
			ProductID: l.ProductID,
			Qty:       l.Input.Qty,
			Currency:  cur.Currency,
			Unit:      l.Unit,
			Date:      date,
		}, nil, nil)
		if err != nil {
			return domain.RepricePlan{}, err
		}
		if res.Winner == nil {
			continue
		}
		quotes[l.LineNo] = domain.RepriceQuote{Price: res.Winner.Price, PriceUnit: res.Winner.PriceUnit,
			Source: domain.PriceSourceTradePrefix + res.Winner.ID}
	}
	return domain.PlanReprice(lines, func(l domain.RepriceLine) (domain.RepriceQuote, bool) {
		q, ok := quotes[l.LineNo]
		return q, ok
	}, money.MinorUnits(cur.Currency))
}

// repriceLines maps the live lines of a PO to the domain's view.
func repriceLines(cur store.PurchaseOrder) []domain.RepriceLine {
	out := make([]domain.RepriceLine, 0, len(cur.Lines))
	for _, l := range cur.Lines {
		if l.Status == domain.POLineCancelled {
			continue
		}
		out = append(out, domain.RepriceLine{LineNo: l.LineNo, ProductID: l.ProductID, Unit: l.Unit,
			PriceSource: l.PriceSource, InvoicedQty: l.InvoicedQty, AgreementDrawn: l.AgreementLineID != "",
			HandedOver: l.InvoicedRepriceSource, Input: repriceInput(l)})
	}
	return out
}

func repriceInput(l store.PurchaseOrderLine) domain.LineInput {
	return domain.LineInput{Qty: l.Qty, UnitPrice: l.UnitPrice, PriceUnit: l.PriceUnit, DiscountPct: l.DiscountPct, Tax: l.TaxAmount}
}

// repriceDocument is the next version of the PO: its live lines and charges,
// each moved line at its new price and source, priced again so line amounts,
// percentage charges and totals stay exact (BR-17.5). Cancelled lines stay
// out; the edit keeps them as they are.
func repriceDocument(cur store.PurchaseOrder, plan domain.RepricePlan) (store.PurchaseOrder, error) {
	moved := map[int]domain.RepriceChange{}
	for _, c := range plan.Changes {
		moved[c.LineNo] = c
	}
	next := cur
	next.Lines = make([]store.PurchaseOrderLine, 0, len(cur.Lines))
	inputs := make([]domain.LineInput, 0, len(cur.Lines))
	for _, l := range cur.Lines {
		if l.Status == domain.POLineCancelled {
			continue
		}
		if c, ok := moved[l.LineNo]; ok {
			l.UnitPrice, l.PriceUnit, l.PriceSource = c.NewPrice, c.NewPriceUnit, c.NewSource
		}
		next.Lines = append(next.Lines, l)
		inputs = append(inputs, repriceInput(l))
	}
	charges, kept := store.LiveCharges(cur.Charges, next.Lines)
	next.Charges = nil
	if err := priceDocument(&next, inputs, charges); err != nil {
		return store.PurchaseOrder{}, err
	}
	copyChargeSides(&next, kept)
	return next, nil
}

func toRepriceChanges(cs []domain.RepriceChange) []repriceChangeDTO {
	out := make([]repriceChangeDTO, 0, len(cs))
	for _, c := range cs {
		out = append(out, repriceChangeDTO{LineNo: c.LineNo, Qty: c.Qty.String(), OldPrice: c.OldPrice.String(),
			OldPriceUnit: c.OldPriceUnit.String(), NewPrice: c.NewPrice.String(), NewPriceUnit: c.NewPriceUnit.String(),
			OldAmount: c.OldAmount.String(), NewAmount: c.NewAmount.String(), Delta: c.Delta.String(),
			OldPriceSource: c.OldSource, NewPriceSource: c.NewSource})
	}
	return out
}

// toRepriceResult renders a plan on cur as a preview.
func toRepriceResult(cur store.PurchaseOrder, plan domain.RepricePlan) repriceResult {
	res := repriceResult{PurchaseOrderID: cur.ID, Number: cur.Number, Currency: cur.Currency, VersionNo: cur.VersionNo,
		PricingDate: pricingDate(cur), TotalDelta: plan.TotalDelta.String(), Changes: toRepriceChanges(plan.Changes),
		Invoiced: toRepriceChanges(plan.Invoiced), Skipped: make([]repriceSkipDTO, 0, len(plan.Skipped))}
	for _, s := range plan.Skipped {
		res.Skipped = append(res.Skipped, repriceSkipDTO{LineNo: s.LineNo, Reason: s.Reason})
	}
	return res
}
