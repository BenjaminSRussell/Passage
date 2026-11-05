package tax

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	Timeout         = 3 * time.Second
	audience        = "tax"
	dutyWithholding = "tax.withholding.calculate"
	withholdingPath = "/v1/tax/withholding/calculate"
	maxResponseSize = 1 << 20
)

var (
	ErrConfig   = errors.New("tax: the service identity cannot be used for this request")
	ErrUnsigned = errors.New("tax: withholding needs a service signing key")
)

type StatusError struct{ Status int }

func (e *StatusError) Error() string { return fmt.Sprintf("tax: unexpected status %d", e.Status) }

type RuleError struct{ Code string }

func (e *RuleError) Error() string { return "tax: withholding rule rejected the request: " + e.Code }

type Rate struct {
	ValidFrom string  `json:"valid_from"`
	ValidTo   string  `json:"valid_to,omitempty"`
	RatePct   string  `json:"rate_pct"`
	Threshold *string `json:"threshold,omitempty"`
}

type Code struct {
	Code         string `json:"code"`
	Jurisdiction string `json:"jurisdiction"`
	IncomeType   string `json:"income_type"`
	Rates        []Rate `json:"rates"`
}

type Request struct {
	Currency    string
	PaymentDate string
	Code        Code
	Gross       decimal.Decimal
	PaidToDate  decimal.Decimal
}

type requestBody struct {
	Currency    string `json:"currency"`
	PartyType   string `json:"party_type"`
	PaymentDate string `json:"payment_date"`
	Code        string `json:"code"`
	GrossAmount string `json:"gross_amount"`
	PaidToDate  string `json:"paid_to_date"`
	Codes       []Code `json:"codes"`
}

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &noRedirect}
}

func (c *Client) WithSigner(s *tenant.ServiceSigner) *Client {
	c.signer = s
	return c
}

func (c *Client) Signed() bool { return c.signer != nil }

func (c *Client) Withholding(ctx context.Context, legalEntityID string, in Request) (decimal.Decimal, error) {
	if c.signer == nil {
		return decimal.Zero, ErrUnsigned
	}
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return decimal.Zero, fmt.Errorf("%w: no principal in context", ErrConfig)
	}
	payload, err := json.Marshal(requestBody{
		Currency: in.Currency, PartyType: "vendor", PaymentDate: in.PaymentDate, Code: in.Code.Code,
		GrossAmount: in.Gross.String(), PaidToDate: in.PaidToDate.String(), Codes: []Code{in.Code},
	})
	if err != nil {
		return decimal.Zero, fmt.Errorf("tax: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+withholdingPath, bytes.NewReader(payload))
	if err != nil {
		return decimal.Zero, fmt.Errorf("tax: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{dutyWithholding}); err != nil {
		return decimal.Zero, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "tax request failed", "err", err, "code", in.Code.Code, "correlationId", middleware.GetReqID(ctx))
		return decimal.Zero, fmt.Errorf("tax: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		slog.ErrorContext(ctx, "tax read failed", "err", err, "code", in.Code.Code, "correlationId", middleware.GetReqID(ctx))
		return decimal.Zero, fmt.Errorf("tax: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnprocessableEntity:
		var problem struct {
			ServiceCode string `json:"serviceCode"`
			Code        string `json:"code"`
		}
		_ = json.Unmarshal(raw, &problem)
		return decimal.Zero, &RuleError{Code: firstNonEmpty(problem.ServiceCode, problem.Code)}
	default:
		slog.ErrorContext(ctx, "tax rejected the withholding request", "status", resp.StatusCode, "code", in.Code.Code, "correlationId", middleware.GetReqID(ctx))
		return decimal.Zero, &StatusError{Status: resp.StatusCode}
	}
	var body struct {
		Withheld string `json:"withholding_amount"`
	}
	err = json.Unmarshal(raw, &body)
	var withheld decimal.Decimal
	if err == nil {
		withheld, err = decimal.NewFromString(body.Withheld)
	}
	if err != nil || withheld.IsNegative() || withheld.GreaterThan(in.Gross) {
		slog.ErrorContext(ctx, "tax withholding response unusable", "code", in.Code.Code, "correlationId", middleware.GetReqID(ctx))
		return decimal.Zero, errors.New("tax: response has no usable withholding amount")
	}
	return withheld, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
