package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/productunits"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// The postings consumer (#480) books what receiving, sales and fulfillment
// post as inventory transactions through the transactions:apply store path:
//
//   - receiving's receipt.posted: a receipt line at RECEIVED (origin
//     RECEIPT_LINE), valued at qty x unit_price in the receipt currency;
//   - receiving's receipt.corrected: the quantity delta of a CORRECTION, or
//     what is left of the line on a CANCELLATION, as a compensating receipt
//     (RECEIVED) or issue (DEDUCTED) of the booked line, at its booked cost;
//   - sales' orderprogress.recorded of kind DELIVERED (the packing slip
//     fulfillment reported): an issue at DEDUCTED (origin SALES_LINE), valued
//     at the row's running average per inventory unit (store.IssueBasis);
//   - fulfillment's packingslip.cancelled: the exact mirror of each booked
//     delivery line of the slip, a receipt at RECEIVED of its quantity, cases
//     and value.
//
// A catch-weight line's inventory unit is fixed by the convention (one for
// receiving, sales and inventory): the receipt line unit is the weight unit,
// so qty is the weight and cw_qty the cases, and a sales line's delivered
// weight is reported_weight while its quantity is cases. A plain line's
// document quantity is converted into the product's inventory (base) unit by
// the product service (productunits); without a converter, or when product
// knows no conversion, the line is parked under INV-PST-002 or INV-PST-001.
//
// Lines are booked under the financial dimensions site and warehouse, plus
// inventory_status for a receipt not received as AVAILABLE, so a delivery
// draws from what was received; location, batch and serial stay with
// warehouse management. A compensation reads the booking it offsets back
// (store.OriginBookings) and takes product, dimensions, unit factor, currency
// and cost from it, never from the event; it is linked to it
// (corrects_transaction_id) and refused when it is not found (INV-PST-003) or
// does not match it (INV-PST-004). Each line is booked under its own origin
// and idempotency key, so a retry or a replay books only what is missing. A
// line that cannot be booked (no site, serials, a rule of inventory such as
// stock that would go negative) fails the event after the other lines: a
// validation or business-rule failure is permanent and parks the event on the
// dead-letter topic, anything else is retried with back-off first.
const (
	// PostingsConsumerName is the consumer (and Kafka group) and the service
	// principal it runs as, service:inventory-postings.
	PostingsConsumerName = "inventory-postings"
	// ReceiptPostedType and ReceiptCorrectedType are receiving's
	// receipt.posted.v1 and receipt.corrected.v1, on ReceiptTopic.
	ReceiptPostedType    = "dev.passage.receiving.receipt.posted.v1"
	ReceiptCorrectedType = "dev.passage.receiving.receipt.corrected.v1"
	ReceiptTopic         = "passage.receiving.receipt"
	ReceiptSource        = "/passage/receiving/receiving"
	// OrderProgressRecordedType is sales' orderprogress.recorded.v1, on OrderProgressTopic.
	OrderProgressRecordedType = "dev.passage.sales.orderprogress.recorded.v1"
	OrderProgressTopic        = "passage.sales.orderprogress"
	SalesSource               = "sales"
	// PackingSlipCancelledType is fulfillment's packingslip.cancelled.v1, on PackingSlipTopic.
	PackingSlipCancelledType = "dev.passage.fulfillment.packingslip.cancelled.v1"
	PackingSlipTopic         = "passage.fulfillment.packingslip"
	FulfillmentSource        = "fulfillment"

	// Reason codes of lines the consumer parks or retries.
	// CodePostingNoConversion: product knows the line's unit has no
	// conversion into the product's inventory unit (or the product).
	CodePostingNoConversion = "INV-PST-001"
	// CodePostingNoConverter: a plain line needs a unit conversion and the
	// consumer has none (PRODUCT_URL unset).
	CodePostingNoConverter = "INV-PST-002"
	// CodePostingOriginalMissing: a correction or cancellation names a line
	// the consumer has not booked.
	CodePostingOriginalMissing = "INV-PST-003"
	// CodePostingMismatch: a correction or cancellation does not match the
	// line as booked (another product, a cancellation of another quantity
	// than the booked net, cases and weight moving apart).
	CodePostingMismatch = "INV-PST-004"

	sourceReceiptLine   = "RECEIPT_LINE"
	sourceSalesLine     = "SALES_LINE"
	inventoryAvailable  = "AVAILABLE"
	progressDelivered   = "DELIVERED"
	correctionKind      = "CORRECTION"
	cancellationKind    = "CANCELLATION"
	postingsKeyPrefix   = "inventory-postings:"
	postingsDateLayout  = "2006-01-02"
	maxPostingReference = 60
	unitFactorScale     = 6
)

