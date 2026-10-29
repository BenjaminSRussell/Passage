package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	Timeout         = 3 * time.Second
	balancesPath    = "/v1/ledger/balances"
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

func (c *Client) AccountBalance(ctx context.Context, ledgerID, account, periodTo string) (Balance, error) {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return Balance{}, errors.New("ledger balance: no principal in context")
	}
	q := url.Values{"ledger": {ledgerID}, "period_to": {periodTo}, "account_from": {account}, "account_to": {account}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+balancesPath+"?"+q.Encode(), nil)
	if err != nil {
		return Balance{}, fmt.Errorf("ledger balance: %w", err)
	}
	forward(req, principal)
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "ledger balance transport failed", "err", err, "account", account, "correlationId", middleware.GetReqID(ctx))
		return Balance{}, fmt.Errorf("ledger balance: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		slog.ErrorContext(ctx, "ledger balance read failed", "err", err, "account", account, "correlationId", middleware.GetReqID(ctx))
		return Balance{}, fmt.Errorf("ledger balance: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("ledger balance: unexpected status %d", resp.StatusCode)
		slog.ErrorContext(ctx, "ledger balance rejected", "err", err, "status", resp.StatusCode, "account", account, "correlationId", middleware.GetReqID(ctx))
		return Balance{}, err
	}
	return parse(ctx, raw, account)
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

func forward(req *http.Request, p tenant.Principal) {
	req.Header.Set(tenant.HeaderTenant, p.TenantID)
	if p.LegalEntityID != "" {
		req.Header.Set(tenant.HeaderLegalEntity, p.LegalEntityID)
	}
	if p.UserID != "" {
		req.Header.Set(tenant.HeaderUser, p.UserID)
	}
	if len(p.Duties) > 0 {
		req.Header.Set(tenant.HeaderDuties, strings.Join(slices.Sorted(maps.Keys(p.Duties)), ","))
	}
	if id := middleware.GetReqID(req.Context()); id != "" {
		req.Header.Set(middleware.RequestIDHeader, id)
	}
}
