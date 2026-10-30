package ledger

import (
	"bytes"
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

	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/payables/internal/identity"
)

const (
	Timeout         = 3 * time.Second
	balancesPath    = "/v1/ledger/balances"
	ledgersPath     = "/v1/ledger/ledgers/"
	entriesPath     = "/v1/ledger/journal-entries"
	sourceSystem    = "payables"
	maxResponseSize = 1 << 20
)

type Balance struct {
	Currency string
	Debit    decimal.Decimal
	Credit   decimal.Decimal
}

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	httpClient = &noRedirect
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

type balancesBody struct {
	Currency string `json:"currency"`
	Rows     []struct {
		Account string `json:"account"`
		Debit   string `json:"debit"`
		Credit  string `json:"credit"`
	} `json:"rows"`
}

type StatusError struct{ Status int }

func (e *StatusError) Error() string { return fmt.Sprintf("ledger: unexpected status %d", e.Status) }

func (c *Client) get(ctx context.Context, path string, q url.Values, subject string) ([]byte, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return nil, errors.New("ledger: no principal in context")
	}
	target := c.baseURL + path
	if q != nil {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	identity.Forward(req, principal)
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "ledger request failed", "err", err, "subject", subject, "correlationId", middleware.GetReqID(ctx))
		return nil, fmt.Errorf("ledger: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		slog.ErrorContext(ctx, "ledger read failed", "err", err, "subject", subject, "correlationId", middleware.GetReqID(ctx))
		return nil, fmt.Errorf("ledger: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		slog.ErrorContext(ctx, "ledger rejected", "status", resp.StatusCode, "subject", subject, "correlationId", middleware.GetReqID(ctx))
		return nil, &StatusError{Status: resp.StatusCode}
	}
	return raw, nil
}

func (c *Client) AccountBalance(ctx context.Context, ledgerID, account, periodTo string) (Balance, error) {
	q := url.Values{"ledger": {ledgerID}, "period_to": {periodTo}, "account_from": {account}, "account_to": {account}}
	raw, err := c.get(ctx, balancesPath, q, account)
	if err != nil {
		return Balance{}, err
	}
	return parse(ctx, raw, account)
}

func (c *Client) LegalEntity(ctx context.Context, ledgerID string) (string, error) {
	raw, err := c.get(ctx, ledgersPath+url.PathEscape(ledgerID), nil, ledgerID)
	if err != nil {
		return "", err
	}
	var body struct {
		LegalEntityID string `json:"legalEntityId"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.LegalEntityID == "" {
		return "", errors.New("ledger: response has no legalEntityId")
	}
	return body.LegalEntityID, nil
}

func parse(ctx context.Context, raw []byte, account string) (Balance, error) {
	var body balancesBody
	if err := json.Unmarshal(raw, &body); err != nil || body.Currency == "" {
		slog.ErrorContext(ctx, "ledger balance response unusable", "err", err, "account", account, "correlationId", middleware.GetReqID(ctx))
		return Balance{}, errors.New("ledger balance: response has no currency")
	}
	out := Balance{Currency: body.Currency}
	for _, r := range body.Rows {
		if r.Account != account {
			continue
		}
		d, derr := decimal.NewFromString(r.Debit)
		c, cerr := decimal.NewFromString(r.Credit)
		if err := errors.Join(derr, cerr); err != nil {
			slog.ErrorContext(ctx, "ledger balance amount unusable", "err", err, "account", account, "correlationId", middleware.GetReqID(ctx))
			return Balance{}, fmt.Errorf("ledger balance: %w", err)
		}
		out.Debit, out.Credit = out.Debit.Add(d), out.Credit.Add(c)
	}
	return out, nil
}

type Entry struct {
	LedgerID       string
	DocumentID     string
	PostingDate    string
	Currency       string
	IdempotencyKey string
	SourceType     string
	Lines          []domain.RunJournalLine
}

type entryBody struct {
	LedgerID    string     `json:"ledgerId"`
	DocumentID  string     `json:"documentId"`
	PostingDate string     `json:"postingDate"`
	Source      sourceBody `json:"source"`
	Lines       []lineBody `json:"lines"`
}

type sourceBody struct {
	System string `json:"system"`
	Type   string `json:"type"`
	ID     string `json:"id"`
}

type lineBody struct {
	Account     string     `json:"account"`
	DebitCredit string     `json:"debitCredit"`
	Amount      amountBody `json:"amount"`
	Text        string     `json:"text,omitempty"`
}

type amountBody struct {
	Currency string `json:"currency"`
	Value    string `json:"value"`
}

type entryResult struct {
	Entries []struct {
		ID string `json:"id"`
	} `json:"entries"`
}

func (c *Client) Post(ctx context.Context, e Entry) (string, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return "", errors.New("ledger: no principal in context")
	}
	body := entryBody{LedgerID: e.LedgerID, DocumentID: e.DocumentID, PostingDate: e.PostingDate, Source: sourceBody{System: sourceSystem, Type: e.SourceType, ID: e.DocumentID}}
	for _, l := range e.Lines {
		side := "D"
		if l.Credit {
			side = "C"
		}
		body.Lines = append(body.Lines, lineBody{Account: l.Account, DebitCredit: side, Text: l.Text, Amount: amountBody{Currency: e.Currency, Value: l.Amount.StringFixed(money.MinorUnits(e.Currency))}})
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("ledger: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+entriesPath, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("ledger: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", e.IdempotencyKey)
	identity.Forward(req, principal)
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "ledger post failed", "err", err, "key", e.IdempotencyKey, "correlationId", middleware.GetReqID(ctx))
		return "", fmt.Errorf("ledger: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		slog.ErrorContext(ctx, "ledger post read failed", "err", err, "key", e.IdempotencyKey, "correlationId", middleware.GetReqID(ctx))
		return "", fmt.Errorf("ledger: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		slog.ErrorContext(ctx, "ledger rejected the entry", "status", resp.StatusCode, "key", e.IdempotencyKey, "correlationId", middleware.GetReqID(ctx))
		return "", &StatusError{Status: resp.StatusCode}
	}
	var result entryResult
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Entries) == 0 || result.Entries[0].ID == "" {
		slog.ErrorContext(ctx, "ledger post response unusable", "err", err, "key", e.IdempotencyKey, "correlationId", middleware.GetReqID(ctx))
		return "", errors.New("ledger post: response has no entry id")
	}
	return result.Entries[0].ID, nil
}