// PostingsStore is what the postings consumer needs of the store.
type PostingsStore interface {
	CreateTransaction(ctx context.Context, in store.CreateTransactionInput) (store.StatusChange, bool, error)
	IssueBasis(ctx context.Context, legalEntityID, productID string, dims domain.Dimensions) (string, decimal.Decimal, error)
	OriginBookings(ctx context.Context, legalEntityID, sourceType string, sourceIDs []string) (map[string]store.TransactionRow, error)
}

// UnitConverter converts a document quantity into the product's inventory
// unit and names that unit (productunits.Client). An error wrapping
// productunits.ErrNoConversion is final; any other may pass on a retry.
type UnitConverter interface {
	ToInventoryUnit(ctx context.Context, legalEntityID, productID, unit string, qty decimal.Decimal) (decimal.Decimal, string, error)
}

type receiptPostedData struct {
	ReceiptID     string `json:"receipt_id"`
	ReceiptNumber string `json:"receipt_number"`
	LegalEntityID string `json:"legal_entity_id"`
	PostingDate   string `json:"posting_date"`
	Currency      string `json:"currency"`
	Lines         []struct {
		LineNo          int      `json:"line_no"`
		ProductID       string   `json:"product_id"`
		Unit            string   `json:"unit"`
		Qty             string   `json:"qty"`
		Serials         []string `json:"serials"`
		InventoryStatus string   `json:"inventory_status"`
		Amount          string   `json:"amount"`
		UnitPrice       string   `json:"unit_price"`
		SiteID          string   `json:"site_id"`
		WarehouseID     string   `json:"warehouse_id"`
		CWQty           int      `json:"cw_qty"`
	} `json:"lines"`
}

type receiptCorrectedData struct {
	ReceiptID     string `json:"receipt_id"`
	ReceiptNumber string `json:"receipt_number"`
	LegalEntityID string `json:"legal_entity_id"`
	PostingDate   string `json:"posting_date"`
	Kind          string `json:"kind"`
	CorrectionSeq int    `json:"correction_seq"`
	Lines         []struct {
		LineNo     int    `json:"line_no"`
		ProductID  string `json:"product_id"`
		QtyDelta   string `json:"qty_delta"`
		CWQtyDelta int    `json:"cw_qty_delta"`
	} `json:"lines"`
}

type orderProgressData struct {
	ProgressID    string `json:"progress_id"`
	OrderID       string `json:"order_id"`
	LegalEntityID string `json:"legal_entity_id"`
	Kind          string `json:"kind"`
	SourceRef     string `json:"source_ref"`
	Currency      string `json:"currency"`
	Lines         []struct {
		LineID         string `json:"line_id"`
		LineNo         int    `json:"line_no"`
		Quantity       string `json:"quantity"`
		ProductID      string `json:"product_id"`
		Unit           string `json:"unit"`
		SiteID         string `json:"site_id"`
		WarehouseID    string `json:"warehouse_id"`
		ReportedWeight string `json:"reported_weight"`
	} `json:"lines"`
}

type packingSlipCancelledData struct {
	PackingSlipID     string `json:"packing_slip_id"`
	PackingSlipNumber string `json:"packing_slip_number"`
	ReversalID        string `json:"reversal_id"`
	OrderID           string `json:"order_id"`
	LegalEntityID     string `json:"legal_entity_id"`
	PostingDate       string `json:"posting_date"`
	Lines             []struct {
		OrderLineID string `json:"order_line_id"`
		Qty         string `json:"qty"`
	} `json:"lines"`
}

// PostingsConsumerHandlers handles receipt.posted, receipt.corrected,
// orderprogress.recorded and packingslip.cancelled. conv converts plain lines
// into the inventory unit; nil parks them. Register it with a consumer named
// PostingsConsumerName with Idempotent set: each booking commits on its own
// under its own key, and the inbox records the event once every line is booked.
func PostingsConsumerHandlers(s PostingsStore, conv UnitConverter, neg domain.NegativePolicy) map[string]eventing.Handler {
	return map[string]eventing.Handler{
		ReceiptPostedType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			return bookReceipt(ctx, s, conv, evt)
		}),
		ReceiptCorrectedType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			return bookReceiptCorrection(ctx, s, neg, evt)
		}),
		OrderProgressRecordedType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			return bookDelivery(ctx, s, conv, neg, evt)
		}),
		PackingSlipCancelledType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			return bookSlipCancellation(ctx, s, evt)
		}),
	}
}

