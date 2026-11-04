package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const (
	billingAudience     = "billing"
	fulfillmentAudience = "fulfillment"
	inventoryAudience   = "inventory"
	receivablesAudience = "receivables"

	dutyInvoiceRead      = "billing.invoice.read"
	dutyPackingSlipRead  = "fulfillment.packing-slip.read"
	dutyInventoryRead    = "inventory.read"
	dutyJournalWrite     = "inventory.journal.write"
	dutyJournalPost      = "inventory.journal.post"
	dutyRMACreditPost    = "ar.rmacredit.post"
	journalTypeMovement  = "MOVEMENT"
	journalStatusPosted  = "POSTED"
	invoiceTypeCredit    = "CREDIT_NOTE"
	invoiceStatusReverse = "REVERSED"
	invoiceStatusProp    = "PROPOSAL"
)

var ErrNotConfigured = errors.New("upstream service is not configured")

// StatusError is a non-success answer of a peer service.
type StatusError struct {
	Audience string
	Status   int
	Code     string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s responded with status %d code %q", e.Audience, e.Status, e.Code)
}

// Refusal reports the peer's status and problem code.
func (e *StatusError) Refusal() (int, string) { return e.Status, e.Code }

// Returns reads invoices, costs and stock and writes receipts and credits for the return-order flow.
type Returns struct {
	client                                                   *Client
	billingURL, fulfillmentURL, inventoryURL, receivablesURL string
	offsetAccount                                            string
}

// NewReturns builds the return-order peers on the signer and HTTP client of c; a peer whose URL is empty answers ErrNotConfigured.
func NewReturns(c *Client, billingURL, fulfillmentURL, inventoryURL, receivablesURL, offsetAccount string) *Returns {
	trim := func(s string) string { return strings.TrimRight(s, "/") }
	return &Returns{client: c, billingURL: trim(billingURL), fulfillmentURL: trim(fulfillmentURL), inventoryURL: trim(inventoryURL),
		receivablesURL: trim(receivablesURL), offsetAccount: offsetAccount}
}

func (r *Returns) call(ctx context.Context, base, audience, method, path, duty, legalEntityID, key string, payload any, out any, headers ...[2]string) error {
	if r == nil || base == "" {
		return ErrNotConfigured
	}
	var raw []byte
	if payload != nil {
		var err error
		if raw, err = json.Marshal(payload); err != nil {
			return err
		}
	}
	status, body, err := r.client.do(ctx, method, base+path, audience, duty, legalEntityID, key, raw, headers...)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		var p struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(body, &p)
		return &StatusError{Audience: audience, Status: status, Code: p.Code}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s response: %w", audience, err)
	}
	return nil
}

func (r *Returns) ReadInvoice(ctx context.Context, legalEntityID, invoiceID string) (store.Invoice, error) {
	var body struct {
		ID, LegalEntityID, CustomerID, Number, Currency, Status, Type string
		Lines                                                         []struct {
			LineNo       int    `json:"lineNo"`
			Qty          string `json:"qty"`
			Net          string `json:"net"`
			ProductID    string `json:"productId"`
			OrderLineID  string `json:"orderLineId"`
			PackingSlip  string `json:"packingSlipId"`
			CreditedQty  string `json:"creditedQty"`
			OriginalLine int    `json:"originalLineNo"`
		} `json:"lines"`
	}
	err := r.call(ctx, r.billingURL, billingAudience, http.MethodGet, "/v1/billing/invoices/"+url.PathEscape(invoiceID), dutyInvoiceRead, legalEntityID, "", nil, &body)
	var se *StatusError
	if errors.As(err, &se) && se.Status == http.StatusNotFound {
		return store.Invoice{}, store.ErrInvoiceNotFound
	}
	if err != nil {
		return store.Invoice{}, err
	}
	out := store.Invoice{ID: body.ID, Number: body.Number, LegalEntityID: body.LegalEntityID, CustomerID: body.CustomerID, Currency: body.Currency,
		Returnable: body.Type != invoiceTypeCredit && body.Status != invoiceStatusReverse && body.Status != invoiceStatusProp && body.Number != ""}
	for _, l := range body.Lines {
		qty, qerr := decimal.NewFromString(l.Qty)
		net, nerr := decimal.NewFromString(l.Net)
		if err := errors.Join(qerr, nerr); err != nil {
			return store.Invoice{}, fmt.Errorf("billing invoice line %d: %w", l.LineNo, err)
		}
		out.Lines = append(out.Lines, store.InvoiceLine{LineNo: l.LineNo, ProductID: l.ProductID, OrderLineID: l.OrderLineID, PackingSlipID: l.PackingSlip, Qty: qty, Net: net})
	}
	return out, nil
}

