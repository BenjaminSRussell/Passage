// Package inventory reads held quantity from the inventory service so a pick
// can refuse stock that a quality order (or other hold) still blocks.
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
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	Timeout         = 3 * time.Second
	audience        = "inventory"
	dutyRead        = "inventory.read"
	onHandPath      = "/v1/inventory/onhand"
	maxResponseSize = 1 << 20
)

// Lot is the physical and held quantity of one product at a warehouse (and
// optional batch). A missing inventory row is the zero value: nothing held.
type Lot struct {
	Physical decimal.Decimal
	Held     decimal.Decimal
}

// Checker returns the lot a movement should test. A nil Checker skips the
// hold check (INVENTORY_URL unset).
type Checker interface {
	Lot(ctx context.Context, legalEntity, product, warehouse, batch string) (Lot, error)
}

// Blocks reports whether qty would move held stock: any positive hold leaves
// only physical − held free to pick, ship or consume.
func Blocks(physical, held, qty decimal.Decimal) bool {
	return held.IsPositive() && qty.GreaterThan(physical.Sub(held))
}

// MapHold is an in-memory Checker for tests. Release (a usage decision) is
// Set with held = 0.
type MapHold struct {
	mu   sync.Mutex
	lots map[string]Lot
}

func (m *MapHold) Set(product string, physical, held decimal.Decimal) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lots == nil {
		m.lots = map[string]Lot{}
	}
	m.lots[product] = Lot{Physical: physical, Held: held}
}

func (m *MapHold) Lot(_ context.Context, _, product, _, _ string) (Lot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lots[product], nil
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
		return nil, errors.New("inventory client needs a service signing key")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient, signer: signer}, nil
}

// FromEnv builds the client from INVENTORY_URL and the service signing key.
// It returns nil, nil when INVENTORY_URL is unset.
func FromEnv(getenv func(string) string) (*Client, error) {
	baseURL := getenv("INVENTORY_URL")
	if baseURL == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("INVENTORY_URL needs PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE")
	}
	return New(baseURL, signer, nil)
}

type onHandBody struct {
	Items []struct {
		PhysicalInvent string  `json:"physicalInvent"`
		HeldPhysical   *string `json:"heldPhysical"`
	} `json:"items"`
	Truncated bool `json:"truncated"`
}

func (c *Client) Lot(ctx context.Context, legalEntity, product, warehouse, batch string) (Lot, error) {
	if product == "" || !ids.Valid(product) {
		return Lot{}, nil
	}
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return Lot{}, errors.New("inventory hold check: no principal in context")
	}
	if !ids.Valid(legalEntity) {
		return Lot{}, errors.New("inventory hold check needs a legal entity")
	}
	query := url.Values{"groupBy": {"product"}, "product": {product}}
	if warehouse != "" {
		query.Set("warehouse", warehouse)
	}
	if batch != "" {
		query.Set("batch", batch)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+onHandPath+"?"+query.Encode(), nil)
	if err != nil {
		return Lot{}, fmt.Errorf("inventory hold check: %w", err)
	}
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntity, []string{dutyRead}); err != nil {
		return Lot{}, fmt.Errorf("inventory hold check: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "inventory hold check failed", "err", err, "correlationId", middleware.GetReqID(ctx))
		return Lot{}, fmt.Errorf("inventory hold check: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return Lot{}, fmt.Errorf("inventory hold check: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		slog.ErrorContext(ctx, "inventory refused the hold check", "status", resp.StatusCode, "correlationId", middleware.GetReqID(ctx))
		return Lot{}, fmt.Errorf("inventory hold check: status %d", resp.StatusCode)
	}
	var body onHandBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return Lot{}, fmt.Errorf("inventory hold check: %w", err)
	}
	if body.Truncated {
		return Lot{}, errors.New("inventory hold check: the answer was truncated")
	}
	var lot Lot
	for _, it := range body.Items {
		physical, err := decimal.NewFromString(it.PhysicalInvent)
		if err != nil {
			return Lot{}, fmt.Errorf("inventory hold check: quantity %q: %w", it.PhysicalInvent, err)
		}
		lot.Physical = lot.Physical.Add(physical)
		if it.HeldPhysical != nil {
			held, err := decimal.NewFromString(*it.HeldPhysical)
			if err != nil {
				return Lot{}, fmt.Errorf("inventory hold check: held %q: %w", *it.HeldPhysical, err)
			}
			lot.Held = lot.Held.Add(held)
		}
	}
	return lot, nil
}