// postingsScope narrows the consumer's principal to the legal entity of the
// document, so the bookings write only under that legal entity's RLS scope.
func postingsScope(ctx context.Context, evt events.Event, wantSource, legalEntityID string) (context.Context, error) {
	actor, ok := tenant.FromContext(ctx)
	if !ok || !actor.HasDuty(dutyTransactionApply) {
		return nil, eventing.Permanent(errors.New("postings consumer has no principal with " + dutyTransactionApply))
	}
	if evt.Source != wantSource {
		return nil, eventing.Permanent(fmt.Errorf("%s from source %q, want %q", evt.Type, evt.Source, wantSource))
	}
	if !ids.Valid(legalEntityID) {
		return nil, eventing.Permanent(fmt.Errorf("%s has no legal_entity_id", evt.Type))
	}
	scoped, err := tenant.ServicePrincipal(PostingsConsumerName, actor.TenantID, legalEntityID)
	if err != nil {
		return nil, eventing.Permanent(err)
	}
	return tenant.With(ctx, scoped), nil
}

// postingSourceID is the origin id of one booked line: receipt lines, the
// lines of one delivery and their compensations have no UUID of their own,
// so it is derived from the document and the line (a name-based UUID,
// version 5 layout).
func postingSourceID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	sum[6] = sum[6]&0x0f | 0x50
	sum[8] = sum[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// receiptLineSourceID is the origin of a booked receipt line.
func receiptLineSourceID(tenantID, receiptID string, lineNo int) string {
	return postingSourceID(tenantID, "receipt-line", strings.ToLower(receiptID), strconv.Itoa(lineNo))
}

// receiptCorrectionSourceID is the origin of the booking of correction seq of a receipt line.
func receiptCorrectionSourceID(tenantID, receiptID string, seq, lineNo int) string {
	return postingSourceID(tenantID, "receipt-line-correction", strings.ToLower(receiptID), strconv.Itoa(seq), strconv.Itoa(lineNo))
}

// deliverySourceID is the origin of a booked delivery line: the order, the
// packing slip number the delivery was reported under (sales applies one
// report of a source reference per order) and the order line, so the slip's
// cancellation finds it.
func deliverySourceID(tenantID, orderID, sourceRef, lineID string) string {
	return postingSourceID(tenantID, "sales-delivery", strings.ToLower(orderID), strings.TrimSpace(sourceRef), strings.ToLower(lineID))
}

func postingReference(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxPostingReference {
		return string(r[:maxPostingReference])
	}
	return s
}

func postingUnit(u string) string { return strings.ToUpper(strings.TrimSpace(u)) }

// postingDate is the document's posting date, or the event time without one.
func postingDate(raw string, evt events.Event) (time.Time, error) {
	if raw == "" {
		return evt.Time.UTC(), nil
	}
	return time.Parse(postingsDateLayout, raw)
}

// lineFailures collects the lines of one event that could not be booked.
type lineFailures struct {
	permanent []string
	transient []error
}

func (f *lineFailures) add(what string, err error) {
	var p *httpx.Problem
	if errors.As(err, &p) && p.Status >= 400 && p.Status < 500 && p.Code != store.CodeKeyInFlight {
		f.permanent = append(f.permanent, fmt.Sprintf("%s: %s %s", what, p.Code, p.Detail))
		return
	}
	if eventing.IsPermanent(err) {
		f.permanent = append(f.permanent, what+": "+err.Error())
		return
	}
	f.transient = append(f.transient, fmt.Errorf("%s: %w", what, err))
}

// err retries while any line failed for a reason that may pass, and parks the
// event once only permanent failures are left.
func (f *lineFailures) err(evt events.Event) error {
	if len(f.transient) > 0 {
		return fmt.Errorf("%s %s: %w", evt.Type, evt.ID, errors.Join(f.transient...))
	}
	if len(f.permanent) > 0 {
		return eventing.Permanent(fmt.Errorf("%s %s: lines not booked: %s", evt.Type, evt.ID, strings.Join(f.permanent, "; ")))
	}
	return nil
}

// inventoryQty converts a plain line's quantity (positive) into the product's
// inventory unit. A missing converter or a refused conversion is permanent.
func inventoryQty(ctx context.Context, conv UnitConverter, legalEntityID, productID, unit string, qty decimal.Decimal) (decimal.Decimal, string, error) {
	if conv == nil {
		return decimal.Zero, "", eventing.Permanent(fmt.Errorf("%s: a plain line needs a unit conversion and the consumer has none (PRODUCT_URL)", CodePostingNoConverter))
	}
	out, invUnit, err := conv.ToInventoryUnit(ctx, legalEntityID, productID, unit, qty)
	switch {
	case errors.Is(err, productunits.ErrNoConversion):
		return decimal.Zero, "", eventing.Permanent(fmt.Errorf("%s: %w", CodePostingNoConversion, err))
	case err != nil:
		return decimal.Zero, "", fmt.Errorf("unit conversion: %w", err)
	case !out.IsPositive() || out.Exponent() < -domain.DefaultJournalRules.MaxScale:
		return decimal.Zero, "", eventing.Permanent(fmt.Errorf("%s: %s %s converts to %s %s, not a positive quantity of at most %d decimals",
			CodePostingNoConversion, qty, unit, out, invUnit, domain.DefaultJournalRules.MaxScale))
	}
	return out, invUnit, nil
}

func bookReceipt(ctx context.Context, s PostingsStore, conv UnitConverter, evt events.Event) error {
	var d receiptPostedData
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return eventing.Permanent(fmt.Errorf("receipt.posted data: %w", err))
	}
	ctx, err := postingsScope(ctx, evt, ReceiptSource, d.LegalEntityID)
	if err != nil {
		return err
	}
	date, err := time.Parse(postingsDateLayout, d.PostingDate)
	if !ids.Valid(d.ReceiptID) || err != nil || !currencyCode.MatchString(d.Currency) {
		return eventing.Permanent(errors.New("receipt.posted needs receipt_id, posting_date and currency"))
	}
	le := strings.ToLower(d.LegalEntityID)
	var failed lineFailures
	booked := 0
	for _, l := range d.Lines {
		what := "line " + strconv.Itoa(l.LineNo)
		qty, qerr := decimal.NewFromString(l.Qty)
		price, perr := decimal.NewFromString(l.UnitPrice)
		unit := postingUnit(l.Unit)
		switch {
		case !ids.Valid(l.ProductID) || qerr != nil || !qty.IsPositive() || perr != nil || price.IsNegative() || !unitCode.MatchString(unit):
			failed.add(what, eventing.Permanent(errors.New("needs product_id, a positive qty, unit_price and a unit")))
			continue
		case !ids.Valid(l.SiteID):
			failed.add(what, eventing.Permanent(errors.New("has no site_id (a receipt line without a PO line has no delivery site)")))
			continue
		case len(l.Serials) > 0:
			failed.add(what, eventing.Permanent(errors.New("serial-tracked receipt lines are not booked by the postings consumer")))
			continue
		}
		product := strings.ToLower(l.ProductID)
		in := store.CreateTransactionInput{Qty: qty, CostPrice: price}
		if l.CWQty <= 0 {
			// A plain line: qty is in the document unit; the inventory books the
			// product's inventory unit, valued at the receipt's own amount.
			invQty, invUnit, err := inventoryQty(ctx, conv, le, product, unit, qty)
			if err != nil {
				failed.add(what, err)
				continue
			}
			if invUnit != unit || !invQty.Equal(qty) {
				amount, aerr := decimal.NewFromString(l.Amount)
				if aerr != nil || amount.IsNegative() {
					failed.add(what, eventing.Permanent(errors.New("a converted line needs its amount")))
					continue
				}
				value := money.Amount{Value: amount, Currency: d.Currency}.Round().Value
				in.Qty, in.CostPrice, in.Value = invQty, value.DivRound(invQty, issueCostScale), &value
			}
		}
		dims := domain.Dimensions{domain.DimSite: strings.ToLower(l.SiteID)}
		if l.WarehouseID != "" {
			dims[domain.DimWarehouse] = strings.ToLower(l.WarehouseID)
		}
		if st := strings.TrimSpace(l.InventoryStatus); st != "" && st != inventoryAvailable {
			dims[domain.DimStatus] = st
		}
		in.LegalEntityID, in.SourceType, in.SourceID = le, sourceReceiptLine, receiptLineSourceID(evt.TenantID, d.ReceiptID, l.LineNo)
		in.Reference, in.ProductID, in.Dims = postingReference(d.ReceiptNumber), product, dims
		in.EnteredQty, in.EnteredUnit, in.Status, in.Currency, in.CWQty, in.Date = qty, unit, domain.StatusReceived, d.Currency, l.CWQty, date
		in.IdempotencyKey = postingsKeyPrefix + "receipt:" + strings.ToLower(d.ReceiptID) + ":" + strconv.Itoa(l.LineNo)
		change, applied, err := s.CreateTransaction(ctx, in)
		if err != nil {
			failed.add(what, err)
			continue
		}
		booked++
		slog.InfoContext(ctx, "receipt line booked", "receiptId", d.ReceiptID, "lineNo", l.LineNo, "transactionId", change.TransactionID,
			"qty", in.Qty.String(), "cwQty", l.CWQty, "applied", applied, "eventId", evt.ID)
	}
	if err := failed.err(evt); err != nil {
		slog.WarnContext(ctx, "receipt lines not booked", "receiptId", d.ReceiptID, "booked", booked, "err", err, "eventId", evt.ID)
		return err
	}
	return nil
}

