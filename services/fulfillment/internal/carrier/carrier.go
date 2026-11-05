// Package carrier is fulfillment's client of carrier-gateway (services/carrier-gateway-ts): rate shopping, label
// purchase and tracking (wave 28 D3, #482). Every call carries a carrier-gateway service token minted for the
// shipment's legal entity on behalf of the person at the packing station, holding only the duty the call needs.
package carrier

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
	Timeout         = 10 * time.Second
	audience        = "carrier-gateway"
	DutyRate        = "carrier.rate.quote"
	DutyLabel       = "carrier.label.create"
	DutyTrack       = "carrier.tracking.read"
	maxResponseSize = 4 << 20
)

type Address struct {
	Name       string `json:"name,omitempty"`
	Street1    string `json:"street1"`
	City       string `json:"city"`
	PostalCode string `json:"postalCode"`
	Country    string `json:"country"`
	State      string `json:"state,omitempty"`
}

type Parcel struct {
	WeightKg string `json:"weightKg"`
	LengthCm string `json:"lengthCm"`
	WidthCm  string `json:"widthCm"`
	HeightCm string `json:"heightCm"`
}

// Shipment is what the carrier is asked to move: the gateway validates it (CGW-0400 names the field).
type Shipment struct {
	ShipFrom Address  `json:"shipFrom"`
	ShipTo   Address  `json:"shipTo"`
	Parcels  []Parcel `json:"parcels"`
}

type Rate struct {
	Carrier          string `json:"carrier"`
	Service          string `json:"service"`
	Amount           string `json:"amount"`
	Currency         string `json:"currency"`
	TransitDays      int    `json:"transitDays"`
	BillableWeightKg string `json:"billableWeightKg"`
}

type Failure struct {
	Carrier string `json:"carrier"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Rates struct {
	Items    []Rate    `json:"items"`
	Failures []Failure `json:"failures"`
}

type Label struct {
	Carrier        string    `json:"carrier"`
	Service        string    `json:"service"`
	TrackingNumber string    `json:"trackingNumber"`
	Amount         string    `json:"amount"`
	Currency       string    `json:"currency"`
	Format         string    `json:"format"`
	Document       string    `json:"document"`
	CreatedAt      time.Time `json:"createdAt"`
	// Created is false when the gateway returned the label an earlier request with the same key bought.
	Created bool `json:"created"`
}

type TrackingEvent struct {
	EventKey    string    `json:"eventKey"`
	Status      string    `json:"status"`
	Description string    `json:"description"`
	EventTime   time.Time `json:"eventTime"`
}

type Tracking struct {
	Carrier        string          `json:"carrier"`
	TrackingNumber string          `json:"trackingNumber"`
	Status         string          `json:"status"`
	Events         []TrackingEvent `json:"events"`
}

// RateQuery narrows a rate shop to some carriers and services (all when empty).
type RateQuery struct {
	Carriers []string `json:"carriers,omitempty"`
	Services []string `json:"services,omitempty"`
	Shipment
}

type LabelOrder struct {
	Carrier   string `json:"carrier"`
	Service   string `json:"service"`
	Reference string `json:"reference,omitempty"`
	Shipment
}

// Gateway is carrier-gateway as fulfillment uses it. A nil Gateway means CARRIER_GATEWAY_URL is unset.
type Gateway interface {
	Rates(ctx context.Context, legalEntity string, q RateQuery) (Rates, error)
	// Label buys one label per key: a repeat with the same key and order returns the same label.
	Label(ctx context.Context, legalEntity, key string, o LabelOrder) (Label, error)
	Track(ctx context.Context, legalEntity, carrier, trackingNumber string) (Tracking, error)
}

// Error is a refusal from the gateway (a problem response); Status and Code are the gateway's.
type Error struct {
	Status int               `json:"status"`
	Code   string            `json:"code"`
	Detail string            `json:"detail"`
	Errors map[string]string `json:"errors,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("carrier-gateway %s (%d): %s", e.Code, e.Status, e.Detail)
}

// ErrUnavailable wraps a transport failure or a gateway 5xx: retryable.
var ErrUnavailable = errors.New("carrier-gateway is unavailable")

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

func New(baseURL string, signer *tenant.ServiceSigner, httpClient *http.Client) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("carrier-gateway client needs a base URL")
	}
	if signer == nil {
		return nil, errors.New("carrier-gateway client needs a service signing key")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient, signer: signer}, nil
}

// FromEnv builds the client from CARRIER_GATEWAY_URL and the service signing key. It returns a nil Gateway (an
// untyped nil, safe to compare) when CARRIER_GATEWAY_URL is unset.
func FromEnv(getenv func(string) string) (Gateway, error) {
	baseURL := getenv("CARRIER_GATEWAY_URL")
	if baseURL == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("CARRIER_GATEWAY_URL needs PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE")
	}
	c, err := New(baseURL, signer, nil)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) Rates(ctx context.Context, legalEntity string, q RateQuery) (Rates, error) {
	var out Rates
	return out, c.call(ctx, http.MethodPost, "/v1/carrier/rates", legalEntity, DutyRate, "", q, &out)
}

func (c *Client) Label(ctx context.Context, legalEntity, key string, o LabelOrder) (Label, error) {
	var out Label
	return out, c.call(ctx, http.MethodPost, "/v1/carrier/labels", legalEntity, DutyLabel, key, o, &out)
}

func (c *Client) Track(ctx context.Context, legalEntity, carrier, trackingNumber string) (Tracking, error) {
	var out Tracking
	path := "/v1/carrier/tracking/" + url.PathEscape(carrier) + "/" + url.PathEscape(trackingNumber)
	return out, c.call(ctx, http.MethodGet, path, legalEntity, DutyTrack, "", nil, &out)
}

func (c *Client) call(ctx context.Context, method, path, legalEntity, duty, key string, body, out any) error {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return errors.New("carrier-gateway: no principal in context")
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("carrier-gateway: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntity, []string{duty}); err != nil {
		return fmt.Errorf("carrier-gateway: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "carrier-gateway call failed", "err", err, "path", path, "correlationId", middleware.GetReqID(ctx))
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if resp.StatusCode >= 500 {
		slog.ErrorContext(ctx, "carrier-gateway answered an error", "status", resp.StatusCode, "path", path, "correlationId", middleware.GetReqID(ctx))
		return fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		p := &Error{Status: resp.StatusCode}
		if json.Unmarshal(raw, p) != nil || p.Code == "" {
			p.Code, p.Detail = "CGW-UNKNOWN", http.StatusText(resp.StatusCode)
		}
		p.Status = resp.StatusCode
		return p
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: undecodable response: %v", ErrUnavailable, err)
	}
	return nil
}
