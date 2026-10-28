package purchasing

import (
	"context"
	"encoding/json"
	"errors"
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
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
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

func (c *Client) Get(ctx context.Context, orderID string) (domain.PurchaseOrder, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return domain.PurchaseOrder{}, errors.New("purchase order get: no principal in context")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+fmt.Sprintf(domain.PurchaseOrderPathTmpl, url.PathEscape(orderID)), nil)
	if err != nil {
		return domain.PurchaseOrder{}, fmt.Errorf("purchase order get %s: %w", orderID, err)
	}
	identity.Forward(req, principal)

	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "purchase order get transport failed", "err", err, "purchaseOrderId", orderID, "correlationId", middleware.GetReqID(ctx))
		return domain.PurchaseOrder{}, fmt.Errorf("purchase order get %s: %w", orderID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		slog.ErrorContext(ctx, "purchase order get read failed", "err", err, "purchaseOrderId", orderID, "correlationId", middleware.GetReqID(ctx))
		return domain.PurchaseOrder{}, fmt.Errorf("purchase order get %s: %w", orderID, err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var out orderBody
		if err := json.Unmarshal(raw, &out); err != nil || out.ID == "" || out.badLine() {
			slog.ErrorContext(ctx, "purchase order response unusable", "err", err, "purchaseOrderId", orderID, "correlationId", middleware.GetReqID(ctx))
			return domain.PurchaseOrder{}, fmt.Errorf("purchase order get %s: response has no usable order", orderID)
		}
		if principal.LegalEntityID != "" && out.LegalEntityID != principal.LegalEntityID {
			return domain.PurchaseOrder{}, fmt.Errorf("purchase order get %s: %w", orderID, domain.ErrPurchaseOrderForbidden)
		}
		return out.toDomain(), nil
	case http.StatusNotFound:
		return domain.PurchaseOrder{}, fmt.Errorf("purchase order get %s: %w", orderID, domain.ErrPurchaseOrderNotFound)
	case http.StatusForbidden:
		return domain.PurchaseOrder{}, fmt.Errorf("purchase order get %s: %w", orderID, domain.ErrPurchaseOrderForbidden)
	}
	err = fmt.Errorf("purchase order get %s: unexpected status %d", orderID, resp.StatusCode)
	slog.ErrorContext(ctx, "purchase order get rejected", "err", err, "status", resp.StatusCode, "purchaseOrderId", orderID, "correlationId", middleware.GetReqID(ctx))
	return domain.PurchaseOrder{}, err
}

func (o orderBody) badLine() bool {
	for _, l := range o.Lines {
		if l.PriceUnit.Sign() <= 0 {
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