// issueCostScale is the scale of a derived unit cost (cost prices are numeric(20,6)).
const issueCostScale = 6

// unitCost is the booked cost per inventory unit of a transaction.
func unitCost(t store.TransactionRow) decimal.Decimal {
	if t.Qty.IsZero() {
		return decimal.Zero
	}
	return t.CostAmountPhysical.Abs().DivRound(t.Qty.Abs(), issueCostScale)
}

// signedCases is the case count of a booking with the sign of its quantity.
func signedCases(t store.TransactionRow) int {
	if t.CWQty == nil {
		return 0
	}
	if t.Qty.IsNegative() {
		return -*t.CWQty
	}
	return *t.CWQty
}

// compensation books qty (inventory unit, signed) of the line orig booked as
// a new registration linked to it: a receipt at RECEIVED or an issue at
// DEDUCTED, in orig's product, dimensions, unit and currency, at value.
func compensation(orig store.TransactionRow, sourceType, sourceID, reference string, qty, entered, value decimal.Decimal, cases int,
	date time.Time, key string, neg domain.NegativePolicy) store.CreateTransactionInput {
	status := domain.StatusReceived
	if qty.IsNegative() {
		status = domain.StatusDeducted
	}
	if cases < 0 {
		cases = -cases
	}
	return store.CreateTransactionInput{
		LegalEntityID: orig.LegalEntityID, SourceType: sourceType, SourceID: sourceID, Reference: postingReference(reference),
		ProductID: orig.ProductID, Dims: orig.Dims, Qty: qty, EnteredQty: entered, EnteredUnit: orig.EnteredUnit, Status: status,
		Currency: orig.Currency, CostPrice: unitCost(orig), CWQty: cases, Date: date, IdempotencyKey: key, Negative: neg,
		Corrects: orig.ID, Value: &value,
	}
}

