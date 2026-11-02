// Package inventory reads physical on-hand quantities from the inventory
// service for an activation revaluation (BR-39.8). It signs a service token
// restricted to the inventory.read duty; the end user's headers are not
// forwarded.
package inventory

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
)

const (
	Timeout         = 10 * time.Second
	audience        = "inventory"
	dutyRead        = "inventory.read"
	onHandPath      = "/v1/inventory/onhand"
	maxResponseSize = 64 << 20
)

// Position is one item at one site.
type Position struct {
	ItemID string
	SiteID string
}

// Reader returns the physical quantity per item and site of one legal entity.
// asOf is a date (YYYY-MM-DD) or empty for the live projection.
type Reader interface {
	OnHand(ctx context.Context, legalEntityID, asOf string) (map[Position]decimal.Decimal, error)
}

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

func New(baseURL string, signer *tenant.ServiceSigner, httpClient *http.Client) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("inventory client needs a base URL")
	}
	if signer == nil {
		return nil, errors.New("inventory client needs a service signing key: the end user's headers are never forwarded")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &noRedirect, signer: signer}, nil
}

// FromEnv builds the client from INVENTORY_URL and the service signing key
// variables. It returns nil, nil when INVENTORY_URL is unset and an error when
// it is set without a signing key.
func FromEnv(getenv func(string) string) (*Client, error) {
	baseURL := getenv("INVENTORY_URL")
	if baseURL == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	return New(baseURL, signer, nil)
}

type onHandBody struct {
	Items []struct {
		Group          map[string]string `json:"group"`
		PhysicalInvent string            `json:"physicalInvent"`
	} `json:"items"`
	Truncated bool `json:"truncated"`
}

func (c *Client) OnHand(ctx context.Context, legalEntityID, asOf string) (map[Position]decimal.Decimal, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return nil, errors.New("inventory on-hand: no principal in context")
	}
	query := url.Values{"groupBy": {"product,site,legal_entity"}}
	if asOf != "" {
		query.Set("asOf", asOf)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+onHandPath+"?"+query.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("inventory on-hand: %w", err)
	}
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{dutyRead}); err != nil {
		return nil, fmt.Errorf("inventory on-hand: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "inventory on-hand request failed", "err", err, "correlationId", middleware.GetReqID(ctx))
		return nil, fmt.Errorf("inventory on-hand: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		slog.ErrorContext(ctx, "inventory on-hand response unreadable", "err", err, "correlationId", middleware.GetReqID(ctx))
		return nil, fmt.Errorf("inventory on-hand: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		slog.ErrorContext(ctx, "inventory refused the on-hand read", "status", resp.StatusCode, "correlationId", middleware.GetReqID(ctx))
		return nil, fmt.Errorf("inventory on-hand: status %d", resp.StatusCode)
	}
	var body onHandBody
	if err := json.Unmarshal(raw, &body); err != nil {
		slog.ErrorContext(ctx, "inventory on-hand response unusable", "err", err, "correlationId", middleware.GetReqID(ctx))
		return nil, fmt.Errorf("inventory on-hand: %w", err)
	}
	if body.Truncated {
		return nil, errors.New("inventory on-hand: the answer was truncated; the quantities would be incomplete")
	}
	out := map[Position]decimal.Decimal{}
	for _, it := range body.Items {
		if it.Group["legal_entity"] != legalEntityID {
			continue
		}
		qty, err := decimal.NewFromString(it.PhysicalInvent)
		if err != nil {
			return nil, fmt.Errorf("inventory on-hand: quantity %q: %w", it.PhysicalInvent, err)
		}
		pos := Position{ItemID: it.Group["product"], SiteID: it.Group["site"]}
		out[pos] = out[pos].Add(qty)
	}
	return out, nil
}
