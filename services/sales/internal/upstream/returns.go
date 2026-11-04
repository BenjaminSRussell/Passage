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

	"github.com/BenjaminSRussell/passage/platform/ids"
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
	dutyReturnReceipt    = "inventory.return-receipt.post"
	dutyRMACreditPost    = "ar.rmacredit.post"
	returnReceiptPath    = "/internal/v1/inventory/return-receipts"
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
		credited, cerr := decimal.Zero, error(nil)
		if l.CreditedQty != "" {
			credited, cerr = decimal.NewFromString(l.CreditedQty)
		}
		if err := errors.Join(qerr, nerr, cerr); err != nil {
			return store.Invoice{}, fmt.Errorf("billing invoice line %d: %w", l.LineNo, err)
		}
		out.Lines = append(out.Lines, store.InvoiceLine{LineNo: l.LineNo, ProductID: l.ProductID, OrderLineID: l.OrderLineID, PackingSlipID: l.PackingSlip,
			Qty: qty, Net: net, CreditedQty: credited})
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

// CurrentUnitCost is the on-hand average cost of the product at the site and warehouse. Inventory keeps dimensions
// as free text, and site and warehouse hold the org UUIDs in whatever case the posting service wrote them, so the
// inquiry groups the product's on-hand by legal entity, site and warehouse and the match is made on the canonical
// UUIDs here rather than by an exact-text filter.
func (r *Returns) CurrentUnitCost(ctx context.Context, legalEntityID, productID, siteID, warehouseID string) (decimal.Decimal, bool, error) {
	site, okSite := ids.Canonical(siteID)
	warehouse, okWarehouse := ids.Canonical(warehouseID)
	le, okLE := ids.Canonical(legalEntityID)
	if !okSite || !okWarehouse || !okLE {
		return decimal.Zero, false, nil
	}
	q := url.Values{"product": {productID}, "groupBy": {"legal_entity,site,warehouse"}}
	var out struct {
		Items []struct {
			Group          map[string]string `json:"group"`
			PhysicalInvent string            `json:"physicalInvent"`
			PostedValue    string            `json:"postedValue"`
		} `json:"items"`
		Truncated bool `json:"truncated"`
	}
	if err := r.call(ctx, r.inventoryURL, inventoryAudience, http.MethodGet, "/v1/inventory/onhand?"+q.Encode(), dutyInventoryRead, legalEntityID, "", nil, &out); err != nil {
		return decimal.Zero, false, err
	}
	if out.Truncated {
		return decimal.Zero, false, errors.New("inventory on-hand: the product's groups were truncated")
	}
	qty, value := decimal.Zero, decimal.Zero
	for _, it := range out.Items {
		gotLE, _ := ids.Canonical(it.Group["legal_entity"])
		gotSite, _ := ids.Canonical(strings.TrimSpace(it.Group["site"]))
		gotWarehouse, _ := ids.Canonical(strings.TrimSpace(it.Group["warehouse"]))
		if gotLE != le || gotSite != site || gotWarehouse != warehouse {
			continue
		}
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
	ID            string `json:"id"`
	Status        string `json:"status"`
	JournalType   string `json:"journalType"`
	LegalEntityID string `json:"legalEntityId"`
	Lines         []struct {
		ClientKey string `json:"clientKey"`
		ProductID string `json:"productId"`
		Qty       string `json:"qty"`
	} `json:"lines"`
}

// ErrForeignReceipt is a journal under a receipt's key that is not that receipt.
var ErrForeignReceipt = errors.New("inventory answered a journal that is not this return receipt")

// ReceiveReturn books the accepted quantity into stock through inventory's service-only return-receipt command: one
// MOVEMENT journal of one line, keyed by the receipt's per-line key, created and posted in one call, so a retry
// finds the same journal. The answer is trusted only when it is that receipt: a POSTED MOVEMENT journal of the legal
// entity whose only line carries the key, the product and the quantity.
func (r *Returns) ReceiveReturn(ctx context.Context, in store.ReturnReceipt) (string, error) {
	if r == nil || r.offsetAccount == "" {
		return "", ErrNotConfigured
	}
	body := map[string]any{"legalEntityId": in.LegalEntityID, "postingDate": in.PostingDate, "currency": in.Currency, "offsetAccountId": r.offsetAccount,
		"productId": in.ProductID, "qty": in.Quantity.String(), "enteredUnit": in.Unit, "costPrice": in.UnitCost.String(),
		"dimensions": map[string]string{"site": in.SiteID, "warehouse": in.WarehouseID}}
	var j journalBody
	if err := r.call(ctx, r.inventoryURL, inventoryAudience, http.MethodPost, returnReceiptPath, dutyReturnReceipt, in.LegalEntityID, in.Key, body, &j); err != nil {
		return "", err
	}
	le, _ := ids.Canonical(j.LegalEntityID)
	want, _ := ids.Canonical(in.LegalEntityID)
	if j.ID == "" || j.Status != journalStatusPosted || j.JournalType != journalTypeMovement || le != want || len(j.Lines) != 1 ||
		j.Lines[0].ClientKey != in.Key || j.Lines[0].ProductID != in.ProductID {
		return "", fmt.Errorf("%w (journal %q status %q type %q)", ErrForeignReceipt, j.ID, j.Status, j.JournalType)
	}
	if qty, err := decimal.NewFromString(j.Lines[0].Qty); err != nil || !qty.Equal(in.Quantity) {
		return "", fmt.Errorf("%w (journal %q quantity %q)", ErrForeignReceipt, j.ID, j.Lines[0].Qty)
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
