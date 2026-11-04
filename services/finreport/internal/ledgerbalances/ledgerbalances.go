// Package ledgerbalances reads the trial balance of one ledger from the ledger
// service (GET /v1/ledger/balances, service duty ledger.balance.read). The
// balance verification (FR-15.1) compares it with the reporting read model.
package ledgerbalances

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

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	// Duty is the service-only ledger duty every request is signed with.
	Duty = "ledger.balance.read"

	audience        = "ledger"
	balancesPath    = "/v1/ledger/balances"
	maxResponseSize = 64 << 20
	timeout         = 30 * time.Second
)

var (
	ErrNoPrincipal = errors.New("ledgerbalances: no principal in context")
	// ErrSign is a service token finreport could not mint: no finreport to
	// ledger grant, or no single legal entity within the caller's scope.
	ErrSign = errors.New("ledgerbalances: cannot sign the ledger request")
	// ErrFiltered stops a ledger that answers with a partial trial balance.
	ErrFiltered = errors.New("ledgerbalances: the ledger answered a filtered trial balance")
	// ErrDuplicateAccount stops a ledger that lists one account twice, which a
	// plain trial balance never does.
	ErrDuplicateAccount = errors.New("ledgerbalances: the ledger listed an account twice")
)

// Error is a non-200 answer of the ledger.
type Error struct{ Status int }

func (e *Error) Error() string { return fmt.Sprintf("ledger balances: status %d", e.Status) }

// Client is the HTTP trial balance reader.
type Client struct {
	baseURL string
	http    *http.Client
	// sign puts the service token on req for the legal entity; tests replace
	// it because the finreport to ledger grant is seeded in the platform.
	sign func(req *http.Request, p tenant.Principal, legalEntityID string) error
}

// New builds the client; signer is required. A nil httpClient gets a 30 s timeout.
func New(baseURL string, signer *tenant.ServiceSigner, httpClient *http.Client) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("ledgerbalances: LEDGER_URL is required")
	}
	if signer == nil {
		return nil, errors.New("ledgerbalances: a service signing key is required (PASSAGE_SERVICE_SIGNING_KEY_FILE)")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	c := *httpClient
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &c, sign: func(req *http.Request, p tenant.Principal, le string) error {
		_, err := signer.ApplyDuties(req, p, audience, le, []string{Duty})
		return err
	}}, nil
}

type balanceBody struct {
	LedgerID string `json:"ledgerId"`
	Rows     []struct {
		Account string `json:"account"`
		Net     string `json:"net"`
	} `json:"rows"`
	Filtered bool `json:"filtered"`
}

// Nets returns the net (debit minus credit, accounting currency) per account of
// the ledger over every period, acting for the calling user within legalEntityID.
func (c *Client) Nets(ctx context.Context, ledgerID, legalEntityID string) (map[string]decimal.Decimal, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return nil, ErrNoPrincipal
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+balancesPath+"?"+url.Values{"ledger": {ledgerID}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if err := c.sign(req, p, legalEntityID); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSign, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &Error{Status: resp.StatusCode}
	}
	var body balanceBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("decode ledger balances: %w", err)
	}
	if body.Filtered {
		return nil, ErrFiltered
	}
	if body.Rows == nil || !strings.EqualFold(body.LedgerID, ledgerID) {
		return nil, fmt.Errorf("ledger balances: the answer is not the trial balance of ledger %s", ledgerID)
	}
	out := make(map[string]decimal.Decimal, len(body.Rows))
	for _, r := range body.Rows {
		n, err := decimal.NewFromString(r.Net)
		if err != nil {
			return nil, fmt.Errorf("ledger account %s: net %q: %w", r.Account, r.Net, err)
		}
		if _, dup := out[r.Account]; dup {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateAccount, r.Account)
		}
		out[r.Account] = n
	}
	return out, nil
}
