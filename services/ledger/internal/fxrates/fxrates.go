package fxrates

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
	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

const (
	Timeout         = 2 * time.Second
	ratesPath       = "/v1/fx/rates"
	audience        = "fx"
	dutyRateLookup  = "fx.rate.lookup"
	maxResponseSize = 1 << 20
)

func cmpOr(preferred, fallback string) string {
	if preferred != "" {
		return preferred
	}
	return fallback
}

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

func New(baseURL string, httpClient *http.Client, signer *tenant.ServiceSigner) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &noRedirect, signer: signer}
}

func (c *Client) Rate(ctx context.Context, legalEntityID, from, to string, date time.Time) (decimal.Decimal, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return decimal.Decimal{}, errors.New("fx: no principal in context")
	}
	query := url.Values{"type": {store.RateTypeSpot}, "from": {from}, "to": {to}, "date": {date.Format(time.DateOnly)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+ratesPath+"?"+query.Encode(), nil)
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
	case http.StatusUnprocessableEntity:
		var problem struct {
			Code        string `json:"code"`
			ServiceCode string `json:"serviceCode"`
			Detail      string `json:"detail"`
		}
		if json.Unmarshal(raw, &problem) != nil {
			return decimal.Decimal{}, errors.New("fx: unreadable rejection")
		}
		code := cmpOr(cmpOr(problem.ServiceCode, problem.Code), store.CodeRateMissing)
		return decimal.Decimal{}, &store.RateRejected{Code: code, Detail: problem.Detail}
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
		slog.ErrorContext(ctx, "fx refused rate lookup", "status", resp.StatusCode, "from", from, "to", to, "correlationId", middleware.GetReqID(ctx))
		return decimal.Decimal{}, &store.RateRefused{Status: resp.StatusCode}
	default:
		slog.ErrorContext(ctx, "fx rejected rate lookup", "status", resp.StatusCode, "from", from, "to", to, "correlationId", middleware.GetReqID(ctx))
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
