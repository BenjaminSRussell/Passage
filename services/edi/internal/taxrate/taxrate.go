// Package taxrate asks the tax service for the VAT rate a self-billing customer's deliveries carry (#35, 35-5), so a self-billed
// line's tax is checked at the rate the tax engine determines on the document date instead of a rate typed into the partner. The
// call is POST /v1/tax/calculate with the tenant's stored configuration, signed with edi's service key and only the duty
// tax.tax.calculate (grant edi -> tax).
package taxrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	// Timeout bounds one call unless the caller passes its own HTTP client.
	Timeout       = 3 * time.Second
	audience      = "tax"
	DutyCalculate = "tax.tax.calculate"
	calculatePath = "/v1/tax/calculate"
	maxResponse   = 1 << 20
)

// base is the amount the rate is read from: the tax on 10000 is the rate in percent with two more decimals than a 100 base gives.
var base = decimal.NewFromInt(10000)

// Query names the deliveries: the selling legal entity, the document currency and date, and the customer's tax country, tax group
// and the item tax group of what was delivered.
type Query struct {
	LegalEntityID string
	Currency      string
	Date          time.Time
	Country       string
	TaxGroup      string
	ItemTaxGroup  string
}

// ErrUnavailable is any answer that is not a usable tax amount.
var ErrUnavailable = errors.New("taxrate: the tax service did not give a usable answer")

// ErrNoDetermination is an answer that determined no tax: no tax code row, or the provider's fallback. It is not a 0% rate; the
// caller keeps the partner's rate.
var ErrNoDetermination = fmt.Errorf("%w: the tax service determined no tax for the deliveries", ErrUnavailable)

// Client calls the tax service.
type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

// New returns a client; signer is required.
func New(baseURL string, signer *tenant.ServiceSigner, httpClient *http.Client) (*Client, error) {
	if baseURL == "" || signer == nil {
		return nil, errors.New("taxrate: a base URL and the service signing key are required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &noRedirect, signer: signer}, nil
}

type calculateBody struct {
	Currency     string `json:"currency"`
	Direction    string `json:"direction"`
	DocumentDate string `json:"document_date"`
	TaxDate      string `json:"tax_date"`
	StoredConfig bool   `json:"stored_config"`
	Party        struct {
		TaxGroup string `json:"tax_group,omitempty"`
		Country  string `json:"country"`
	} `json:"party"`
	Lines []struct {
		LineRef      string `json:"line_ref"`
		Amount       string `json:"amount"`
		ItemTaxGroup string `json:"item_tax_group,omitempty"`
	} `json:"lines"`
}

// RatePct is the output VAT rate, in percent, the tax engine applies to the deliveries.
func (c *Client) RatePct(ctx context.Context, q Query) (decimal.Decimal, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return decimal.Zero, errors.New("taxrate: no principal in context")
	}
	var body calculateBody
	date := q.Date.Format(time.DateOnly)
	body.Currency, body.Direction, body.DocumentDate, body.TaxDate, body.StoredConfig = q.Currency, "output", date, date, true
	body.Party.TaxGroup, body.Party.Country = q.TaxGroup, q.Country
	body.Lines = append(body.Lines, struct {
		LineRef      string `json:"line_ref"`
		Amount       string `json:"amount"`
		ItemTaxGroup string `json:"item_tax_group,omitempty"`
	}{LineRef: "rate", Amount: base.String(), ItemTaxGroup: q.ItemTaxGroup})
	payload, err := json.Marshal(body)
	if err != nil {
		return decimal.Zero, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+calculatePath, bytes.NewReader(payload))
	if err != nil {
		return decimal.Zero, err
	}
	req.Header.Set("Content-Type", "application/json")
	if _, err := c.signer.ApplyDuties(req, principal, audience, q.LegalEntityID, []string{DutyCalculate}); err != nil {
		return decimal.Zero, fmt.Errorf("taxrate: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return decimal.Zero, fmt.Errorf("taxrate: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return decimal.Zero, fmt.Errorf("taxrate: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return decimal.Zero, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	return parseRate(raw)
}

func parseRate(raw []byte) (decimal.Decimal, error) {
	var res struct {
		Fallback bool `json:"fallback"`
		Rows     []struct {
			TaxAmount string `json:"tax_amount"`
		} `json:"document_taxes_by_code"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return decimal.Zero, ErrUnavailable
	}
	if res.Fallback || len(res.Rows) == 0 {
		return decimal.Zero, ErrNoDetermination
	}
	total := decimal.Zero
	for _, row := range res.Rows {
		v, err := decimal.NewFromString(row.TaxAmount)
		if err != nil || v.IsNegative() {
			return decimal.Zero, ErrUnavailable
		}
		total = total.Add(v)
	}
	pct := total.Mul(decimal.NewFromInt(100)).Div(base)
	if pct.GreaterThan(decimal.NewFromInt(100)) {
		return decimal.Zero, ErrUnavailable
	}
	return pct, nil
}
