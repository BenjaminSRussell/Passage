package purchasing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/identity"
)

const (
	Timeout         = 2 * time.Second
	maxResponseSize = 1 << 20
	dutyLookup      = "procurement.purchase-order.lookup"
)

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

type lineBody struct {
	LineNo      int             `json:"lineNo"`
	ProductID   string          `json:"productId"`
	Unit        string          `json:"unit"`
	Qty         decimal.Decimal `json:"qty"`
	UnitPrice   decimal.Decimal `json:"unitPrice"`
	PriceUnit   decimal.Decimal `json:"priceUnit"`
	DiscountPct decimal.Decimal `json:"discountPct"`
	ReceivedQty decimal.Decimal `json:"receivedQty"`
	InvoicedQty decimal.Decimal `json:"invoicedQty"`
	Status      string          `json:"status"`
	SiteID      string          `json:"siteId"`
	WarehouseID string          `json:"warehouseId"`
}

type orderBody struct {
	ID             string     `json:"id"`
	VendorID       string     `json:"vendorId"`
	Currency       string     `json:"currency"`
	LegalEntityID  string     `json:"legalEntityId"`
	ApprovalStatus string     `json:"approvalStatus"`
	DocumentStatus string     `json:"documentStatus"`
	Lines          []lineBody `json:"lines"`
}

func (c *Client) fetch(ctx context.Context, what, legalEntityID, orderID, path string) (int, []byte, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return 0, nil, fmt.Errorf("%s: no principal in context", what)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %w", what, orderID, err)
	}
	if err := c.authenticate(req, principal, legalEntityID); err != nil {
		return 0, nil, fmt.Errorf("%s %s: %w", what, orderID, err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, what+" transport failed", "err", err, "purchaseOrderId", orderID, "correlationId", middleware.GetReqID(ctx))
		return 0, nil, fmt.Errorf("%s %s: %w", what, orderID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		slog.ErrorContext(ctx, what+" read failed", "err", err, "purchaseOrderId", orderID, "correlationId", middleware.GetReqID(ctx))
		return 0, nil, fmt.Errorf("%s %s: %w", what, orderID, err)
	}
	return resp.StatusCode, raw, nil
}

func (c *Client) authenticate(req *http.Request, p tenant.Principal, legalEntityID string) error {
	if c.signer == nil || (legalEntityID == "" && len(tenant.LegalEntityScopeList(p)) != 1) {
		identity.Forward(req, p)
		return nil
	}
	_, err := c.signer.ApplyDuties(req, p, audience, legalEntityID, []string{dutyLookup})
	return err
}

func (c *Client) Get(ctx context.Context, legalEntityID, orderID string) (domain.PurchaseOrder, error) {
	const what = "purchase order get"
	status, raw, err := c.fetch(ctx, what, legalEntityID, orderID, fmt.Sprintf(domain.PurchaseOrderPathTmpl, url.PathEscape(orderID)))
	if err != nil {
		return domain.PurchaseOrder{}, err
	}
	switch status {
	case http.StatusOK:
		principal, _ := tenant.FromContext(ctx)
		var out orderBody
		if err := json.Unmarshal(raw, &out); err != nil || out.ID == "" || out.badLine() {
			slog.ErrorContext(ctx, "purchase order response unusable", "err", err, "purchaseOrderId", orderID, "correlationId", middleware.GetReqID(ctx))
			return domain.PurchaseOrder{}, fmt.Errorf("purchase order get %s: response has no usable order", orderID)
		}
		if !tenant.InLegalEntityScope(principal, out.LegalEntityID) {
			return domain.PurchaseOrder{}, fmt.Errorf("purchase order get %s: %w", orderID, domain.ErrPurchaseOrderForbidden)
		}
		return out.toDomain(), nil
	case http.StatusNotFound:
		return domain.PurchaseOrder{}, fmt.Errorf("purchase order get %s: %w", orderID, domain.ErrPurchaseOrderNotFound)
	case http.StatusForbidden:
		return domain.PurchaseOrder{}, fmt.Errorf("purchase order get %s: %w", orderID, domain.ErrPurchaseOrderForbidden)
	}
	err = fmt.Errorf("purchase order get %s: unexpected status %d", orderID, status)
	slog.ErrorContext(ctx, "purchase order get rejected", "err", err, "status", status, "purchaseOrderId", orderID, "correlationId", middleware.GetReqID(ctx))
	return domain.PurchaseOrder{}, err
}

func (o orderBody) badLine() bool {
	for _, l := range o.Lines {
		if l.PriceUnit.Sign() <= 0 || l.DiscountPct.Sign() < 0 || l.DiscountPct.GreaterThan(decimal.NewFromInt(100)) {
			return true
		}
	}
	return false
}

func (o orderBody) toDomain() domain.PurchaseOrder {
	out := domain.PurchaseOrder{ID: o.ID, VendorID: o.VendorID, Currency: o.Currency, LegalEntityID: o.LegalEntityID, ApprovalStatus: o.ApprovalStatus, DocumentStatus: o.DocumentStatus, Lines: make([]domain.PurchaseOrderLine, 0, len(o.Lines))}
	for _, l := range o.Lines {
		out.Lines = append(out.Lines, domain.PurchaseOrderLine(l))
	}
	return out
}
