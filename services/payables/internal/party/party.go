package party

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

const (
	Timeout         = 3 * time.Second
	audience        = "party"
	dutyBankReveal  = "party.vendor_bank.reveal"
	revealPath      = "/v1/party/vendors/%s/bank-accounts:reveal"
	maxResponseSize = 1 << 20
)

var (
	ErrVendorNotFound = errors.New("party: vendor not found")
	ErrUnsigned       = errors.New("party: bank details need a service signing key; the end user's headers are never forwarded for a reveal")
)

type StatusError struct{ Status int }

func (e *StatusError) Error() string { return fmt.Sprintf("party: unexpected status %d", e.Status) }

type BankAccount struct {
	ID            string `json:"id"`
	AccountHolder string `json:"accountHolder"`
	BankCountry   string `json:"bankCountry"`
	CurrencyCode  string `json:"currencyCode"`
	AccountNumber string `json:"accountNumber"`
	RoutingNumber string `json:"routingNumber"`
}

func (b BankAccount) Last4() string {
	n := strings.TrimSpace(b.AccountNumber)
	if len(n) < 4 {
		return "****"
	}
	return n[len(n)-4:]
}

func (b BankAccount) Fingerprint() string {
	h := sha256.New()
	for _, f := range []string{b.AccountNumber, b.RoutingNumber, b.AccountHolder, b.BankCountry} {
		_, _ = fmt.Fprintf(h, "%d:%s|", len(f), strings.TrimSpace(f))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (b BankAccount) Creditor() domain.CreditorDTO {
	number := strings.TrimSpace(b.AccountNumber)
	c := domain.CreditorDTO{PartyDTO: domain.PartyDTO{Name: strings.TrimSpace(b.AccountHolder), Address: domain.PostalAddressDTO{Country: strings.ToUpper(strings.TrimSpace(b.BankCountry))}}, RoutingNumber: strings.TrimSpace(b.RoutingNumber)}
	if domain.ValidateIBAN(number) == nil {
		c.IBAN = number
	} else {
		c.OtherID = number
	}
	return c
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

func (c *Client) BankAccounts(ctx context.Context, legalEntityID, vendorID string) ([]BankAccount, error) {
	if c.signer == nil {
		return nil, ErrUnsigned
	}
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return nil, errors.New("party: no principal in context")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+fmt.Sprintf(revealPath, url.PathEscape(vendorID)), nil)
	if err != nil {
		return nil, fmt.Errorf("party: %w", err)
	}
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{dutyBankReveal}); err != nil {
		return nil, fmt.Errorf("party: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "party request failed", "err", err, "vendorId", vendorID, "correlationId", middleware.GetReqID(ctx))
		return nil, fmt.Errorf("party: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		slog.ErrorContext(ctx, "party read failed", "err", err, "vendorId", vendorID, "correlationId", middleware.GetReqID(ctx))
		return nil, fmt.Errorf("party: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrVendorNotFound
	default:
		slog.ErrorContext(ctx, "party rejected the bank reveal", "status", resp.StatusCode, "vendorId", vendorID, "correlationId", middleware.GetReqID(ctx))
		return nil, &StatusError{Status: resp.StatusCode}
	}
	var body struct {
		BankAccounts []BankAccount `json:"bankAccounts"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		slog.ErrorContext(ctx, "party bank response unusable", "err", err, "vendorId", vendorID, "correlationId", middleware.GetReqID(ctx))
		return nil, errors.New("party: response is not a bank account list")
	}
	return body.BankAccounts, nil
}
