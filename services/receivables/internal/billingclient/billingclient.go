package billingclient

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

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	timeout     = 3 * time.Second
	audience    = "billing"
	dutyRead    = "billing.invoice.read"
	invoicePath = "/v1/billing/invoices/"
)

var ErrUnsigned = errors.New("billing client needs the service signing key")

var ErrNotFound = errors.New("billing: invoice not found")

type Basis struct {
	Number        string
	LegalEntityID string
	Remaining     map[string]decimal.Decimal
}

type Reader interface {
	TaxBasis(ctx context.Context, legalEntityID, invoiceID string) (Basis, error)
}

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

func FromEnv(getenv func(string) string) (*Client, error) {
	baseURL := getenv("BILLING_URL")
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
	if v, err := time.ParseDuration(getenv("BILLING_TIMEOUT")); err == nil && v > 0 {
		d = v
	}
	noRedirect := http.Client{Timeout: d, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &noRedirect, signer: signer}, nil
}

type invoiceBody struct {
	Number        string `json:"number"`
	LegalEntityID string `json:"legalEntityId"`
	Status        string `json:"status"`
	Type          string `json:"type"`
	Lines         []struct {
		TaxCode     string `json:"taxCode"`
		Tax         string `json:"tax"`
		CreditedTax string `json:"creditedTax"`
	} `json:"lines"`
}

func (c *Client) TaxBasis(ctx context.Context, legalEntityID, invoiceID string) (Basis, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return Basis{}, errors.New("billing: no principal in context")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+invoicePath+url.PathEscape(invoiceID), nil)
	if err != nil {
		return Basis{}, fmt.Errorf("billing: %w", err)
	}
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{dutyRead}); err != nil {
		return Basis{}, fmt.Errorf("billing: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "billing request failed", "err", err, "correlationId", middleware.GetReqID(ctx))
		return Basis{}, fmt.Errorf("billing: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusNotFound {
		return Basis{}, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		slog.ErrorContext(ctx, "billing refused the invoice read", "status", resp.StatusCode, "correlationId", middleware.GetReqID(ctx))
		return Basis{}, fmt.Errorf("billing: unexpected status %d", resp.StatusCode)
	}
	var body invoiceBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return Basis{}, fmt.Errorf("billing: decode invoice: %w", err)
	}
	if body.Type == "CREDIT_NOTE" || body.Status == "REVERSED" || body.Status == "PROPOSAL" {
		return Basis{}, ErrNotFound
	}
	out := Basis{Number: body.Number, LegalEntityID: body.LegalEntityID, Remaining: map[string]decimal.Decimal{}}
	for _, l := range body.Lines {
		if l.TaxCode == "" {
			continue
		}
		tax, terr := optional(l.Tax)
		credited, cerr := optional(l.CreditedTax)
		if err := errors.Join(terr, cerr); err != nil {
			return Basis{}, fmt.Errorf("billing: invoice line tax: %w", err)
		}
		out.Remaining[l.TaxCode] = out.Remaining[l.TaxCode].Add(tax).Sub(credited)
	}
	return out, nil
}

func optional(s string) (decimal.Decimal, error) {
	if s == "" {
		return decimal.Zero, nil
	}
	return decimal.NewFromString(s)
}

type Fake struct {
	mu       sync.Mutex
	Err      error
	invoices map[string]Basis
}

func NewFake() *Fake { return &Fake{invoices: map[string]Basis{}} }

func (f *Fake) Set(invoiceID string, b Basis) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invoices[invoiceID] = b
}

func (f *Fake) TaxBasis(_ context.Context, _, invoiceID string) (Basis, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return Basis{}, f.Err
	}
	b, ok := f.invoices[invoiceID]
	if !ok {
		return Basis{}, ErrNotFound
	}
	return b, nil
}