// bookReceiptCorrection books receipt.corrected against the booked receipt
// lines. A CORRECTION books each line's delta, converted at the booked line's
// own unit factor and valued at its booked cost. A CANCELLATION offsets what
// the line holds after its booked corrections, exactly (quantity, cases and
// value), once the booked net matches the quantity receiving cancels.
func bookReceiptCorrection(ctx context.Context, s PostingsStore, neg domain.NegativePolicy, evt events.Event) error {
	var d receiptCorrectedData
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return eventing.Permanent(fmt.Errorf("receipt.corrected data: %w", err))
	}
	ctx, err := postingsScope(ctx, evt, ReceiptSource, d.LegalEntityID)
	if err != nil {
		return err
	}
	date, err := postingDate(d.PostingDate, evt)
	if !ids.Valid(d.ReceiptID) || err != nil || (d.Kind != correctionKind && d.Kind != cancellationKind) ||
		(d.Kind == correctionKind && d.CorrectionSeq < 1) || d.CorrectionSeq < 0 {
		return eventing.Permanent(errors.New("receipt.corrected needs receipt_id, a CORRECTION (correction_seq from 1) or CANCELLATION kind and a valid posting_date"))
	}
	le, receipt := strings.ToLower(d.LegalEntityID), strings.ToLower(d.ReceiptID)
	// The booked line, and on a cancellation every booked correction of it.
	var origins []string
	for _, l := range d.Lines {
		origins = append(origins, receiptLineSourceID(evt.TenantID, receipt, l.LineNo))
		if d.Kind == cancellationKind {
			for seq := 1; seq <= d.CorrectionSeq; seq++ {
				origins = append(origins, receiptCorrectionSourceID(evt.TenantID, receipt, seq, l.LineNo))
			}
		}
	}
	booked, err := s.OriginBookings(ctx, le, sourceReceiptLine, origins)
	if err != nil {
		return fmt.Errorf("read the booked receipt lines: %w", err)
	}
	var failed lineFailures
	done := 0
	for _, l := range d.Lines {
		what := "line " + strconv.Itoa(l.LineNo)
		delta, derr := decimal.NewFromString(l.QtyDelta)
		if derr != nil {
			failed.add(what, eventing.Permanent(errors.New("needs a qty_delta")))
			continue
		}
		orig, ok := booked[receiptLineSourceID(evt.TenantID, receipt, l.LineNo)]
		switch {
		case !ok:
			failed.add(what, eventing.Permanent(fmt.Errorf("%s: receipt line %s/%d was not booked", CodePostingOriginalMissing, receipt, l.LineNo)))
			continue
		case !strings.EqualFold(orig.ProductID, l.ProductID) || orig.EnteredQty.IsZero():
			failed.add(what, eventing.Permanent(fmt.Errorf("%s: product %s, the booked line holds %s", CodePostingMismatch, l.ProductID, orig.ProductID)))
			continue
		case delta.IsZero():
			continue // nothing left to offset
		}
		var in store.CreateTransactionInput
		if d.Kind == correctionKind {
			// The delta is in the document unit; the booked line's factor
			// (inventory qty per entered qty) converts it.
			qty := delta
			if !orig.Qty.Equal(orig.EnteredQty) {
				qty = delta.Mul(orig.Qty).DivRound(orig.EnteredQty, unitFactorScale)
			}
			if qty.IsZero() || (l.CWQtyDelta != 0 && (l.CWQtyDelta > 0) != qty.IsPositive()) {
				failed.add(what, eventing.Permanent(fmt.Errorf("%s: a delta of %s with %d cases cannot be booked as one movement", CodePostingMismatch, l.QtyDelta, l.CWQtyDelta)))
				continue
			}
			value := domain.ReceiptValue(qty, unitCost(orig), orig.Currency)
			src := receiptCorrectionSourceID(evt.TenantID, receipt, d.CorrectionSeq, l.LineNo)
			key := postingsKeyPrefix + "receipt-correction:" + receipt + ":" + strconv.Itoa(d.CorrectionSeq) + ":" + strconv.Itoa(l.LineNo)
			in = compensation(orig, sourceReceiptLine, src, d.ReceiptNumber, qty, delta, value, l.CWQtyDelta, date, key, neg)
		} else {
			qty, entered, value, cases := orig.Qty, orig.EnteredQty, orig.CostAmountPhysical, signedCases(orig)
			for seq := 1; seq <= d.CorrectionSeq; seq++ {
				if c, ok := booked[receiptCorrectionSourceID(evt.TenantID, receipt, seq, l.LineNo)]; ok {
					qty, entered, value, cases = qty.Add(c.Qty), entered.Add(c.EnteredQty), value.Add(c.CostAmountPhysical), cases+signedCases(c)
				}
			}
			if !entered.Equal(delta.Neg()) {
				failed.add(what, eventing.Permanent(fmt.Errorf("%s: receiving cancels %s, the booked line holds %s (a correction of it was not booked)",
					CodePostingMismatch, delta.Neg(), entered)))
				continue
			}
			if qty.IsZero() {
				continue
			}
			src := postingSourceID(evt.TenantID, "receipt-line-cancellation", receipt, strconv.Itoa(l.LineNo))
			key := postingsKeyPrefix + "receipt-cancellation:" + receipt + ":" + strconv.Itoa(l.LineNo)
			in = compensation(orig, sourceReceiptLine, src, d.ReceiptNumber, qty.Neg(), delta, value.Neg(), cases, date, key, neg)
		}
		change, applied, err := s.CreateTransaction(ctx, in)
		if err != nil {
			failed.add(what, err)
			continue
		}
		done++
		slog.InfoContext(ctx, "receipt correction booked", "receiptId", d.ReceiptID, "kind", d.Kind, "lineNo", l.LineNo, "transactionId", change.TransactionID,
			"corrects", orig.ID, "qty", in.Qty.String(), "applied", applied, "eventId", evt.ID)
	}
	if err := failed.err(evt); err != nil {
		slog.WarnContext(ctx, "receipt correction lines not booked", "receiptId", d.ReceiptID, "booked", done, "err", err, "eventId", evt.ID)
		return err
	}
	return nil
}

