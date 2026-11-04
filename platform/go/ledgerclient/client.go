package ledgerclient

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
)

const (
	Timeout = 3 * time.Second

	DutyPost        = "ledger.entry.post"
	DutyReverse     = "ledger.journal.reverse"
	DutyBalanceRead = "ledger.balance.read"
	DutyLedgerRead  = "ledger.ledger.lookup"
	DutyPostClosed  = "ledger.period.postclosed"

	audience = "ledger"

	CloseService               = "close"
	YearEndClosingSourceType   = "year_end_closing"
	excludeYearEndClosingParam = "exclude_year_end_closing"
	ledgersPath                = "/v1/ledger/ledgers/"
	entriesPath                = "/v1/ledger/journal-entries"
	balancesPath               = "/v1/ledger/balances"
	maxResponseSize            = 1 << 20
	sideDebit                  = "D"
	sideCredit                 = "C"
)

var (
	ErrUnsigned            = errors.New("ledger: posting needs a service signing key; the end user's headers are never forwarded for a journal")
	ErrNoPrincipal         = errors.New("ledger: no principal in context")
	ErrNoDuty              = errors.New("ledger: a request must name the duties it is signed with; an empty list would sign the whole grant")
	ErrUnbalanced          = errors.New("ledger: entry lines do not balance")
	ErrExclusionNotApplied = errors.New("ledger balance: the ledger did not confirm excluding the year-end closing entries")
	ErrEmptyEntry          = errors.New("ledger: entry needs at least two lines with positive amounts, an account and a currency")
)

type Line struct {
	Account    string
	Credit     bool
	Amount     decimal.Decimal
	Text       string
	Dimensions map[string]string
}

type Entry struct {
	LegalEntityID  string
	LedgerID       string
	DocumentID     string
	PostingDate    string
	Currency       string
	IdempotencyKey string
	SourceType     string
	PostClosed     bool
	Lines          []Line
}

type Reversal struct {
	LegalEntityID  string
	EntryID        string
	PostingDate    string
	Description    string
	IdempotencyKey string
	PostClosed     bool
}

type BalanceQuery struct {
	LegalEntityID string
	LedgerID      string
	Account       string
	AccountTo     string
	PeriodFrom    string
	PeriodTo      string
	// ExcludeYearEndClosing asks the ledger to leave out the year-end closing entries posted by the close service and their
	// reversals; Balances fails when the ledger does not confirm it applied the option.
	ExcludeYearEndClosing bool
}

type Balance struct {
	Currency string
	Debit    decimal.Decimal
	Credit   decimal.Decimal
}

func (b Balance) Net() decimal.Decimal { return b.Debit.Sub(b.Credit) }

type AccountBalance struct {
	Account string
	Debit   decimal.Decimal
	Credit  decimal.Decimal
}

func (b AccountBalance) Net() decimal.Decimal { return b.Debit.Sub(b.Credit) }

type AccountBalances struct {
	Currency               string
	ExcludedYearEndClosing bool
	Rows                   []AccountBalance
}

type Ledger interface {
	Post(ctx context.Context, e Entry) (string, error)
	Reverse(ctx context.Context, r Reversal) (string, error)
	Balance(ctx context.Context, q BalanceQuery) (Balance, error)
	LegalEntity(ctx context.Context, legalEntityID, ledgerID string) (string, error)
}

// Error is a non-success answer of the ledger; Code is the problem code it sent, passed through unchanged.
type Error struct {
	Op     string
	Status int
	Code   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("ledger %s: status %d code %q", e.Op, e.Status, e.Code)
}

// CodeOf returns the ledger problem code carried by err, or "".
func CodeOf(err error) string {
	var le *Error
	if errors.As(err, &le) {
		return le.Code
	}
	return ""
}

// Rejected reports a 4xx answer that a retry cannot fix (not a conflict or a timeout).
func Rejected(err error) bool {
	var le *Error
	return errors.As(err, &le) && le.Status >= 400 && le.Status < 500 && le.Status != http.StatusConflict && le.Status != http.StatusRequestTimeout
}

// Rounded returns the entry with every line rounded to the currency's minor unit, refusing it when it has fewer than two lines,
// a line that rounds to zero, or debits and credits that differ on the rounded amounts.
func (e Entry) Rounded() (Entry, error) {
	if len(e.Lines) < 2 || e.Currency == "" || e.LedgerID == "" || e.IdempotencyKey == "" {
		return Entry{}, ErrEmptyEntry
	}
	mu := money.MinorUnits(e.Currency)
	out := e
	out.Lines = make([]Line, len(e.Lines))
	debit, credit := decimal.Zero, decimal.Zero
	for i, l := range e.Lines {
		l.Amount = l.Amount.Round(mu)
		if l.Account == "" || !l.Amount.IsPositive() {
			return Entry{}, ErrEmptyEntry
		}
		if l.Credit {
			credit = credit.Add(l.Amount)
		} else {
			debit = debit.Add(l.Amount)
		}
		out.Lines[i] = l
	}
	if !debit.Equal(credit) {
		return Entry{}, fmt.Errorf("%w: debit %s credit %s", ErrUnbalanced, debit, credit)
	}
	return out, nil
}

