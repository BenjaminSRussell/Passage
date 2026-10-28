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
)

const (
	Timeout                  = 2 * time.Second
	dutyPurchaseOrderInquire = "PurchaseOrderInquire"
	maxResponseSize          = 1 << 20
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
	ReceivedQty decimal.Decimal `json:"receivedQty"`
	InvoicedQty decimal.Decimal `json:"invoicedQty"`
	Status      string          `json:"status"`
}

type orderBody struct {
	ID             string     `json:"id"`
	VendorID       string     `json:"vendorId"`
	Currency       string     `json:"currency"`
	DocumentStatus string     `json:"documentStatus"`
	Lines          []lineBody `json:"lines"`
}

func (c *Client) Get(ctx context.Context, id string) (domain.PurchaseOrder, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return domain.PurchaseOrder{}, errors.New("purchase order get: no principal in context")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+fmt.Sprintf(domain.PurchaseOrderPathTmpl, url.PathEscape(id)), nil)
	if err != nil {
		return domain.PurchaseOrder{}, fmt.Errorf("purchase order get %s: %w", id, err)
	}
	req.Header.Set(tenant.HeaderDuties, dutyPurchaseOrderInquire)
	req.Header.Set(tenant.HeaderTenant, principal.TenantID)
	if principal.LegalEntityID != "" {
		req.Header.Set(tenant.HeaderLegalEntity, principal.LegalEntityID)
	}
	if id := middleware.GetReqID(ctx); id != "" {
		req.Header.Set(middleware.RequestIDHeader, id)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "purchase order get transport failed", "err", err, "purchaseOrderId", id, "correlationId", middleware.GetReqID(ctx))
		return domain.PurchaseOrder{}, fmt.Errorf("purchase order get %s: %w", id, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		slog.ErrorContext(ctx, "purchase order get read failed", "err", err, "purchaseOrderId", id, "correlationId", middleware.GetReqID(ctx))
		return domain.PurchaseOrder{}, fmt.Errorf("purchase order get %s: %w", id, err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var out orderBody
		if err := json.Unmarshal(raw, &out); err != nil || out.ID == "" {
			slog.ErrorContext(ctx, "purchase order response unusable", "err", err, "purchaseOrderId", id, "correlationId", middleware.GetReqID(ctx))
			return domain.PurchaseOrder{}, fmt.Errorf("purchase order get %s: response has no usable order", id)
		}
		return out.toDomain(), nil
	case http.StatusNotFound:
		return domain.PurchaseOrder{}, fmt.Errorf("purchase order get %s: %w", id, domain.ErrPurchaseOrderNotFound)
	}
	err = fmt.Errorf("purchase order get %s: unexpected status %d", id, resp.StatusCode)
	slog.ErrorContext(ctx, "purchase order get rejected", "err", err, "status", resp.StatusCode, "purchaseOrderId", id, "correlationId", middleware.GetReqID(ctx))
	return domain.PurchaseOrder{}, err
}

func (o orderBody) toDomain() domain.PurchaseOrder {
	out := domain.PurchaseOrder{ID: o.ID, VendorID: o.VendorID, Currency: o.Currency, DocumentStatus: o.DocumentStatus, Lines: make([]domain.PurchaseOrderLine, 0, len(o.Lines))}
	for _, l := range o.Lines {
		out.Lines = append(out.Lines, domain.PurchaseOrderLine(l))
	}
	return out
}
