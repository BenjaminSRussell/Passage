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
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/identity"
)

const (
	Timeout             = 3 * time.Second
	idempotencyConflict = "LEDGER_IDEMPOTENCY_CONFLICT"
	maxResponseSize     = 1 << 20
	dateFormat          = "2006-01-02"
	sourceSystem        = "receiving"
	sideDebit           = "D"
	sideCredit          = "C"
	reversalDescription = "reversal of a receiving posting that was never confirmed locally"
)

var periodClosedCodes = []string{"period_closed", "LEDGER_PERIOD_CLOSED"}

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

type sourceBody struct {
	System string `json:"system"`
	Type   string `json:"type"`
	ID     string `json:"id"`
}

type amountBody struct {
	Currency string `json:"currency"`
	Value    string `json:"value"`
}

type lineBody struct {
	Account     string     `json:"account"`
	DebitCredit string     `json:"debitCredit"`
	Amount      amountBody `json:"amount"`
}

type requestBody struct {
	LedgerID     string     `json:"ledgerId"`
	DocumentID   string     `json:"documentId"`
	PostingDate  string     `json:"postingDate"`
	DocumentDate string     `json:"documentDate,omitempty"`
	Source       sourceBody `json:"source"`
	Lines        []lineBody `json:"lines"`
}

type reverseBody struct {
	PostingDate string `json:"postingDate"`
	Description string `json:"description"`
}

type responseBody struct {
	JournalEntryID string `json:"journalEntryId"`
	Entries        []struct {
		ID string `json:"id"`
	} `json:"entries"`
}

func (r responseBody) entryID() string {
	if r.JournalEntryID != "" {
		return r.JournalEntryID
	}
	if len(r.Entries) > 0 {
		return r.Entries[0].ID
	}
	return ""
}

type problemBody struct {
	Code       string `json:"code"`
	Violations []struct {
		Code string `json:"code"`
	} `json:"violations"`
}

func (p problemBody) periodClosed() bool {
	if slices.Contains(periodClosedCodes, p.Code) {
		return true
	}
	for _, v := range p.Violations {
		if slices.Contains(periodClosedCodes, v.Code) {
			return true
		}
	}
	return false
}

func buildBody(entry domain.JournalEntry) (requestBody, error) {
	if len(entry.Lines) == 0 {
		return requestBody{}, errors.New("journal entry has no lines")
	}
	currency := entry.Lines[0].Amount.Currency
	body := requestBody{
		LedgerID:    entry.LedgerID,
		DocumentID:  entry.Source.DocumentID,
		PostingDate: entry.PostingDate.Format(dateFormat),
		Source:      sourceBody{System: sourceSystem, Type: entry.Source.DocumentType, ID: entry.Source.DocumentID},
		Lines:       make([]lineBody, 0, len(entry.Lines)),
	}
	if !entry.DocumentDate.IsZero() {
		body.DocumentDate = entry.DocumentDate.Format(dateFormat)
	}
	for i, l := range entry.Lines {
		if l.Account == "" || l.Amount.Currency != currency {
			return requestBody{}, fmt.Errorf("journal line %d needs an account and currency %s", i+1, currency)
		}
		side := sideDebit
		if l.Amount.Value.IsNegative() {
			side = sideCredit
		}
		abs := money.Amount{Value: l.Amount.Value.Abs(), Currency: currency}
		body.Lines = append(body.Lines, lineBody{
			Account:     l.Account,
			DebitCredit: side,
			Amount:      amountBody{Currency: currency, Value: abs.Value.StringFixed(money.MinorUnits(currency))},
		})
	}
	return body, nil
}

func (c *Client) newRequest(ctx context.Context, path, idempotencyKey string, payload any) (*http.Request, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return nil, errors.New("no principal in context")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)
	identity.Forward(req, principal)
	return req, nil
}

func (c *Client) do(ctx context.Context, op, subject string, req *http.Request) (int, []byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "ledger "+op+" transport failed", "err", err, "subject", subject, "correlationId", middleware.GetReqID(ctx))
		return 0, nil, fmt.Errorf("ledger %s %s: %w", op, subject, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		slog.ErrorContext(ctx, "ledger "+op+" read failed", "err", err, "subject", subject, "correlationId", middleware.GetReqID(ctx))
		return 0, nil, fmt.Errorf("ledger %s %s: %w", op, subject, err)
	}
	return resp.StatusCode, raw, nil
}

func classify(status int, raw []byte) error {
	var problem problemBody
	_ = json.Unmarshal(raw, &problem)
	switch {
	case status == http.StatusUnprocessableEntity && problem.periodClosed():
		return domain.ErrPeriodClosed
	case status == http.StatusConflict && problem.Code == idempotencyConflict:
		return domain.ErrLedgerConflict
	case status >= 400 && status < 500 && status != http.StatusConflict && status != http.StatusRequestTimeout:
		return domain.ErrLedgerRejected
	}
	return nil
}

func (c *Client) Post(ctx context.Context, entry domain.JournalEntry) (string, error) {
	body, err := buildBody(entry)
	if err != nil {
		return "", fmt.Errorf("ledger post %s: %w", entry.IdempotencyKey, err)
	}
	req, err := c.newRequest(ctx, domain.LedgerPostPath, entry.IdempotencyKey, body)
	if err != nil {
		return "", fmt.Errorf("ledger post %s: %w", entry.IdempotencyKey, err)
	}
	status, raw, err := c.do(ctx, "post", entry.IdempotencyKey, req)
	if err != nil {
		return "", err
	}
	if status == http.StatusOK || status == http.StatusCreated {
		var out responseBody
		if err := json.Unmarshal(raw, &out); err != nil || out.entryID() == "" {
			slog.ErrorContext(ctx, "ledger post response unusable", "err", err, "status", status, "idempotencyKey", entry.IdempotencyKey, "correlationId", middleware.GetReqID(ctx))
			return "", fmt.Errorf("ledger post %s: response has no journalEntryId", entry.IdempotencyKey)
		}
		return out.entryID(), nil
	}
	return "", c.rejection(ctx, "post", entry.IdempotencyKey, status, raw)
}

func (c *Client) rejection(ctx context.Context, op, subject string, status int, raw []byte) error {
	kind := classify(status, raw)
	err := fmt.Errorf("ledger %s %s: unexpected status %d", op, subject, status)
	if kind != nil {
		err = fmt.Errorf("ledger %s %s: status %d: %w", op, subject, status, kind)
	}
	if !errors.Is(kind, domain.ErrPeriodClosed) && !errors.Is(kind, domain.ErrLedgerConflict) {
		slog.ErrorContext(ctx, "ledger "+op+" rejected", "err", err, "status", status, "subject", subject, "correlationId", middleware.GetReqID(ctx))
	}
	return err
}

func (c *Client) Reverse(ctx context.Context, entryID, idempotencyKey string, postingDate time.Time) error {
	body := reverseBody{PostingDate: postingDate.Format(dateFormat), Description: reversalDescription}
	req, err := c.newRequest(ctx, fmt.Sprintf(domain.LedgerReversePathTmpl, url.PathEscape(entryID)), idempotencyKey, body)
	if err != nil {
		return fmt.Errorf("ledger reverse %s: %w", entryID, err)
	}
	status, raw, err := c.do(ctx, "reverse", entryID, req)
	if err != nil {
		return err
	}
	if status == http.StatusOK || status == http.StatusCreated {
		return nil
	}
	return c.rejection(ctx, "reverse", entryID, status, raw)
}