func (e Entry) Validate() error {
	_, err := e.Rounded()
	return err
}

type Client struct {
	baseURL string
	source  string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

// New builds the client of one calling service; source is the journal's source system and signer is required.
func New(baseURL, source string, signer *tenant.ServiceSigner, httpClient *http.Client) (*Client, error) {
	if baseURL == "" || source == "" {
		return nil, errors.New("ledger client needs a base URL and a source system")
	}
	if signer == nil {
		return nil, ErrUnsigned
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: Timeout}
	}
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), source: source, http: &noRedirect, signer: signer}, nil
}

type amountBody struct {
	Currency string `json:"currency"`
	Value    string `json:"value"`
}

type lineBody struct {
	Account     string            `json:"account"`
	DebitCredit string            `json:"debitCredit"`
	Amount      amountBody        `json:"amount"`
	Text        string            `json:"text,omitempty"`
	Dimensions  map[string]string `json:"dimensions,omitempty"`
}

type sourceBody struct {
	System string `json:"system"`
	Type   string `json:"type"`
	ID     string `json:"id"`
}

type entryBody struct {
	LedgerID    string     `json:"ledgerId"`
	DocumentID  string     `json:"documentId"`
	PostingDate string     `json:"postingDate"`
	Source      sourceBody `json:"source"`
	Lines       []lineBody `json:"lines"`
}

type reverseBody struct {
	PostingDate string `json:"postingDate,omitempty"`
	Description string `json:"description,omitempty"`
}

type entryResult struct {
	JournalEntryID string `json:"journalEntryId"`
	Entries        []struct {
		ID string `json:"id"`
	} `json:"entries"`
}

func (r entryResult) id() string {
	if r.JournalEntryID != "" {
		return r.JournalEntryID
	}
	if len(r.Entries) > 0 {
		return r.Entries[0].ID
	}
	return ""
}

type balancesBody struct {
	Currency               string `json:"currency"`
	ExcludesYearEndClosing bool   `json:"excludesYearEndClosing"`
	Rows                   []struct {
		Account string `json:"account"`
		Debit   string `json:"debit"`
		Credit  string `json:"credit"`
	} `json:"rows"`
}

