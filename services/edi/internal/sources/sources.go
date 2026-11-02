package sources

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

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
)

const (
	Timeout         = 3 * time.Second
	maxResponseSize = 2 << 20

	audFulfillment = "fulfillment"
	audBilling     = "billing"
	audSales       = "sales"

	DutyShipmentRead = "fulfillment.shipment.read"
	DutyInvoiceRead  = "billing.invoice.read"
	DutyOrderRead    = "sales.order.lookup"
)

var (
	ErrNotFound  = errors.New("source document not found")
	ErrForbidden = errors.New("source service refused the read")
)

type Client struct {
	signer      *tenant.ServiceSigner
	http        *http.Client
	fulfillment string
	billing     string
	sales       string
}

func New(signer *tenant.ServiceSigner, fulfillmentURL, billingURL, salesURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	trim := func(s string) string { return strings.TrimRight(s, "/") }
	return &Client{signer: signer, http: httpClient, fulfillment: trim(fulfillmentURL), billing: trim(billingURL), sales: trim(salesURL)}
}

func (c *Client) Shipment(ctx context.Context, legalEntityID, id string) (domain.SourceShipment, error) {
	var out domain.SourceShipment
	return out, c.get(ctx, &out, audFulfillment, DutyShipmentRead, c.fulfillment, legalEntityID, "/v1/fulfillment/shipments/"+url.PathEscape(id))
}

func (c *Client) Invoice(ctx context.Context, legalEntityID, id string) (domain.SourceInvoice, error) {
	var out domain.SourceInvoice
	return out, c.get(ctx, &out, audBilling, DutyInvoiceRead, c.billing, legalEntityID, "/v1/billing/invoices/"+url.PathEscape(id))
}

func (c *Client) Order(ctx context.Context, legalEntityID, id string) (domain.SourceOrder, error) {
	var out domain.SourceOrder
	return out, c.get(ctx, &out, audSales, DutyOrderRead, c.sales, legalEntityID, "/v1/sales/orders/"+url.PathEscape(id))
}

func (c *Client) get(ctx context.Context, into any, aud, duty, base, legalEntityID, path string) error {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return errors.New("source get: no principal in context")
	}
	if base == "" || c.signer == nil {
		return fmt.Errorf("source get %s: %s is not configured", path, aud)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return fmt.Errorf("source get %s: %w", path, err)
	}
	if _, err := c.signer.ApplyDuties(req, principal, aud, legalEntityID, []string{duty}); err != nil {
		return fmt.Errorf("source get %s: %w: %v", path, ErrForbidden, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "source get transport failed", "err", err, "path", path, "correlationId", middleware.GetReqID(ctx))
		return fmt.Errorf("source get %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		slog.ErrorContext(ctx, "source get read failed", "err", err, "path", path, "correlationId", middleware.GetReqID(ctx))
		return fmt.Errorf("source get %s: %w", path, err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		if err := json.Unmarshal(raw, into); err != nil {
			slog.ErrorContext(ctx, "source document unusable", "err", err, "path", path, "correlationId", middleware.GetReqID(ctx))
			return fmt.Errorf("source get %s: response is not the expected document", path)
		}
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("source get %s: %w", path, ErrNotFound)
	case http.StatusForbidden, http.StatusUnauthorized:
		return fmt.Errorf("source get %s: %w", path, ErrForbidden)
	}
	err = fmt.Errorf("source get %s: unexpected status %d", path, resp.StatusCode)
	slog.ErrorContext(ctx, "source get rejected", "err", err, "status", resp.StatusCode, "correlationId", middleware.GetReqID(ctx))
	return err
}
