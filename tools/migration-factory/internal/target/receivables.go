// Package target is the migration factory's client for the services it loads into. Today that is the
// receivables service: the open_items pack is sent to POST /v1/receivables/open-items:batch and the target's own
// totals are read from GET /v1/receivables/open-item-imports. The factory signs as its own service
// (PASSAGE_SERVICE_NAME=migration-factory, its own key, never the migration service's) and acts as the existing
// service:migration-loader principal in the run's legal entity. Each token carries one duty of the grant: the
// batch only receivables.import.write, the totals read only ARView. Every attempt carries a freshly signed token
// (a token id is single use).
package target

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

const (
	// ServiceName is the caller name the service tokens are signed under; the grant tables name it.
	ServiceName = "migration-factory"
	// Principal is the service principal the factory acts as (it already holds receivables.import.write).
	Principal = "migration-loader"

	audReceivables = "receivables"
	dutyImport     = "receivables.import.write"
	dutyRead       = "ARView"

	defaultAttempts = 5
	defaultBackoff  = time.Second
	maxBackoff      = 60 * time.Second
	defaultTimeout  = 60 * time.Second
	maxBody         = 8 << 20
	maxDetail       = 300
	// importsPage is the largest page GET /open-item-imports serves (receivables store.MaxPageSize). The route
	// has no cursor and no import-source filter, so a full page may have left this source's groups out.
	importsPage = 500
)

var (
	// ErrNotConfigured means no receivables address or no service signing key is set.
	ErrNotConfigured = errors.New("the receivables target is not configured")
	// ErrDenied means receivables refused the factory's service token (401 or 403): the grant or the key is missing.
	ErrDenied = errors.New("receivables refused the factory's service token")
	// ErrUnavailable means receivables kept answering 5xx or 429, or not at all, through every attempt.
	ErrUnavailable = errors.New("receivables did not answer")
	// ErrRefused means receivables refused a whole request (400 or another 4xx): a defect in what was sent.
	ErrRefused = errors.New("receivables refused the request")
	// ErrBadAnswer means a 2xx answer that does not describe the batch sent.
	ErrBadAnswer = errors.New("receivables answered something other than the batch result")
)

// Error carries the target's answer behind one of the sentinel errors above.
type Error struct {
	Kind   error
	Status int
	Code   string
	Detail string
}

func (e *Error) Error() string {
	parts := []string{e.Kind.Error()}
	if e.Status != 0 {
		parts = append(parts, "status "+strconv.Itoa(e.Status))
	}
	if e.Code != "" {
		parts = append(parts, e.Code)
	}
	if e.Detail != "" {
		parts = append(parts, e.Detail)
	}
	return strings.Join(parts, ": ")
}

func (e *Error) Unwrap() error { return e.Kind }

// Signer signs a request for a target service; *tenant.ServiceSigner is the production one.
type Signer interface {
	ApplyDuties(req *http.Request, p tenant.Principal, aud, legalEntity string, only []string) (bool, error)
}

