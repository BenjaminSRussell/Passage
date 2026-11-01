// Package upstream calls the product and production services with a signed
// service token that carries only the narrow duty each call needs.
package upstream

import (
	"bytes"
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
)

const (
	timeout         = 5 * time.Second
	maxResponseSize = 1 << 20

	productAudience    = "product"
	productionAudience = "production"
	dutyConfiguration  = "product.configuration.read"
	dutyCreateFromLine = "production.order.create-from-sales"
)

var (
	ErrConfigurationInvalid = errors.New("saved configuration unknown, not valid or not of the product")
	ErrOriginConflict       = errors.New("the sales order line already has a production order for another product or configuration")
)

type Client struct {
	productURL, productionURL string
	signer                    *tenant.ServiceSigner
	http                      *http.Client
}

func New(productURL, productionURL string, signer *tenant.ServiceSigner) *Client {
	return &Client{productURL: strings.TrimRight(productURL, "/"), productionURL: strings.TrimRight(productionURL, "/"), signer: signer,
		http: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

type ProductionRequest struct {
	LegalEntityID   string `json:"legalEntityId"`
	ProductID       string `json:"productId"`
	SiteID          string `json:"siteId"`
	WarehouseID     string `json:"warehouseId"`
	Currency        string `json:"currency"`
	Quantity        string `json:"quantity"`
	DeliveryDate    string `json:"deliveryDate"`
	OriginID        string `json:"originId"`
	ConfigurationID string `json:"configurationId"`
}

// Check reports ErrConfigurationInvalid unless the product service knows the saved
// configuration, it is valid and its model belongs to productID (or a variant of it).
func (c *Client) Check(ctx context.Context, legalEntityID, configurationID, productID string) error {
	if c == nil || c.productURL == "" {
		return errors.New("product service is not configured")
	}
	target := c.productURL + "/v1/product/saved-configurations/" + url.PathEscape(configurationID) + "?" + url.Values{"productId": {productID}}.Encode()
	status, raw, err := c.do(ctx, http.MethodGet, target, productAudience, dutyConfiguration, legalEntityID, "", nil)
	if err != nil {
		return err
	}
	switch {
	case status == http.StatusNotFound:
		return ErrConfigurationInvalid
	case status != http.StatusOK:
		return fmt.Errorf("product responded with status %d", status)
	}
	var body struct {
		Status           string `json:"status"`
		BelongsToProduct bool   `json:"belongsToProduct"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("decode product response: %w", err)
	}
	if body.Status != "valid" || !body.BelongsToProduct {
		return ErrConfigurationInvalid
	}
	return nil
}

// CreateForLine asks production for the order of one configured sales order line.
// Production answers the same order for a repeat, so created is false then.
func (c *Client) CreateForLine(ctx context.Context, in ProductionRequest) (orderID string, created bool, err error) {
	if c == nil || c.productionURL == "" {
		return "", false, errors.New("production service is not configured")
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return "", false, err
	}
	status, raw, err := c.do(ctx, http.MethodPost, c.productionURL+"/v1/production/sales-line-orders", productionAudience, dutyCreateFromLine,
		in.LegalEntityID, "sales-line-"+in.OriginID, payload)
	if err != nil {
		return "", false, err
	}
	switch status {
	case http.StatusConflict:
		return "", false, ErrOriginConflict
	case http.StatusOK, http.StatusCreated:
		var body struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &body); err != nil || body.ID == "" {
			return "", false, fmt.Errorf("decode production response: %w", err)
		}
		return body.ID, status == http.StatusCreated, nil
	}
	return "", false, fmt.Errorf("production responded with status %d", status)
}

func (c *Client) do(ctx context.Context, method, target, audience, duty, legalEntityID, idempotencyKey string, payload []byte) (int, []byte, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return 0, nil, errors.New("no principal in context")
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	signed, err := c.signer.ApplyDuties(req, p, audience, legalEntityID, []string{duty})
	if err == nil && !signed {
		err = errors.New("no service signer configured")
	}
	if err != nil {
		return 0, nil, fmt.Errorf("sign %s request: %w", audience, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, audience+" request failed", "err", err, "correlationId", middleware.GetReqID(ctx))
		return 0, nil, fmt.Errorf("%s request: %w", audience, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return 0, nil, fmt.Errorf("read %s response: %w", audience, err)
	}
	return resp.StatusCode, raw, nil
}
