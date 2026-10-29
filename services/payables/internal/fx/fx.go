package fx

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
	"github.com/BenjaminSRussell/passage/services/payables/internal/identity"
)

const (
	Timeout         = 2 * time.Second
	DefaultRateType = "spot"
	ratesPath       = "/v1/fx/rates"
	maxResponseSize = 1 << 20
)

var ErrRateUnavailable = errors.New("fx: no rate for the pair and date")

type StatusError struct{ Status int }

func (e *StatusError) Error() string { return fmt.Sprintf("fx: unexpected status %d", e.Status) }

type Client struct {
	baseURL  string
	rateType string
	http     *http.Client
}

func New(baseURL, rateType string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	if rateType == "" {
		rateType = DefaultRateType
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), rateType: rateType, http: httpClient}
}

func (c *Client) Rate(ctx context.Context, from, to string, date time.Time) (decimal.Decimal, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return decimal.Decimal{}, errors.New("fx: no principal in context")
	}
	q := url.Values{"type": {c.rateType}, "from": {from}, "to": {to}, "date": {date.Format(time.DateOnly)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+ratesPath+"?"+q.Encode(), nil)
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("fx: %w", err)
	}
	identity.Forward(req, principal)
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "fx request failed", "err", err, "from", from, "to", to, "correlationId", middleware.GetReqID(ctx))
		return decimal.Decimal{}, fmt.Errorf("fx: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		slog.ErrorContext(ctx, "fx read failed", "err", err, "from", from, "to", to, "correlationId", middleware.GetReqID(ctx))
		return decimal.Decimal{}, fmt.Errorf("fx: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnprocessableEntity:
		return decimal.Decimal{}, fmt.Errorf("%w: %s/%s on %s", ErrRateUnavailable, from, to, q.Get("date"))
	default:
		slog.ErrorContext(ctx, "fx rejected", "status", resp.StatusCode, "from", from, "to", to, "correlationId", middleware.GetReqID(ctx))
		return decimal.Decimal{}, &StatusError{Status: resp.StatusCode}
	}
	var body struct {
		Rate string `json:"rate"`
	}
	rate, err := decimal.NewFromString("")
	if jerr := json.Unmarshal(raw, &body); jerr == nil {
		rate, err = decimal.NewFromString(body.Rate)
	}
	if err != nil || !rate.IsPositive() {
		slog.ErrorContext(ctx, "fx rate response unusable", "from", from, "to", to, "correlationId", middleware.GetReqID(ctx))
		return decimal.Decimal{}, errors.New("fx: response has no usable rate")
	}
	return rate, nil
}