// Config says where receivables is and how to sign for it.
type Config struct {
	BaseURL string
	Signer  Signer
	Client  *http.Client
	// Attempts is the most tries per call (default 5); Backoff the wait after the first (default 1s, doubling, at
	// most 60s; a Retry-After header longer than that wait wins, up to 60s).
	Attempts int
	Backoff  time.Duration
	// Sleep waits between attempts; tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Receivables is the client.
type Receivables struct {
	cfg    Config
	client *http.Client
}

// NewReceivables builds the client. A zero Config is valid and reports Configured false.
func NewReceivables(cfg Config) *Receivables {
	if cfg.Attempts < 1 {
		cfg.Attempts = defaultAttempts
	}
	if cfg.Backoff <= 0 {
		cfg.Backoff = defaultBackoff
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleep
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Receivables{cfg: cfg, client: client}
}

// Configured reports whether a load can be attempted: an address and a signing key.
func (r *Receivables) Configured() bool {
	return r != nil && r.cfg.BaseURL != "" && r.cfg.Signer != nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// RowResult is the target's outcome for one row of a batch.
type RowResult struct {
	Index      int    `json:"index"`
	LegacyKey  string `json:"legacyKey"`
	Outcome    string `json:"outcome"`
	OpenItemID string `json:"openItemId"`
	Code       string `json:"code"`
	Detail     string `json:"detail"`
}

// Outcomes receivables reports per row.
const (
	OutcomeCreated   = "CREATED"
	OutcomeUpdated   = "UPDATED"
	OutcomeUnchanged = "UNCHANGED"
	OutcomeRejected  = "REJECTED"
)

type batchRequest struct {
	LegalEntityID string                  `json:"legalEntityId"`
	ImportSource  string                  `json:"importSource"`
	Atomic        bool                    `json:"atomic"`
	Items         []domain.ReceivablesRow `json:"items"`
}

type batchResponse struct {
	Results []RowResult `json:"results"`
}

// LoadBatch sends up to 1000 rows of one legal entity and returns one result per row, in order. idemKey is sent as
// the Idempotency-Key and stays the same for the same rows, so a retry (here or by a later load) is the same
// request; the target keys rows by (importSource, legacyKey) and answers a repeat as UNCHANGED. A 5xx, a 429, a
// timeout or a dropped connection is retried with backoff; when the attempts are used up the error is
// ErrUnavailable and nothing is known about the batch.
func (r *Receivables) LoadBatch(ctx context.Context, tenantID, legalEntityID, importSource, idemKey string, rows []domain.ReceivablesRow) ([]RowResult, error) {
	if !r.Configured() {
		return nil, ErrNotConfigured
	}
	if len(rows) == 0 || len(rows) > domain.MaxBatchRows {
		return nil, fmt.Errorf("a batch holds 1 to %d rows, got %d", domain.MaxBatchRows, len(rows))
	}
	payload, err := json.Marshal(batchRequest{LegalEntityID: legalEntityID, ImportSource: importSource, Atomic: false, Items: rows})
	if err != nil {
		return nil, err
	}
	status, body, err := r.do(ctx, http.MethodPost, "/v1/receivables/open-items:batch", tenantID, legalEntityID, dutyImport, idemKey, payload)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return nil, answerError(status, body)
	}
	var out batchResponse
	if json.Unmarshal(body, &out) != nil || len(out.Results) != len(rows) {
		return nil, &Error{Kind: ErrBadAnswer, Status: status, Detail: fmt.Sprintf("%d results for %d rows", len(out.Results), len(rows))}
	}
	ordered := make([]RowResult, len(rows))
	seen := make([]bool, len(rows))
	for _, res := range out.Results {
		if res.Index < 0 || res.Index >= len(rows) || seen[res.Index] || res.LegacyKey != rows[res.Index].LegacyKey {
			return nil, &Error{Kind: ErrBadAnswer, Status: status, Detail: "a result does not belong to the batch sent"}
		}
		seen[res.Index] = true
		ordered[res.Index] = res
	}
	return ordered, nil
}

// ImportSummary is one row of GET /v1/receivables/open-item-imports.
type ImportSummary struct {
	ImportSource  string          `json:"importSource"`
	LegalEntityID string          `json:"legalEntityId"`
	Currency      string          `json:"currency"`
	ItemCount     int             `json:"itemCount"`
	DebitCount    int             `json:"debitCount"`
	CreditCount   int             `json:"creditCount"`
	DebitOpen     decimal.Decimal `json:"debitOpen"`
	CreditOpen    decimal.Decimal `json:"creditOpen"`
	NetOpen       decimal.Decimal `json:"netOpen"`
}

// ImportTotals reads what receivables holds for the import source in the legal entity, per currency. The token is
// scoped to the legal entity, so receivables' row-level security returns that entity's groups only. Receivables
// totals every item ever loaded under the import source (by any run), as Passage holds it now. A full page is
// refused as unreadable: a currency missing from it could be beyond the page, and reading it as zero would
// report a false mismatch (or hide a missing currency).
func (r *Receivables) ImportTotals(ctx context.Context, tenantID, legalEntityID, importSource string) ([]domain.ImportTotals, error) {
	if !r.Configured() {
		return nil, ErrNotConfigured
	}
	status, body, err := r.do(ctx, http.MethodGet, "/v1/receivables/open-item-imports?limit="+strconv.Itoa(importsPage), tenantID, legalEntityID, dutyRead, "", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, answerError(status, body)
	}
	var out struct {
		Items []ImportSummary `json:"items"`
	}
	if json.Unmarshal(body, &out) != nil {
		return nil, &Error{Kind: ErrBadAnswer, Status: status, Detail: "the import totals are not readable"}
	}
	if len(out.Items) >= importsPage {
		return nil, &Error{Kind: ErrBadAnswer, Status: status,
			Detail: fmt.Sprintf("receivables lists %d or more import groups in this legal entity and cannot be filtered by import source; the totals cannot be read completely", importsPage)}
	}
	var totals []domain.ImportTotals
	for _, s := range out.Items {
		if s.ImportSource != importSource || !strings.EqualFold(s.LegalEntityID, legalEntityID) {
			continue
		}
		totals = append(totals, domain.ImportTotals{Currency: s.Currency, ItemCount: s.ItemCount, DebitOpen: s.DebitOpen, CreditOpen: s.CreditOpen, NetOpen: s.NetOpen})
	}
	return totals, nil
}

type problemBody struct {
	Code   string            `json:"code"`
	Detail string            `json:"detail"`
	Title  string            `json:"title"`
	Errors map[string]string `json:"errors"`
}

func answerError(status int, body []byte) error {
	var p problemBody
	_ = json.Unmarshal(body, &p)
	detail := p.Detail
	if detail == "" {
		detail = p.Title
	}
	if len(p.Errors) > 0 {
		keys := make([]string, 0, len(p.Errors))
		for k := range p.Errors {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			detail += "; " + k + ": " + p.Errors[k]
		}
	}
	kind := ErrRefused
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		kind = ErrDenied
	}
	return &Error{Kind: kind, Status: status, Code: p.Code, Detail: clip(detail)}
}

func clip(s string) string {
	if len(s) > maxDetail {
		return s[:maxDetail] + "..."
	}
	return s
}

// do sends one request with retries. A 5xx, a 429, a timeout or a connection failure is retried with exponential
// backoff (the Retry-After of a 429 or 503 wins when it is longer); every other answer is final.
func (r *Receivables) do(ctx context.Context, method, path, tenantID, legalEntityID, duty, idemKey string, payload []byte) (int, []byte, error) {
	principal, err := tenant.ServicePrincipal(Principal, tenantID, legalEntityID)
	if err != nil {
		return 0, nil, err
	}
	var last string
	for attempt := 1; attempt <= r.cfg.Attempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, r.cfg.BaseURL+path, bytes.NewReader(payload))
		if err != nil {
			return 0, nil, err
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if idemKey != "" {
			req.Header.Set("Idempotency-Key", idemKey)
		}
		if _, err := r.cfg.Signer.ApplyDuties(req, principal, audReceivables, legalEntityID, []string{duty}); err != nil {
			return 0, nil, &Error{Kind: ErrDenied, Detail: clip("cannot sign for receivables with " + duty + ": " + err.Error())}
		}
		var wait time.Duration
		resp, err := r.client.Do(req)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return 0, nil, ctx.Err()
			}
			last = "no answer from the receivables service"
		default:
			body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxBody))
			_ = resp.Body.Close()
			if rerr == nil && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
				return resp.StatusCode, body, nil
			}
			last = fmt.Sprintf("the receivables service answered %d", resp.StatusCode)
			if rerr != nil {
				last = "the receivables answer was cut off"
			}
			if secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && secs > 0 {
				wait = time.Duration(secs) * time.Second
			}
		}
		if attempt < r.cfg.Attempts {
			backoff := min(r.cfg.Backoff<<(attempt-1), maxBackoff)
			if wait < backoff {
				wait = backoff
			}
			if err := r.cfg.Sleep(ctx, min(wait, maxBackoff)); err != nil {
				return 0, nil, err
			}
		}
	}
	return 0, nil, &Error{Kind: ErrUnavailable, Detail: clip(fmt.Sprintf("%s; gave up after %d attempts", last, r.cfg.Attempts))}
}