func bookDelivery(ctx context.Context, s PostingsStore, conv UnitConverter, neg domain.NegativePolicy, evt events.Event) error {
	var d orderProgressData
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return eventing.Permanent(fmt.Errorf("orderprogress.recorded data: %w", err))
	}
	if d.Kind != progressDelivered {
		return nil // an invoice report moves no stock
	}
	ctx, err := postingsScope(ctx, evt, SalesSource, d.LegalEntityID)
	if err != nil {
		return err
	}
	if !ids.Valid(d.ProgressID) || !ids.Valid(d.OrderID) || strings.TrimSpace(d.SourceRef) == "" {
		return eventing.Permanent(errors.New("orderprogress.recorded needs progress_id, order_id and source_ref"))
	}
	date := evt.Time.UTC()
	le := strings.ToLower(d.LegalEntityID)
	var failed lineFailures
	booked := 0
	for _, l := range d.Lines {
		what := "line " + strconv.Itoa(l.LineNo)
		quantity, qerr := decimal.NewFromString(l.Quantity)
		unit := postingUnit(l.Unit)
		catchWeight := l.ReportedWeight != ""
		switch {
		case !ids.Valid(l.LineID) || !ids.Valid(l.ProductID) || qerr != nil || !quantity.IsPositive() || !unitCode.MatchString(unit):
			failed.add(what, eventing.Permanent(errors.New("needs line_id, product_id, unit and a positive quantity")))
			continue
		case !ids.Valid(l.SiteID):
			failed.add(what, eventing.Permanent(errors.New("has no site_id")))
			continue
		case !currencyCode.MatchString(d.Currency):
			failed.add(what, eventing.Permanent(errors.New("the report has no order currency")))
			continue
		}
		product := strings.ToLower(l.ProductID)
		// qty is in the inventory unit: the weight of a catch-weight line (its
		// cases are cw_qty and, as entered on the sales line, enteredQty in the
		// case unit), the converted quantity of a plain one.
		var qty decimal.Decimal
		cwQty := 0
		if catchWeight {
			weight, werr := decimal.NewFromString(l.ReportedWeight)
			if werr != nil || !weight.IsPositive() || !quantity.IsInteger() || quantity.GreaterThan(decimal.NewFromInt(maxCWQty)) {
				failed.add(what, eventing.Permanent(errors.New("a catch-weight line needs whole cases and a positive reported_weight")))
				continue
			}
			qty, cwQty = weight, int(quantity.IntPart())
		} else {
			if qty, _, err = inventoryQty(ctx, conv, le, product, unit, quantity); err != nil {
				failed.add(what, err)
				continue
			}
		}
		dims := domain.Dimensions{domain.DimSite: strings.ToLower(l.SiteID)}
		if l.WarehouseID != "" {
			dims[domain.DimWarehouse] = strings.ToLower(l.WarehouseID)
		}
		// The issue is valued at the row's running average (physical receipts
		// included) in the currency the stock is held in; the order currency
		// only names a row with no value.
		currency, cost, err := s.IssueBasis(ctx, le, product, dims)
		if err != nil {
			failed.add(what, err)
			continue
		}
		if currency == "" {
			currency = d.Currency
		}
		src := deliverySourceID(evt.TenantID, d.OrderID, d.SourceRef, l.LineID)
		change, applied, err := s.CreateTransaction(ctx, store.CreateTransactionInput{
			LegalEntityID: le, SourceType: sourceSalesLine, SourceID: src,
			Reference: postingReference(d.SourceRef), ProductID: product, Dims: dims,
			Qty: qty.Neg(), EnteredQty: quantity.Neg(), EnteredUnit: unit, Status: domain.StatusDeducted,
			Currency: currency, CostPrice: cost, CWQty: cwQty, Date: date, IdempotencyKey: postingsKeyPrefix + "delivery:" + src, Negative: neg,
		})
		if err != nil {
			failed.add(what, err)
			continue
		}
		booked++
		slog.InfoContext(ctx, "sales delivery booked", "orderId", d.OrderID, "lineId", l.LineID, "transactionId", change.TransactionID,
			"qty", qty.Neg().String(), "cwQty", cwQty, "applied", applied, "eventId", evt.ID)
	}
	if err := failed.err(evt); err != nil {
		slog.WarnContext(ctx, "delivery lines not booked", "orderId", d.OrderID, "booked", booked, "err", err, "eventId", evt.ID)
		return err
	}
	return nil
}

