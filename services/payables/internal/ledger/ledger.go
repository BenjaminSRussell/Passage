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
	linesPath       = "/v1/ledger/journal-lines"
	sourceSystem    = "payables"
	maxResponseSize = 1 << 20
	maxLookupPages  = 5
)

type Balance struct {
	Currency string
	Debit    decimal.Decimal
	Credit   decimal.Decimal
}

const audience = "ledger"

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

func (c *Client) WithSigner(s *tenant.ServiceSigner) *Client {
	c.signer = s
	return c
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

type StatusError struct {
	Status int
	Code   string
}

func (e *StatusError) Error() string { return fmt.Sprintf("ledger: unexpected status %d", e.Status) }

const CodeIdempotencyInProgress = "LEDGER_IDEMPOTENCY_IN_PROGRESS"

const (
	dutyBalanceRead  = "ledger.balance.read"
	dutyLedgerLookup = "ledger.ledger.lookup"
	dutyEntryLookup  = "ledger.entry.lookup"
	dutyEntryPost    = "ledger.entry.post"
)

func (c *Client) get(ctx context.Context, legalEntityID, path string, q url.Values, subject, duty string) ([]byte, error) {
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
	signed, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{duty})
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	if !signed {
		identity.Forward(req, principal)
	}
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

func (c *Client) AccountBalance(ctx context.Context, legalEntityID, ledgerID, account, periodTo string) (Balance, error) {
	q := url.Values{"ledger": {ledgerID}, "period_to": {periodTo}, "account_from": {account}, "account_to": {account}}
	raw, err := c.get(ctx, legalEntityID, balancesPath, q, account, dutyBalanceRead)
	if err != nil {
		return Balance{}, err
	}
	return parse(ctx, raw, account)
}

func (c *Client) LegalEntity(ctx context.Context, legalEntityID, ledgerID string) (string, error) {
	raw, err := c.get(ctx, legalEntityID, ledgersPath+url.PathEscape(ledgerID), nil, ledgerID, dutyLedgerLookup)
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

type lookedUpLine struct {
	EntryID     string `json:"entryId"`
	Account     string `json:"account"`
	DebitCredit string `json:"debitCredit"`
	Amount      string `json:"amount"`
	Currency    string `json:"currency"`
	SourceType  string `json:"sourceType"`
	SourceID    string `json:"sourceId"`
}

// EntryForDocument returns the id of an entry this service already posted for e: same ledger, document id,
// payables source and exactly e's lines. An entry that merely shares the document id is never returned.
func (c *Client) EntryForDocument(ctx context.Context, e Entry) (string, error) {
	var order []string
	byEntry := map[string][]lookedUpLine{}
	cursor := ""
	for page := 0; page < maxLookupPages; page++ {
		q := url.Values{"ledger": {e.LedgerID}, "documentId": {e.DocumentID}, "source_system": {sourceSystem}, "limit": {"1000"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		raw, err := c.get(ctx, e.LegalEntityID, linesPath, q, e.DocumentID, dutyEntryLookup)
		if err != nil {
			return "", err
		}
		var body struct {
			Items      []lookedUpLine `json:"items"`
			NextCursor string         `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return "", fmt.Errorf("ledger: entry lookup response unusable: %w", err)
		}
		for _, l := range body.Items {
			if _, seen := byEntry[l.EntryID]; !seen {
				order = append(order, l.EntryID)
			}
			byEntry[l.EntryID] = append(byEntry[l.EntryID], l)
		}
		if cursor = body.NextCursor; cursor == "" {
			break
		}
	}
	for _, id := range order {
		if sameLines(e, byEntry[id]) {
			return id, nil
		}
	}
	return "", nil
}

func sameLines(e Entry, got []lookedUpLine) bool {
	if len(got) != len(e.Lines) {
		return false
	}
	want := make([]string, 0, len(e.Lines))
	for _, l := range e.Lines {
		want = append(want, lineKey(l.Account, l.Credit, l.Amount, e.Currency))
	}
	have := make([]string, 0, len(got))
	for _, l := range got {
		amount, err := decimal.NewFromString(l.Amount)
		if err != nil || l.SourceType != e.SourceType || l.SourceID != e.DocumentID {
			return false
		}
		have = append(have, lineKey(l.Account, l.DebitCredit == "C", amount, l.Currency))
	}
	slices.Sort(want)
	slices.Sort(have)
	return slices.Equal(want, have)
}

func lineKey(account string, credit bool, amount decimal.Decimal, currency string) string {
	return fmt.Sprintf("%s|%t|%s|%s", account, credit, amount.String(), currency)
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
	LegalEntityID  string
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

func (c *Client) Signed() bool { return c.signer != nil }

func (c *Client) Post(ctx context.Context, e Entry) (string, error) {
	if c.signer == nil {
		return "", errors.New("ledger: posting needs a service signing key; the end user's headers are never forwarded for a journal")
	}
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
	if _, err := c.signer.ApplyDuties(req, principal, audience, e.LegalEntityID, []string{dutyEntryPost}); err != nil {
		return "", fmt.Errorf("ledger: %w", err)
	}
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
		var problem struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(raw, &problem)
		return "", &StatusError{Status: resp.StatusCode, Code: problem.Code}
	}
	var result entryResult
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Entries) == 0 || result.Entries[0].ID == "" {
		slog.ErrorContext(ctx, "ledger post response unusable", "err", err, "key", e.IdempotencyKey, "correlationId", middleware.GetReqID(ctx))
		return "", errors.New("ledger post: response has no entry id")
	}
	return result.Entries[0].ID, nil
}
