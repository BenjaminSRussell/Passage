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
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// The postings consumer (#480) books what receiving and sales post as
// inventory transactions through the transactions:apply store path:
//
//   - receiving's receipt.posted: a receipt line at RECEIVED (origin
//     RECEIPT_LINE), valued at qty x unit_price in the receipt currency;
//   - sales' orderprogress.recorded of kind DELIVERED (the packing slip
//     fulfillment reported): an issue at DEDUCTED (origin SALES_LINE), valued
//     at the row's running average per weight unit (store.IssueBasis).
//
// Only catch-weight lines are booked. The catch-weight convention (one for
// receiving, sales and inventory) fixes their inventory unit: the receipt line
// unit is the weight unit, so qty is the weight and cw_qty the cases, and a
// sales line's delivered weight is reported_weight while its quantity is cases.
// A plain line's document unit may differ from the product's inventory unit
// and no unit conversion is available to this service yet, so plain lines are
// skipped (receiving #96 and the inventory NOT-IMPLEMENTED row).
//
// Lines are booked under the financial dimensions site and warehouse, plus
// inventory_status for a receipt not received as AVAILABLE, so a delivery
// draws from what was received; location, batch and serial stay with
// warehouse management. Each line is booked under its own idempotency key, so
// a retry books only what is missing. A line that cannot be booked (no site,
// serials, a rule of inventory such as stock that would go negative) fails
// the event after the other lines: a validation or business-rule failure is
// permanent and parks the event on the dead-letter topic, anything else is
// retried with back-off first.
const (
	// PostingsConsumerName is the consumer (and Kafka group) and the service
	// principal it runs as, service:inventory-postings.
	PostingsConsumerName = "inventory-postings"
	// ReceiptPostedType is receiving's receipt.posted.v1, on ReceiptTopic.
	ReceiptPostedType = "dev.passage.receiving.receipt.posted.v1"
	ReceiptTopic      = "passage.receiving.receipt"
	ReceiptSource     = "/passage/receiving/receiving"
	// OrderProgressRecordedType is sales' orderprogress.recorded.v1, on OrderProgressTopic.
	OrderProgressRecordedType = "dev.passage.sales.orderprogress.recorded.v1"
	OrderProgressTopic        = "passage.sales.orderprogress"
	SalesSource               = "sales"

	sourceReceiptLine   = "RECEIPT_LINE"
	sourceSalesLine     = "SALES_LINE"
	inventoryAvailable  = "AVAILABLE"
	progressDelivered   = "DELIVERED"
	postingsKeyPrefix   = "inventory-postings:"
	postingsDateLayout  = "2006-01-02"
	maxPostingReference = 60
)