func (c *Client) do(ctx context.Context, op, subject, method, path string, query url.Values, payload any, key, legalEntity string, duties ...string) ([]byte, error) {
	if len(duties) == 0 {
		return nil, ErrNoDuty
	}
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return nil, ErrNoPrincipal
	}
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("ledger %s: %w", op, err)
		}
		body = bytes.NewReader(raw)
	}
	target := c.baseURL + path
	if query != nil {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, fmt.Errorf("ledger %s: %w", op, err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntity, duties); err != nil {
		return nil, fmt.Errorf("ledger %s: %w", op, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "ledger request failed", "op", op, "subject", subject, "err", err, "correlationId", middleware.GetReqID(ctx))
		return nil, fmt.Errorf("ledger %s %s: %w", op, subject, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		slog.ErrorContext(ctx, "ledger response unreadable", "op", op, "subject", subject, "err", err, "correlationId", middleware.GetReqID(ctx))
		return nil, fmt.Errorf("ledger %s %s: %w", op, subject, err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		var problem struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(raw, &problem)
		slog.ErrorContext(ctx, "ledger rejected the request", "op", op, "subject", subject, "status", resp.StatusCode, "code", problem.Code, "correlationId", middleware.GetReqID(ctx))
		return nil, &Error{Op: op, Status: resp.StatusCode, Code: problem.Code}
	}
	return raw, nil
}

func postDuties(duty string, postClosed bool) []string {
	if postClosed {
		return []string{duty, DutyPostClosed}
	}
	return []string{duty}
}

func entryID(ctx context.Context, op, subject string, raw []byte) (string, error) {
	var out entryResult
	if err := json.Unmarshal(raw, &out); err != nil || out.id() == "" {
		slog.ErrorContext(ctx, "ledger response has no entry id", "op", op, "subject", subject, "err", err, "correlationId", middleware.GetReqID(ctx))
		return "", fmt.Errorf("ledger %s %s: response has no entry id", op, subject)
	}
	return out.id(), nil
}

func (c *Client) Post(ctx context.Context, e Entry) (string, error) {
	e, err := e.Rounded()
	if err != nil {
		return "", err
	}
	body := entryBody{LedgerID: e.LedgerID, DocumentID: e.DocumentID, PostingDate: e.PostingDate,
		Source: sourceBody{System: c.source, Type: e.SourceType, ID: e.DocumentID}}
	for _, l := range e.Lines {
		side := sideDebit
		if l.Credit {
			side = sideCredit
		}
		body.Lines = append(body.Lines, lineBody{Account: l.Account, DebitCredit: side, Text: l.Text, Dimensions: l.Dimensions,
			Amount: amountBody{Currency: e.Currency, Value: l.Amount.StringFixed(money.MinorUnits(e.Currency))}})
	}
	raw, err := c.do(ctx, "post", e.IdempotencyKey, http.MethodPost, entriesPath, nil, body, e.IdempotencyKey, e.LegalEntityID, postDuties(DutyPost, e.PostClosed)...)
	if err != nil {
		return "", err
	}
	return entryID(ctx, "post", e.IdempotencyKey, raw)
}

func (c *Client) Reverse(ctx context.Context, r Reversal) (string, error) {
	path := entriesPath + "/" + url.PathEscape(r.EntryID) + "/reverse"
	raw, err := c.do(ctx, "reverse", r.EntryID, http.MethodPost, path, nil, reverseBody{PostingDate: r.PostingDate, Description: r.Description}, r.IdempotencyKey, r.LegalEntityID, postDuties(DutyReverse, r.PostClosed)...)
	if err != nil {
		return "", err
	}
	return entryID(ctx, "reverse", r.EntryID, raw)
}

func (c *Client) Balance(ctx context.Context, q BalanceQuery) (Balance, error) {
	q.AccountTo = q.Account
	set, err := c.Balances(ctx, q)
	if err != nil {
		return Balance{}, err
	}
	out := Balance{Currency: set.Currency}
	for _, r := range set.Rows {
		if r.Account == q.Account {
			out.Debit, out.Credit = out.Debit.Add(r.Debit), out.Credit.Add(r.Credit)
		}
	}
	return out, nil
}

// Balances returns one row per account from Account to AccountTo (Account alone when AccountTo is empty).
func (c *Client) Balances(ctx context.Context, q BalanceQuery) (AccountBalances, error) {
	to := q.AccountTo
	if to == "" {
		to = q.Account
	}
	query := url.Values{"ledger": {q.LedgerID}, "account_from": {q.Account}, "account_to": {to}}
	if q.PeriodFrom != "" {
		query.Set("period_from", q.PeriodFrom)
	}
	if q.PeriodTo != "" {
		query.Set("period_to", q.PeriodTo)
	}
	if q.ExcludeYearEndClosing {
		query.Set(excludeYearEndClosingParam, "true")
	}
	raw, err := c.do(ctx, "balance", q.Account, http.MethodGet, balancesPath, query, nil, "", q.LegalEntityID, DutyBalanceRead)
	if err != nil {
		return AccountBalances{}, err
	}
	var body balancesBody
	if err := json.Unmarshal(raw, &body); err != nil || body.Currency == "" {
		slog.ErrorContext(ctx, "ledger balance response unusable", "account", q.Account, "err", err, "correlationId", middleware.GetReqID(ctx))
		return AccountBalances{}, errors.New("ledger balance: response has no currency")
	}
	if q.ExcludeYearEndClosing && !body.ExcludesYearEndClosing {
		slog.ErrorContext(ctx, "ledger did not apply excludeYearEndClosing", "account", q.Account, "correlationId", middleware.GetReqID(ctx))
		return AccountBalances{}, ErrExclusionNotApplied
	}
	out := AccountBalances{Currency: body.Currency, ExcludedYearEndClosing: body.ExcludesYearEndClosing}
	for _, r := range body.Rows {
		d, derr := decimal.NewFromString(r.Debit)
		cr, cerr := decimal.NewFromString(r.Credit)
		if err := errors.Join(derr, cerr); err != nil {
			slog.ErrorContext(ctx, "ledger balance amount unusable", "account", r.Account, "err", err, "correlationId", middleware.GetReqID(ctx))
			return AccountBalances{}, fmt.Errorf("ledger balance: %w", err)
		}
		out.Rows = append(out.Rows, AccountBalance{Account: r.Account, Debit: d, Credit: cr})
	}
	return out, nil
}

// LegalEntity returns the legal entity that owns ledgerID; an unknown ledger is an *Error with status 404.
func (c *Client) LegalEntity(ctx context.Context, legalEntityID, ledgerID string) (string, error) {
	raw, err := c.do(ctx, "ledger", ledgerID, http.MethodGet, ledgersPath+url.PathEscape(ledgerID), nil, nil, "", legalEntityID, DutyLedgerRead)
	if err != nil {
		return "", err
	}
	var body struct {
		LegalEntityID string `json:"legalEntityId"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.LegalEntityID == "" {
		slog.ErrorContext(ctx, "ledger lookup response unusable", "ledger", ledgerID, "err", err, "correlationId", middleware.GetReqID(ctx))
		return "", errors.New("ledger lookup: response has no legalEntityId")
	}
	return body.LegalEntityID, nil
}

// FromEnv builds the client from LEDGER_URL, LEDGER_TIMEOUT and the service signing key variables; it returns nil, nil when LEDGER_URL is unset
// and an error when it is set without a signing key.
func FromEnv(getenv func(string) string, source string) (*Client, error) {
	baseURL := getenv("LEDGER_URL")
	if baseURL == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	timeout := Timeout
	if v, err := time.ParseDuration(getenv("LEDGER_TIMEOUT")); err == nil && v > 0 {
		timeout = v
	}
	return New(baseURL, source, signer, &http.Client{Timeout: timeout})
}
