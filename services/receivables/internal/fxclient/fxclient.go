// Package fxclient reads exchange rates from the fx service for the realized-FX journal of a cross-currency settlement.
package fxclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	timeout         = 3 * time.Second
	audience        = "fx"
	dutyRateLookup  = "fx.rate.lookup"
	ratesPath       = "/v1/fx/rates"
	rateTypeSpot    = "SPOT"
	maxResponseSize = 1 << 20
)

// ErrUnsigned is returned when FX_URL is set without a service signing key.
var ErrUnsigned = errors.New("fx client needs the service signing key")

// ErrNoRate is returned when the fx service has no rate for the pair and date (or refuses the lookup).
var ErrNoRate = errors.New("fx: no rate for the currency pair and date")

// Rates converts one currency into another at the spot rate of a day.
type Rates interface {
	Rate(ctx context.Context, legalEntityID, from, to string, date time.Time) (decimal.Decimal, error)
}

// Client is the HTTP Rates, signed as the receivables service.
type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

// FromEnv builds the client from FX_URL; without it there is no client and cross-currency settlement cannot be priced.
func FromEnv(getenv func(string) string) (*Client, error) {
	baseURL := getenv("FX_URL")
	if baseURL == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, ErrUnsigned
	}
	d := timeout
	if v, err := time.ParseDuration(getenv("FX_TIMEOUT")); err == nil && v > 0 {
		d = v
	}
	noRedirect := http.Client{Timeout: d, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &noRedirect, signer: signer}, nil
}

// New builds a client over an http client and signer (tests).
func New(baseURL string, h *http.Client, signer *tenant.ServiceSigner) *Client {
	if h == nil {
		h = &http.Client{Timeout: timeout}
	}
	noRedirect := *h
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &noRedirect, signer: signer}
}

// Rate returns the spot rate that converts one unit of from into to on date. The same currency is 1 without a call.
func (c *Client) Rate(ctx context.Context, legalEntityID, from, to string, date time.Time) (decimal.Decimal, error) {
	if from == to {
		return decimal.NewFromInt(1), nil
	}
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return decimal.Decimal{}, errors.New("fx: no principal in context")
	}
	q := url.Values{"type": {rateTypeSpot}, "from": {from}, "to": {to}, "date": {date.Format(time.DateOnly)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+ratesPath+"?"+q.Encode(), nil)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("fx: %w", err)
	}
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{dutyRateLookup}); err != nil {
		return decimal.Decimal{}, fmt.Errorf("fx: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("fx: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("fx: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnprocessableEntity, http.StatusNotFound:
		return decimal.Decimal{}, ErrNoRate
	default:
		return decimal.Decimal{}, fmt.Errorf("fx: unexpected status %d", resp.StatusCode)
	}
	var body struct {
		Rate string `json:"rate"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return decimal.Decimal{}, errors.New("fx: response has no usable rate")
	}
	rate, err := decimal.NewFromString(body.Rate)
	if err != nil || !rate.IsPositive() {
		return decimal.Decimal{}, errors.New("fx: response has no usable rate")
	}
	return rate, nil
}
