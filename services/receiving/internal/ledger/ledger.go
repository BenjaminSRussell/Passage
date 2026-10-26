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
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

const (
	Timeout         = 3 * time.Second
	periodClosed    = "period_closed"
	maxResponseSize = 1 << 20
	dateFormat      = "2006-01-02"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

type moneyBody struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type lineBody struct {
	LineNo  int        `json:"lineNo"`
	Account string     `json:"account"`
	Debit   *moneyBody `json:"debit,omitempty"`
	Credit  *moneyBody `json:"credit,omitempty"`
}

type requestBody struct {
	LegalEntityID string                `json:"legalEntityId"`
	PostingDate   string                `json:"postingDate"`
	DocumentDate  string                `json:"documentDate"`
	Source        domain.DocumentSource `json:"source"`
	Currency      string                `json:"currency"`
	RateType      string                `json:"rateType"`
	Lines         []lineBody            `json:"lines"`
}

type responseBody struct {
	JournalEntryID string `json:"journalEntryId"`
}

type problemBody struct {
	Code string `json:"code"`
}

func buildBody(entry domain.JournalEntry) (requestBody, error) {
	if len(entry.Lines) == 0 {
		return requestBody{}, errors.New("journal entry has no lines")
	}
	currency := entry.Lines[0].Amount.Currency
	body := requestBody{
		LegalEntityID: entry.LegalEntityID,
		PostingDate:   entry.PostingDate.Format(dateFormat),
		DocumentDate:  entry.DocumentDate.Format(dateFormat),
		Source:        entry.Source,
		Currency:      currency,
		RateType:      entry.RateType,
		Lines:         make([]lineBody, 0, len(entry.Lines)),
	}
	for i, l := range entry.Lines {
		if l.Account == "" || l.Amount.Currency != currency {
			return requestBody{}, fmt.Errorf("journal line %d needs an account and currency %s", i+1, currency)
		}
		line := lineBody{LineNo: i + 1, Account: l.Account}
		abs := money.Amount{Value: l.Amount.Value.Abs(), Currency: currency}
		wire := &moneyBody{Amount: abs.Value.StringFixed(money.MinorUnits(currency)), Currency: currency}
		if l.Amount.Value.IsNegative() {
			line.Credit = wire
		} else {
			line.Debit = wire
		}
		body.Lines = append(body.Lines, line)
	}
	return body, nil
}

func (c *Client) Post(ctx context.Context, entry domain.JournalEntry) (string, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return "", errors.New("ledger post: no principal in context")
	}
	body, err := buildBody(entry)
	if err != nil {
		return "", fmt.Errorf("ledger post %s: %w", entry.IdempotencyKey, err)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("ledger post %s: %w", entry.IdempotencyKey, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+domain.LedgerPostPath, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("ledger post %s: %w", entry.IdempotencyKey, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", entry.IdempotencyKey)
	req.Header.Set(tenant.HeaderTenant, principal.TenantID)
	if id := middleware.GetReqID(ctx); id != "" {
		req.Header.Set(middleware.RequestIDHeader, id)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "ledger post transport failed", "err", err, "idempotencyKey", entry.IdempotencyKey, "correlationId", middleware.GetReqID(ctx))
		return "", fmt.Errorf("ledger post %s: %w", entry.IdempotencyKey, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		slog.ErrorContext(ctx, "ledger post read failed", "err", err, "idempotencyKey", entry.IdempotencyKey, "correlationId", middleware.GetReqID(ctx))
		return "", fmt.Errorf("ledger post %s: %w", entry.IdempotencyKey, err)
	}

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		var out responseBody
		if err := json.Unmarshal(raw, &out); err != nil || out.JournalEntryID == "" {
			slog.ErrorContext(ctx, "ledger post response unusable", "err", err, "status", resp.StatusCode, "idempotencyKey", entry.IdempotencyKey, "correlationId", middleware.GetReqID(ctx))
			return "", fmt.Errorf("ledger post %s: response has no journalEntryId", entry.IdempotencyKey)
		}
		return out.JournalEntryID, nil
	}

	if resp.StatusCode == http.StatusUnprocessableEntity {
		var problem problemBody
		if json.Unmarshal(raw, &problem) == nil && problem.Code == periodClosed {
			return "", fmt.Errorf("ledger post %s: %w", entry.IdempotencyKey, domain.ErrPeriodClosed)
		}
	}
	err = fmt.Errorf("ledger post %s: unexpected status %d", entry.IdempotencyKey, resp.StatusCode)
	slog.ErrorContext(ctx, "ledger post rejected", "err", err, "status", resp.StatusCode, "idempotencyKey", entry.IdempotencyKey, "correlationId", middleware.GetReqID(ctx))
	return "", err
}
