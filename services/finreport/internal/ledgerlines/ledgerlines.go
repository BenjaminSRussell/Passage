// Package ledgerlines reads the posted lines of one journal entry from the
// ledger service (GET /v1/ledger/journal-lines, service duty
// ledger.entry.lookup). It implements consumer.LineSource.
package ledgerlines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

const (
	// Duty is the service-only ledger duty every request is signed with.
	Duty = "ledger.entry.lookup"

	audience        = "ledger"
	linesPath       = "/v1/ledger/journal-lines"
	pageSize        = 1000
	maxPages        = 1000
	maxResponseSize = 8 << 20
	timeout         = 10 * time.Second
)

var (
	ErrNoPrincipal = errors.New("ledgerlines: no principal in context")
	ErrNoEntity    = errors.New("ledgerlines: the principal names no legal entity")
	// ErrTooManyPages stops a ledger that keeps answering with a cursor.
	ErrTooManyPages = errors.New("ledgerlines: the ledger returned too many pages")
)

// Error is a non-200 answer of the ledger.
type Error struct{ Status int }

func (e *Error) Error() string { return fmt.Sprintf("ledger journal-lines: status %d", e.Status) }

// Client is the HTTP LineSource.
type Client struct {
	baseURL string
	http    *http.Client
	// sign puts the service token on req; tests replace it because the
	// finreport to ledger grant is seeded in the platform, not here.
	sign func(req *http.Request, p tenant.Principal) error
}

// New builds the client; signer is required (the end user's headers are never
// forwarded: the consumer has none). A nil httpClient gets a 10 s timeout.
func New(baseURL string, signer *tenant.ServiceSigner, httpClient *http.Client) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("ledgerlines: LEDGER_URL is required")
	}
	if signer == nil {
		return nil, errors.New("ledgerlines: a service signing key is required (PASSAGE_SERVICE_SIGNING_KEY_FILE)")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	c := *httpClient
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &c, sign: func(req *http.Request, p tenant.Principal) error {
		_, err := signer.ApplyDuties(req, p, audience, p.LegalEntityID, []string{Duty})
		return err
	}}, nil
}

type lineBody struct {
	EntryID           string            `json:"entryId"`
	LineNumber        int               `json:"lineNumber"`
	Account           string            `json:"account"`
	Dimensions        map[string]string `json:"dimensions"`
	DebitCredit       string            `json:"debitCredit"`
	AmountAcct        string            `json:"amountAcct"`
	NegativePosting   bool              `json:"negativePosting"`
	ReversalOfEntryID string            `json:"reversalOfEntryId"`
	SourceURI         string            `json:"sourceUri"`
	TraceID           string            `json:"traceId"`
}

type pageBody struct {
	Items      []lineBody `json:"items"`
	NextCursor string     `json:"nextCursor"`
}

// EntryLines returns every line of entryID (all pages), in the ledger's
// order. A reversal shares its original's documentId, so lines of other
// entries on the page are dropped.
func (c *Client) EntryLines(ctx context.Context, ledgerID, documentID, entryID string) ([]domain.SourceLine, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return nil, ErrNoPrincipal
	}
	if p.LegalEntityID == "" {
		return nil, ErrNoEntity
	}
	var out []domain.SourceLine
	cursor := ""
	for range maxPages {
		q := url.Values{"ledger": {ledgerID}, "documentId": {documentID}, "limit": {fmt.Sprint(pageSize)}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		page, err := c.fetch(ctx, p, q)
		if err != nil {
			return nil, err
		}
		for _, l := range page.Items {
			if l.EntryID != entryID {
				continue
			}
			amount, err := decimal.NewFromString(l.AmountAcct)
			if err != nil {
				return nil, fmt.Errorf("ledger line %d of entry %s: amountAcct %q: %w", l.LineNumber, entryID, l.AmountAcct, err)
			}
			out = append(out, domain.SourceLine{
				LineNumber: l.LineNumber, Account: l.Account, Dimensions: l.Dimensions, DebitCredit: l.DebitCredit,
				AmountAcct: amount, Negative: l.NegativePosting, ReversalOfEntryID: l.ReversalOfEntryID,
				SourceURI: l.SourceURI, TraceID: l.TraceID,
			})
		}
		if page.NextCursor == "" {
			return out, nil
		}
		cursor = page.NextCursor
	}
	return nil, ErrTooManyPages
}

func (c *Client) fetch(ctx context.Context, p tenant.Principal, q url.Values) (pageBody, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+linesPath+"?"+q.Encode(), nil)
	if err != nil {
		return pageBody{}, err
	}
	if err := c.sign(req, p); err != nil {
		return pageBody{}, fmt.Errorf("sign ledger request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return pageBody{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return pageBody{}, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusBadRequest, http.StatusNotFound:
		// The request is refused for what it names (a ledger outside the
		// event's legal entity, or a malformed filter): retrying the same
		// event cannot change the answer, so it is dead-lettered at once
		// instead of holding its partition through every retry.
		return pageBody{}, eventing.Permanent(&Error{Status: resp.StatusCode})
	default:
		// 401/403 (seed or key not deployed yet), 429 and 5xx are retried.
		return pageBody{}, &Error{Status: resp.StatusCode}
	}
	var page pageBody
	if err := json.Unmarshal(raw, &page); err != nil {
		return pageBody{}, fmt.Errorf("decode ledger lines: %w", err)
	}
	return page, nil
}
