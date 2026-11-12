// Package taxclient asks the tax service what a cross-border intercompany charge carries and records the transaction once the charge
// is posted. Calls are signed with the service identity and carry only the duty of the call.
package taxclient

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
	// Timeout bounds one call unless the caller passes its own HTTP client.
	Timeout = 3 * time.Second

	audience         = "tax"
	dutyCalculate    = "tax.tax.calculate"
	dutyRecord       = "tax.transaction.record"
	calculatePath    = "/v1/tax/calculate"
	transactionsPath = "/v1/tax/transactions"
	maxResponseSize  = 1 << 20
)

// Input is one charge to price: the lender's legal entity supplies, the borrower's country and tax group are the customer.
type Input struct {
	LegalEntityID string
	Currency      string
	Date          time.Time
	Country       string
	TaxGroup      string
	Amount        decimal.Decimal
	Ref           string
}

// Result is the tax of the charge in its currency.
type Result struct {
	Amount decimal.Decimal
	Code   string
}

// Tax is what the API needs of the tax service. Client implements it.
type Tax interface {
	Calculate(ctx context.Context, in Input) (Result, error)
	Record(ctx context.Context, legalEntityID, id string, date time.Time, r Result) error
}

// ErrUnavailable is returned for any answer that is not a usable tax amount.
var ErrUnavailable = errors.New("taxclient: the tax service did not give a usable answer")

// Client calls the tax service.
type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

// New returns a client; signer is required.
func New(baseURL string, signer *tenant.ServiceSigner, httpClient *http.Client) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("taxclient: a base URL is required")
	}
	if signer == nil {
		return nil, errors.New("taxclient: a service signing key is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &noRedirect, signer: signer}, nil
}

// FromEnv builds the client from TAX_URL, TAX_TIMEOUT and the service signing key variables; nil, nil when TAX_URL is unset.
func FromEnv(getenv func(string) string) (*Client, error) {
	baseURL := getenv("TAX_URL")
	if baseURL == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("taxclient: TAX_URL needs the service signing key")
	}
	timeout := Timeout
	if v, err := time.ParseDuration(getenv("TAX_TIMEOUT")); err == nil && v > 0 {
		timeout = v
	}
	return New(baseURL, signer, &http.Client{Timeout: timeout})
}

type party struct {
	TaxGroup string `json:"tax_group,omitempty"`
	Country  string `json:"country"`
}

type line struct {
	LineRef string `json:"line_ref"`
	Amount  string `json:"amount"`
}

type calculateBody struct {
	Currency     string `json:"currency"`
	Direction    string `json:"direction"`
	DocumentDate string `json:"document_date"`
	TaxDate      string `json:"tax_date"`
	StoredConfig bool   `json:"stored_config"`
	Party        party  `json:"party"`
	Lines        []line `json:"lines"`
}

// Calculate asks for the output tax of a charge with the tenant's stored tax configuration.
func (c *Client) Calculate(ctx context.Context, in Input) (Result, error) {
	date := in.Date.Format(time.DateOnly)
	payload, err := json.Marshal(calculateBody{Currency: in.Currency, Direction: "output", DocumentDate: date, TaxDate: date, StoredConfig: true,
		Party: party{TaxGroup: in.TaxGroup, Country: in.Country}, Lines: []line{{LineRef: "charge", Amount: in.Amount.String()}}})
	if err != nil {
		return Result{}, fmt.Errorf("taxclient: %w", err)
	}
	raw, err := c.do(ctx, in.LegalEntityID, calculatePath, payload, dutyCalculate)
	if err != nil {
		return Result{}, err
	}
	var res struct {
		Rows []struct {
			Code      string `json:"code"`
			TaxAmount string `json:"tax_amount"`
		} `json:"document_taxes_by_code"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return Result{}, ErrUnavailable
	}
	out := Result{}
	for _, row := range res.Rows {
		v, err := decimal.NewFromString(row.TaxAmount)
		if err != nil || v.IsNegative() {
			return Result{}, ErrUnavailable
		}
		out.Amount = out.Amount.Add(v)
		if out.Code == "" {
			out.Code = row.Code
		} else if row.Code != out.Code {
			out.Code += "+" + row.Code
		}
	}
	return out, nil
}

// Record gives the tax service the transaction of a posted charge; the id makes a repeat harmless.
func (c *Client) Record(ctx context.Context, legalEntityID, id string, date time.Time, r Result) error {
	payload, err := json.Marshal(map[string]any{"transactions": []map[string]string{{
		"id": id, "code": r.Code, "direction": "output", "tax_date": date.Format(time.DateOnly), "amount": r.Amount.String()}}})
	if err != nil {
		return fmt.Errorf("taxclient: %w", err)
	}
	_, err = c.do(ctx, legalEntityID, transactionsPath, payload, dutyRecord)
	return err
}

func (c *Client) do(ctx context.Context, legalEntityID, path string, payload []byte, duty string) ([]byte, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return nil, errors.New("taxclient: no principal in context")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("taxclient: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{duty}); err != nil {
		return nil, fmt.Errorf("taxclient: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "tax request failed", "err", err, "path", path, "correlationId", middleware.GetReqID(ctx))
		return nil, fmt.Errorf("taxclient: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("taxclient: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.ErrorContext(ctx, "tax rejected the request", "status", resp.StatusCode, "path", path, "correlationId", middleware.GetReqID(ctx))
		return nil, fmt.Errorf("taxclient: unexpected status %d", resp.StatusCode)
	}
	return raw, nil
}

// Fake is a Tax for tests: a flat rate per country, and the transactions it was given.
type Fake struct {
	RatePct   map[string]decimal.Decimal // by customer country
	Fail      error
	Recorded  []string
	FailWrite error
}

// Calculate implements Tax.
func (f *Fake) Calculate(_ context.Context, in Input) (Result, error) {
	if f.Fail != nil {
		return Result{}, f.Fail
	}
	rate := f.RatePct[in.Country]
	return Result{Amount: in.Amount.Mul(rate).Div(decimal.NewFromInt(100)).Round(2), Code: "X-" + in.Country}, nil
}

// Record implements Tax.
func (f *Fake) Record(_ context.Context, _, id string, _ time.Time, _ Result) error {
	if f.FailWrite != nil {
		return f.FailWrite
	}
	f.Recorded = append(f.Recorded, id)
	return nil
}