// PostingsStore is what the postings consumer needs of the store.
type PostingsStore interface {
	CreateTransaction(ctx context.Context, in store.CreateTransactionInput) (store.StatusChange, bool, error)
	IssueBasis(ctx context.Context, legalEntityID, productID string, dims domain.Dimensions) (string, decimal.Decimal, error)
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
		UnitPrice       string   `json:"unit_price"`
		SiteID          string   `json:"site_id"`
		WarehouseID     string   `json:"warehouse_id"`
		CWQty           int      `json:"cw_qty"`
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

// PostingsConsumerHandlers handles receipt.posted and orderprogress.recorded.
// Register it with a consumer named PostingsConsumerName with Idempotent set:
// each booking commits on its own under its own key, and the inbox records the
// event once every line is booked.
func PostingsConsumerHandlers(s PostingsStore, neg domain.NegativePolicy) map[string]eventing.Handler {
	return map[string]eventing.Handler{
		ReceiptPostedType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			return bookReceipt(ctx, s, evt)
		}),
		OrderProgressRecordedType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			return bookDelivery(ctx, s, neg, evt)
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

// postingSourceID is the origin id of one booked line: receipt lines and the
// lines of one delivery have no UUID of their own, so it is derived from the
// document and the line (a name-based UUID, version 5 layout).
func postingSourceID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	sum[6] = sum[6]&0x0f | 0x50
	sum[8] = sum[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

func postingReference(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxPostingReference {
		return string(r[:maxPostingReference])
	}
	return s
}

func postingUnit(u string) string { return strings.ToUpper(strings.TrimSpace(u)) }

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

func bookReceipt(ctx context.Context, s PostingsStore, evt events.Event) error {
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
	var failed lineFailures
	booked := 0
	for _, l := range d.Lines {
		if l.CWQty <= 0 {
			continue // plain line: no unit conversion yet
		}
		what := "line " + strconv.Itoa(l.LineNo)
		qty, qerr := decimal.NewFromString(l.Qty)
		price, perr := decimal.NewFromString(l.UnitPrice)
		switch {
		case !ids.Valid(l.ProductID) || qerr != nil || !qty.IsPositive() || perr != nil || price.IsNegative() || !unitCode.MatchString(postingUnit(l.Unit)):
			failed.add(what, eventing.Permanent(errors.New("needs product_id, a positive qty, unit_price and a unit")))
			continue
		case !ids.Valid(l.SiteID):
			failed.add(what, eventing.Permanent(errors.New("has no site_id (a receipt line without a PO line has no delivery site)")))
			continue
		case len(l.Serials) > 0:
			failed.add(what, eventing.Permanent(errors.New("serial-tracked receipt lines are not booked by the postings consumer")))
			continue
		}
		dims := domain.Dimensions{domain.DimSite: strings.ToLower(l.SiteID)}
		if l.WarehouseID != "" {
			dims[domain.DimWarehouse] = strings.ToLower(l.WarehouseID)
		}
		if st := strings.TrimSpace(l.InventoryStatus); st != "" && st != inventoryAvailable {
			dims[domain.DimStatus] = st
		}
		key := postingsKeyPrefix + "receipt:" + strings.ToLower(d.ReceiptID) + ":" + strconv.Itoa(l.LineNo)
		change, applied, err := s.CreateTransaction(ctx, store.CreateTransactionInput{
			LegalEntityID: strings.ToLower(d.LegalEntityID), SourceType: sourceReceiptLine,
			SourceID:  postingSourceID(evt.TenantID, "receipt-line", strings.ToLower(d.ReceiptID), strconv.Itoa(l.LineNo)),
			Reference: postingReference(d.ReceiptNumber), ProductID: strings.ToLower(l.ProductID), Dims: dims,
			Qty: qty, EnteredQty: qty, EnteredUnit: postingUnit(l.Unit), Status: domain.StatusReceived,
			Currency: d.Currency, CostPrice: price, CWQty: l.CWQty, Date: date, IdempotencyKey: key,
		})
		if err != nil {
			failed.add(what, err)
			continue
		}
		booked++
		slog.InfoContext(ctx, "receipt line booked", "receiptId", d.ReceiptID, "lineNo", l.LineNo, "transactionId", change.TransactionID,
			"qty", qty.String(), "cwQty", l.CWQty, "applied", applied, "eventId", evt.ID)
	}
	if err := failed.err(evt); err != nil {
		slog.WarnContext(ctx, "receipt lines not booked", "receiptId", d.ReceiptID, "booked", booked, "err", err, "eventId", evt.ID)
		return err
	}
	return nil
}

func bookDelivery(ctx context.Context, s PostingsStore, neg domain.NegativePolicy, evt events.Event) error {
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
	if !ids.Valid(d.ProgressID) || !ids.Valid(d.OrderID) {
		return eventing.Permanent(errors.New("orderprogress.recorded needs progress_id and order_id"))
	}
	date := evt.Time.UTC()
	var failed lineFailures
	booked := 0
	for _, l := range d.Lines {
		if l.ReportedWeight == "" {
			continue // plain line: no unit conversion yet
		}
		what := "line " + strconv.Itoa(l.LineNo)
		cases, cerr := decimal.NewFromString(l.Quantity)
		weight, werr := decimal.NewFromString(l.ReportedWeight)
		switch {
		case !ids.Valid(l.LineID) || !ids.Valid(l.ProductID) || cerr != nil || werr != nil || !weight.IsPositive() || !cases.IsPositive() ||
			!cases.IsInteger() || cases.GreaterThan(decimal.NewFromInt(maxCWQty)) || !unitCode.MatchString(postingUnit(l.Unit)):
			failed.add(what, eventing.Permanent(errors.New("needs line_id, product_id, unit, whole cases and a positive reported_weight")))
			continue
		case !ids.Valid(l.SiteID):
			failed.add(what, eventing.Permanent(errors.New("has no site_id")))
			continue
		case !currencyCode.MatchString(d.Currency):
			failed.add(what, eventing.Permanent(errors.New("the report has no order currency")))
			continue
		}
		dims := domain.Dimensions{domain.DimSite: strings.ToLower(l.SiteID)}
		if l.WarehouseID != "" {
			dims[domain.DimWarehouse] = strings.ToLower(l.WarehouseID)
		}
		le, product := strings.ToLower(d.LegalEntityID), strings.ToLower(l.ProductID)
		// The issue is valued at the row's running average (physical receipts
		// included) in the currency the stock is held in; the order currency
		// only names a row with no value.
		currency, unitCost, err := s.IssueBasis(ctx, le, product, dims)
		if err != nil {
			failed.add(what, err)
			continue
		}
		if currency == "" {
			currency = d.Currency
		}
		key := postingsKeyPrefix + "delivery:" + strings.ToLower(d.ProgressID) + ":" + strings.ToLower(l.LineID)
		cwQty := int(cases.IntPart())
		change, applied, err := s.CreateTransaction(ctx, store.CreateTransactionInput{
			LegalEntityID: le, SourceType: sourceSalesLine,
			SourceID:  postingSourceID(evt.TenantID, "sales-delivery", strings.ToLower(d.ProgressID), strings.ToLower(l.LineID)),
			Reference: postingReference(d.SourceRef), ProductID: product, Dims: dims,
			// qty is the weight in the inventory (weight) unit; the cases are cw_qty
			// and, as entered on the sales line, enteredQty in the line's case unit.
			Qty: weight.Neg(), EnteredQty: cases.Neg(), EnteredUnit: postingUnit(l.Unit), Status: domain.StatusDeducted,
			Currency: currency, CostPrice: unitCost, CWQty: cwQty, Date: date, IdempotencyKey: key, Negative: neg,
		})
		if err != nil {
			failed.add(what, err)
			continue
		}
		booked++
		slog.InfoContext(ctx, "sales delivery booked", "orderId", d.OrderID, "lineId", l.LineID, "transactionId", change.TransactionID,
			"qty", weight.Neg().String(), "cwQty", cwQty, "applied", applied, "eventId", evt.ID)
	}
	if err := failed.err(evt); err != nil {
		slog.WarnContext(ctx, "delivery lines not booked", "orderId", d.OrderID, "booked", booked, "err", err, "eventId", evt.ID)
		return err
	}
	return nil
}