// bookSlipCancellation books the mirror of each delivery line booked for the
// cancelled packing slip: a receipt at RECEIVED of the booked quantity, cases
// and value. The delivery arrives on sales' topic and may not be booked yet
// when the cancellation is read, so a missing booking is retried with
// back-off before the event is parked.
func bookSlipCancellation(ctx context.Context, s PostingsStore, evt events.Event) error {
	var d packingSlipCancelledData
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return eventing.Permanent(fmt.Errorf("packingslip.cancelled data: %w", err))
	}
	ctx, err := postingsScope(ctx, evt, FulfillmentSource, d.LegalEntityID)
	if err != nil {
		return err
	}
	date, err := postingDate(d.PostingDate, evt)
	if !ids.Valid(d.PackingSlipID) || !ids.Valid(d.ReversalID) || !ids.Valid(d.OrderID) || strings.TrimSpace(d.PackingSlipNumber) == "" || err != nil {
		return eventing.Permanent(errors.New("packingslip.cancelled needs packing_slip_id, packing_slip_number, reversal_id, order_id and a valid posting_date"))
	}
	le := strings.ToLower(d.LegalEntityID)
	origins := make([]string, 0, len(d.Lines))
	for _, l := range d.Lines {
		origins = append(origins, deliverySourceID(evt.TenantID, d.OrderID, d.PackingSlipNumber, l.OrderLineID))
	}
	booked, err := s.OriginBookings(ctx, le, sourceSalesLine, origins)
	if err != nil {
		return fmt.Errorf("read the booked deliveries: %w", err)
	}
	var failed lineFailures
	done := 0
	for i, l := range d.Lines {
		what := "order line " + l.OrderLineID
		qty, qerr := decimal.NewFromString(l.Qty)
		orig, ok := booked[origins[i]]
		switch {
		case !ids.Valid(l.OrderLineID) || qerr != nil:
			failed.add(what, eventing.Permanent(errors.New("needs order_line_id and qty")))
			continue
		case !ok:
			failed.add(what, fmt.Errorf("%s: no delivery of packing slip %s is booked for the line yet", CodePostingOriginalMissing, d.PackingSlipNumber))
			continue
		case !orig.Qty.IsNegative() || !qty.Abs().Equal(orig.EnteredQty.Abs()):
			failed.add(what, eventing.Permanent(fmt.Errorf("%s: the slip reverses %s, the booked delivery issued %s %s",
				CodePostingMismatch, qty.Abs(), orig.EnteredQty.Abs(), orig.EnteredUnit)))
			continue
		}
		src := postingSourceID(evt.TenantID, "sales-delivery-cancellation", orig.SourceID)
		in := compensation(orig, sourceSalesLine, src, d.PackingSlipNumber, orig.Qty.Neg(), orig.EnteredQty.Neg(), orig.CostAmountPhysical.Neg(),
			signedCases(orig), date, postingsKeyPrefix+"delivery-cancellation:"+src, domain.NegativePolicy{})
		change, applied, err := s.CreateTransaction(ctx, in)
		if err != nil {
			failed.add(what, err)
			continue
		}
		done++
		slog.InfoContext(ctx, "cancelled delivery booked", "packingSlipId", d.PackingSlipID, "orderLineId", l.OrderLineID, "transactionId", change.TransactionID,
			"corrects", orig.ID, "qty", in.Qty.String(), "applied", applied, "eventId", evt.ID)
	}
	if err := failed.err(evt); err != nil {
		slog.WarnContext(ctx, "cancelled delivery lines not booked", "packingSlipId", d.PackingSlipID, "booked", done, "err", err, "eventId", evt.ID)
		return err
	}
	return nil
}
