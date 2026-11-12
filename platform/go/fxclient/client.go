// Package fxclient reads exchange rates from the fx service's rate tables on behalf of a calling service. The request is signed
// with the service identity and carries only the rate-lookup duty, so the end user's headers are never forwarded.
package fxclient

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
	// Timeout bounds one lookup unless the caller passes its own HTTP client.
	Timeout = 2 * time.Second

	// DutyRateLookup is the service-only duty fx accepts on GET /v1/fx/rates.
	DutyRateLookup = "fx.rate.lookup"

	audience        = "fx"
	ratesPath       = "/v1/fx/rates"
	maxResponseSize = 1 << 20
)

// ErrRateMissing is returned when the rate tables hold no rate for the type, pair and date (fx answers 422).
var ErrRateMissing = errors.New("fxclient: no rate for the type, pair and date")

// Error is any other non-success answer of the fx service.
type Error struct{ Status int }

func (e *Error) Error() string { return fmt.Sprintf("fxclient: unexpected status %d", e.Status) }

// Rater resolves a rate. Client and Fake implement it.
type Rater interface {
	// Rate returns units of `to` per unit of `from` of the given rate type valid on date.
	Rate(ctx context.Context, legalEntityID, rateType, from, to string, date time.Time) (decimal.Decimal, error)
}

// Client calls the fx service.
type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

// New returns a client; signer is required.
func New(baseURL string, signer *tenant.ServiceSigner, httpClient *http.Client) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("fxclient: a base URL is required")
	}
	if signer == nil {
		return nil, errors.New("fxclient: a service signing key is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &noRedirect, signer: signer}, nil
}

// FromEnv builds the client from FX_URL, FX_TIMEOUT and the service signing key variables; it returns nil, nil when FX_URL is unset
// and an error when it is set without a signing key.
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
		return nil, errors.New("fxclient: FX_URL needs the service signing key")
	}
	timeout := Timeout
	if v, err := time.ParseDuration(getenv("FX_TIMEOUT")); err == nil && v > 0 {
		timeout = v
	}
	return New(baseURL, signer, &http.Client{Timeout: timeout})
}

// Rate implements Rater.
func (c *Client) Rate(ctx context.Context, legalEntityID, rateType, from, to string, date time.Time) (decimal.Decimal, error) {
	if from == to {
		return decimal.NewFromInt(1), nil
	}
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return decimal.Decimal{}, errors.New("fxclient: no principal in context")
	}
	q := url.Values{"type": {rateType}, "from": {from}, "to": {to}, "date": {date.Format(time.DateOnly)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+ratesPath+"?"+q.Encode(), nil)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("fxclient: %w", err)
	}
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{DutyRateLookup}); err != nil {
		return decimal.Decimal{}, fmt.Errorf("fxclient: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("fxclient: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("fxclient: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnprocessableEntity:
		return decimal.Decimal{}, fmt.Errorf("%w: %s %s to %s on %s", ErrRateMissing, rateType, from, to, date.Format(time.DateOnly))
	default:
		slog.ErrorContext(ctx, "fx refused rate lookup", "status", resp.StatusCode, "type", rateType, "from", from, "to", to, "correlationId", middleware.GetReqID(ctx))
		return decimal.Decimal{}, &Error{Status: resp.StatusCode}
	}
	var body struct {
		Rate string `json:"rate"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return decimal.Decimal{}, errors.New("fxclient: response has no usable rate")
	}
	rate, err := decimal.NewFromString(body.Rate)
	if err != nil || !rate.IsPositive() {
		return decimal.Decimal{}, errors.New("fxclient: response has no usable rate")
	}
	return rate, nil
}

// Fake is an in-memory Rater for tests, keyed by "type|from|to".
type Fake map[string]decimal.Decimal

// Set stores a rate.
func (f Fake) Set(rateType, from, to string, rate decimal.Decimal) {
	f[rateType+"|"+from+"|"+to] = rate
}

// Rate implements Rater.
func (f Fake) Rate(_ context.Context, _ string, rateType, from, to string, _ time.Time) (decimal.Decimal, error) {
	if from == to {
		return decimal.NewFromInt(1), nil
	}
	if r, ok := f[rateType+"|"+from+"|"+to]; ok {
		return r, nil
	}
	return decimal.Decimal{}, ErrRateMissing
}
