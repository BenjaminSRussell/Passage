package recon

import (
	"context"
	"encoding/json"
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
	ledgerAudience   = "ledger"
	dutyEntryLookup  = "ledger.entry.lookup"
	dutyLedgerLookup = "ledger.ledger.lookup"
	linesPath        = "/v1/ledger/journal-lines"
	ledgersPath      = "/v1/ledger/ledgers/"
	linePageSize     = "1000"
	maxLedgerBody    = 8 << 20
	maxLedgerPages   = 1000
	ledgerHTTPBudget = 30 * time.Second
)

type LedgerRef struct {
	LegalEntityID string
	LedgerID      string
}

type LedgerReader struct {
	baseURL     string
	signer      *tenant.ServiceSigner
	http        *http.Client
	Ledgers     []LedgerRef
	AccountFrom string
	AccountTo   string
}

func NewLedgerReader(baseURL string, signer *tenant.ServiceSigner, client *http.Client, ledgers []LedgerRef, accountFrom, accountTo string) *LedgerReader {
	if client == nil {
		client = &http.Client{Timeout: ledgerHTTPBudget}
	}
	return &LedgerReader{baseURL: strings.TrimRight(baseURL, "/"), signer: signer, http: client, Ledgers: ledgers, AccountFrom: accountFrom, AccountTo: accountTo}
}

func (l *LedgerReader) get(ctx context.Context, legalEntityID, target string, duty string, into any) error {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return fmt.Errorf("ledger: no principal in context")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.baseURL+target, nil)
	if err != nil {
		return err
	}
	signed, err := l.signer.ApplyDuties(req, principal, ledgerAudience, legalEntityID, []string{duty})
	if err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	if !signed {
		return fmt.Errorf("ledger: no service signing key configured")
	}
	resp, err := l.http.Do(req)
	if err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxLedgerBody))
	if err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ledger: unexpected status %d for %s", resp.StatusCode, req.URL.Path)
	}
	return json.Unmarshal(raw, into)
}

func (l *LedgerReader) Revenue(ctx context.Context, day string) ([]GLRevenue, error) {
	out := make([]GLRevenue, 0, len(l.Ledgers))
	for _, ref := range l.Ledgers {
		var ledger struct {
			Currency string `json:"currency"`
		}
		if err := l.get(ctx, ref.LegalEntityID, ledgersPath+url.PathEscape(ref.LedgerID), dutyLedgerLookup, &ledger); err != nil {
			return nil, err
		}
		total := decimal.Zero
		cursor := ""
		for page := 0; ; page++ {
			if page == maxLedgerPages {
				return nil, fmt.Errorf("ledger: more than %d pages of revenue lines for %s", maxLedgerPages, ref.LedgerID)
			}
			query := url.Values{"ledger": {ref.LedgerID}, "account_from": {l.AccountFrom}, "account_to": {l.AccountTo},
				"date_from": {day}, "date_to": {day}, "limit": {linePageSize}}
			if cursor != "" {
				query.Set("cursor", cursor)
			}
			var lines struct {
				Items []struct {
					DebitCredit string `json:"debitCredit"`
					Amount      string `json:"amountAcct"`
				} `json:"items"`
				NextCursor string `json:"nextCursor"`
			}
			if err := l.get(ctx, ref.LegalEntityID, linesPath+"?"+query.Encode(), dutyEntryLookup, &lines); err != nil {
				return nil, err
			}
			for _, item := range lines.Items {
				amount, err := decimal.NewFromString(item.Amount)
				if err != nil {
					return nil, fmt.Errorf("ledger: line amount %q: %w", item.Amount, err)
				}
				if item.DebitCredit == "C" {
					total = total.Add(amount)
				} else {
					total = total.Sub(amount)
				}
			}
			if lines.NextCursor == "" {
				break
			}
			cursor = lines.NextCursor
		}
		out = append(out, GLRevenue{LegalEntityID: ref.LegalEntityID, Currency: ledger.Currency, Amount: total})
	}
	return out, nil
}