func (r *Returns) OriginalUnitCost(ctx context.Context, legalEntityID, packingSlipID string, orderLineIDs []string) (decimal.Decimal, bool, error) {
	var slip struct {
		Lines []struct {
			OrderLineID string `json:"orderLineId"`
			Qty         string `json:"qty"`
			CostAmount  string `json:"costAmount"`
		} `json:"lines"`
	}
	if err := r.call(ctx, r.fulfillmentURL, fulfillmentAudience, http.MethodGet, "/v1/fulfillment/packing-slips/"+url.PathEscape(packingSlipID), dutyPackingSlipRead, legalEntityID, "", nil, &slip); err != nil {
		return decimal.Zero, false, err
	}
	qty, cost := decimal.Zero, decimal.Zero
	for _, l := range slip.Lines {
		if !contains(orderLineIDs, l.OrderLineID) {
			continue
		}
		q, qerr := decimal.NewFromString(l.Qty)
		c, cerr := decimal.NewFromString(l.CostAmount)
		if err := errors.Join(qerr, cerr); err != nil {
			return decimal.Zero, false, fmt.Errorf("packing slip line: %w", err)
		}
		qty, cost = qty.Add(q), cost.Add(c)
	}
	if !qty.IsPositive() {
		return decimal.Zero, false, nil
	}
	return cost.Div(qty).Round(6), true, nil
}

func (r *Returns) CurrentUnitCost(ctx context.Context, legalEntityID, productID, siteID, warehouseID string) (decimal.Decimal, bool, error) {
	q := url.Values{"product": {productID}, "site": {siteID}, "warehouse": {warehouseID}}
	var out struct {
		Items []struct {
			PhysicalInvent string `json:"physicalInvent"`
			PostedValue    string `json:"postedValue"`
		} `json:"items"`
	}
	if err := r.call(ctx, r.inventoryURL, inventoryAudience, http.MethodGet, "/v1/inventory/onhand?"+q.Encode(), dutyInventoryRead, legalEntityID, "", nil, &out); err != nil {
		return decimal.Zero, false, err
	}
	qty, value := decimal.Zero, decimal.Zero
	for _, it := range out.Items {
		q, qerr := decimal.NewFromString(it.PhysicalInvent)
		v, verr := decimal.NewFromString(it.PostedValue)
		if err := errors.Join(qerr, verr); err != nil {
			return decimal.Zero, false, fmt.Errorf("inventory on-hand: %w", err)
		}
		qty, value = qty.Add(q), value.Add(v)
	}
	if !qty.IsPositive() {
		return decimal.Zero, false, nil
	}
	return value.Div(qty).Round(6), true, nil
}

type journalBody struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Version int    `json:"version"`
}

// ReceiveReturn books the accepted quantity into stock as a MOVEMENT journal keyed by the receipt's key, so a retry
// finds the same journal and line; it returns the posted journal's id.
func (r *Returns) ReceiveReturn(ctx context.Context, in store.ReturnReceipt) (string, error) {
	if r == nil || r.offsetAccount == "" {
		return "", ErrNotConfigured
	}
	var j journalBody
	create := map[string]any{"journalType": journalTypeMovement, "legalEntityId": in.LegalEntityID, "postingDate": in.PostingDate,
		"offsetAccountId": r.offsetAccount, "currency": in.Currency}
	if err := r.call(ctx, r.inventoryURL, inventoryAudience, http.MethodPost, "/v1/inventory/journals", dutyJournalWrite, in.LegalEntityID, in.Key, create, &j); err != nil {
		return "", err
	}
	if j.Status == journalStatusPosted {
		return j.ID, nil
	}
	lines := map[string]any{"lines": []map[string]any{{"clientKey": in.Key, "productId": in.ProductID, "qty": in.Quantity.String(), "enteredUnit": in.Unit,
		"costPrice": in.UnitCost.String(), "dimensions": map[string]string{"site": in.SiteID, "warehouse": in.WarehouseID}}}}
	if err := r.call(ctx, r.inventoryURL, inventoryAudience, http.MethodPost, "/v1/inventory/journals/"+url.PathEscape(j.ID)+"/lines:bulk", dutyJournalWrite, in.LegalEntityID, "", lines, &j); err != nil {
		return "", err
	}
	if err := r.call(ctx, r.inventoryURL, inventoryAudience, http.MethodPost, "/v1/inventory/journals/"+url.PathEscape(j.ID)+":post", dutyJournalPost, in.LegalEntityID, "", nil, &j, [2]string{"If-Match", fmt.Sprintf("%q", fmt.Sprint(j.Version))}); err != nil {
		return "", err
	}
	return j.ID, nil
}

func (r *Returns) PostRMACredit(ctx context.Context, in store.ARCredit) (store.CreditNote, error) {
	body := map[string]string{"rmaId": in.RMAID, "rmaNumber": in.RMANumber, "legalEntityId": in.LegalEntityID, "invoiceNumber": in.InvoiceNumber,
		"postingDate": in.PostingDate, "gross": in.Gross.StringFixed(2), "restocking": in.Restocking.StringFixed(2), "net": in.Net.StringFixed(2)}
	var out struct {
		OpenItemID       string `json:"openItemId"`
		DocumentNumber   string `json:"documentNumber"`
		JournalPostingID string `json:"journalPostingId"`
	}
	if err := r.call(ctx, r.receivablesURL, receivablesAudience, http.MethodPost, "/v1/receivables/rma-credits", dutyRMACreditPost, in.LegalEntityID, in.IdempotencyKey, body, &out); err != nil {
		return store.CreditNote{}, err
	}
	return store.CreditNote{OpenItemID: out.OpenItemID, DocumentNumber: out.DocumentNumber, JournalPostingID: out.JournalPostingID}, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
