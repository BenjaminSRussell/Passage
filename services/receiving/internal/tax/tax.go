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
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/identity"
)

const (
	Timeout          = 3 * time.Second
	audience         = "tax"
	dutyCalculate    = "tax.tax.calculate"
	dutyRecord       = "tax.transaction.record"
	calculatePath    = "/v1/tax/calculate"
	transactionsPath = "/v1/tax/transactions"
	maxResponseSize  = 1 << 20
)

var (
	ErrUnsigned = errors.New("tax: the call needs a service signing key")
	ErrConfig   = errors.New("tax: the service identity cannot be used for this request")
)

type StatusError struct{ Status int }

func (e *StatusError) Error() string { return fmt.Sprintf("tax: unexpected status %d", e.Status) }

type Rule struct {
	Code         string
	Jurisdiction string
	RatePct      decimal.Decimal
}

type Config struct {
	Rules     map[string]Rule
	Group     string
	ItemGroup string
	Rounding  string
}

func (c Config) Ready() bool { return len(c.Rules) > 0 }

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
	config  Config
}

func New(baseURL string, cfg Config, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if cfg.Rounding == "" {
		cfg.Rounding = "per_document"
	}
	if cfg.Group == "" {
		cfg.Group = "G"
	}
	if cfg.ItemGroup == "" {
		cfg.ItemGroup = "I"
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &noRedirect, config: cfg}
}

func (c *Client) WithSigner(s *tenant.ServiceSigner) *Client {
	c.signer = s
	return c
}

type calculateBody struct {
	Currency     string       `json:"currency"`
	Direction    string       `json:"direction"`
	DocumentDate string       `json:"document_date"`
	TaxDate      string       `json:"tax_date"`
	Party        partyBody    `json:"party"`
	Lines        []lineBody   `json:"lines"`
	Config       calculateCfg `json:"config"`
}

type partyBody struct {
	TaxGroup string `json:"tax_group"`
	Country  string `json:"country,omitempty"`
}

type lineBody struct {
	LineRef          string `json:"line_ref"`
	Amount           string `json:"amount"`
	ItemTaxGroup     string `json:"item_tax_group"`
	PriceIncludesTax bool   `json:"price_includes_tax"`
}

type calculateCfg struct {
	DefaultRounding string              `json:"default_rounding"`
	Groups          map[string][]string `json:"groups"`
	ItemGroups      map[string][]string `json:"item_groups"`
	Codes           []codeBody          `json:"codes"`
}

type codeBody struct {
	Code         string     `json:"code"`
	Jurisdiction string     `json:"jurisdiction"`
	Rates        []rateBody `json:"rates"`
}

type rateBody struct {
	ValidFrom string `json:"valid_from"`
	RatePct   string `json:"rate_pct"`
}

type calculateResult struct {
	DocumentTaxesByCode []struct {
		Code      string `json:"code"`
		Direction string `json:"direction"`
		TaxAmount string `json:"tax_amount"`
	} `json:"document_taxes_by_code"`
}

func (c *Client) CalculateInput(ctx context.Context, legalEntityID string, in domain.TaxDocument) (domain.TaxResult, error) {
	rule, ok := c.config.Rules[strings.ToUpper(in.Country)]
	if !ok || rule.Code == "" {
		return domain.TaxResult{}, domain.ErrTaxNotDetermined
	}
	lines := make([]lineBody, 0, len(in.Lines))
	for i, l := range in.Lines {
		ref := l.Ref
		if ref == "" {
			ref = fmt.Sprintf("L%d", i+1)
		}
		lines = append(lines, lineBody{LineRef: ref, Amount: l.Amount.String(), ItemTaxGroup: c.config.ItemGroup})
	}
	payload, err := json.Marshal(calculateBody{
		Currency: in.Currency, Direction: "input", DocumentDate: in.Date, TaxDate: in.Date,
		Party: partyBody{TaxGroup: c.config.Group, Country: strings.ToUpper(in.Country)},
		Lines: lines,
		Config: calculateCfg{
			DefaultRounding: c.config.Rounding,
			Groups:          map[string][]string{c.config.Group: {rule.Code}},
			ItemGroups:      map[string][]string{c.config.ItemGroup: {rule.Code}},
			Codes:           []codeBody{{Code: rule.Code, Jurisdiction: rule.Jurisdiction, Rates: []rateBody{{ValidFrom: "2000-01-01", RatePct: rule.RatePct.String()}}}},
		},
	})
	if err != nil {
		return domain.TaxResult{}, fmt.Errorf("tax: %w", err)
	}
	raw, err := c.do(ctx, legalEntityID, http.MethodPost, calculatePath, payload, dutyCalculate)
	if err != nil {
		return domain.TaxResult{}, err
	}
	var res calculateResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return domain.TaxResult{}, errors.New("tax: response has no usable tax amount")
	}
	total := decimal.Zero
	for _, row := range res.DocumentTaxesByCode {
		amount, err := decimal.NewFromString(row.TaxAmount)
		if err != nil || amount.IsNegative() {
			return domain.TaxResult{}, errors.New("tax: response has no usable tax amount")
		}
		if row.Code != "" && row.Code != rule.Code {
			return domain.TaxResult{}, fmt.Errorf("tax: response carries tax code %q, the document was sent with %q only", row.Code, rule.Code)
		}
		total = total.Add(amount)
	}
	return domain.TaxResult{Code: rule.Code, RatePct: rule.RatePct, Amount: total}, nil
}

type transactionsBody struct {
	Transactions []txnBody `json:"transactions"`
}

type txnBody struct {
	ID        string `json:"id"`
	Code      string `json:"code"`
	Direction string `json:"direction"`
	TaxDate   string `json:"tax_date"`
	Amount    string `json:"amount"`
}

func (c *Client) RecordInput(ctx context.Context, legalEntityID string, txn domain.TaxTxn) error {
	payload, err := json.Marshal(transactionsBody{Transactions: []txnBody{{
		ID: txn.ID, Code: txn.Code, Direction: "input", TaxDate: txn.Date, Amount: txn.Amount.String(),
	}}})
	if err != nil {
		return fmt.Errorf("tax: %w", err)
	}
	_, err = c.do(ctx, legalEntityID, http.MethodPost, transactionsPath, payload, dutyRecord)
	return err
}

func (c *Client) do(ctx context.Context, legalEntityID, method, path string, payload []byte, duty string) ([]byte, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("%w: no principal in context", ErrConfig)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("tax: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.signer != nil {
		if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{duty}); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrConfig, err)
		}
	} else {
		identity.Forward(req, principal)
		req.Header.Set(tenant.HeaderDuties, duty)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "tax request failed", "err", err, "path", path, "correlationId", middleware.GetReqID(ctx))
		return nil, fmt.Errorf("tax: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		slog.ErrorContext(ctx, "tax read failed", "err", err, "path", path, "correlationId", middleware.GetReqID(ctx))
		return nil, fmt.Errorf("tax: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.ErrorContext(ctx, "tax rejected the request", "status", resp.StatusCode, "path", path, "correlationId", middleware.GetReqID(ctx))
		return nil, &StatusError{Status: resp.StatusCode}
	}
	return raw, nil
}
