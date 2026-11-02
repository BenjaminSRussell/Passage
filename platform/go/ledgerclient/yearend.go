package ledgerclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/shopspring/decimal"
)

const (
	DutyChartRead = "ledger.account.lookup"

	CodeInProgress = "LEDGER_IDEMPOTENCY_IN_PROGRESS"

	chartsPath = "/v1/ledger/charts/"
	pageLimit  = "1000"
)

var ErrNotVisible = errors.New("ledger: not visible to the legal entity")

type Info struct {
	LegalEntityID       string
	ChartCode           string
	Currency            string
	DimensionAttributes []string
}

type ChartAccount struct {
	Type     string
	Postable bool
}

type BalanceRow struct {
	Account    string
	Dimensions map[string]string
	Net        decimal.Decimal
}

type TrialBalanceQuery struct {
	LegalEntityID string
	LedgerID      string
	PeriodFrom    string
	PeriodTo      string
	GroupBy       []string
}

type TrialBalance struct {
	Currency string
	Rows     []BalanceRow
}

// YearEndSource reads what a year-end close derives its entries from.
type YearEndSource interface {
	Ledger(ctx context.Context, legalEntityID, ledgerID string) (Info, error)
	Accounts(ctx context.Context, legalEntityID, chart string) (map[string]ChartAccount, error)
	TrialBalance(ctx context.Context, q TrialBalanceQuery) (TrialBalance, error)
}

// InProgress reports a 409 from the ledger for a request racing another with the same idempotency key.
func InProgress(err error) bool {
	var le *Error
	return errors.As(err, &le) && le.Status == http.StatusConflict && le.Code == CodeInProgress
}

func (c *Client) Ledger(ctx context.Context, legalEntityID, ledgerID string) (Info, error) {
	raw, err := c.do(ctx, "ledger", ledgerID, http.MethodGet, ledgersPath+url.PathEscape(ledgerID), nil, nil, "", legalEntityID, DutyLedgerRead)
	if err != nil {
		var le *Error
		if errors.As(err, &le) && (le.Status == http.StatusForbidden || le.Status == http.StatusNotFound) {
			return Info{}, ErrNotVisible
		}
		return Info{}, err
	}
	var body struct {
		LegalEntityID       string   `json:"legalEntityId"`
		ChartCode           string   `json:"chartCode"`
		Currency            string   `json:"currency"`
		DimensionAttributes []string `json:"dimensionAttributes"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.LegalEntityID == "" || body.ChartCode == "" || body.Currency == "" {
		return Info{}, errors.New("ledger: response has no legal entity, chart or currency")
	}
	return Info(body), nil
}

func (c *Client) Accounts(ctx context.Context, legalEntityID, chart string) (map[string]ChartAccount, error) {
	out := map[string]ChartAccount{}
	after := ""
	for {
		query := url.Values{"limit": {pageLimit}}
		if after != "" {
			query.Set("after", after)
		}
		raw, err := c.do(ctx, "accounts", chart, http.MethodGet, chartsPath+url.PathEscape(chart)+"/accounts", query, nil, "", legalEntityID, DutyChartRead)
		if err != nil {
			return nil, err
		}
		var page struct {
			Items []struct {
				AccountNumber string `json:"accountNumber"`
				AccountType   string `json:"accountType"`
				IsPostable    bool   `json:"isPostable"`
			} `json:"items"`
			NextAfter string `json:"nextAfter"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("ledger accounts: %w", err)
		}
		for _, a := range page.Items {
			out[a.AccountNumber] = ChartAccount{Type: a.AccountType, Postable: a.IsPostable}
		}
		if page.NextAfter == "" || page.NextAfter == after {
			return out, nil
		}
		after = page.NextAfter
	}
}

func (c *Client) TrialBalance(ctx context.Context, q TrialBalanceQuery) (TrialBalance, error) {
	query := url.Values{"ledger": {q.LedgerID}}
	if q.PeriodFrom != "" {
		query.Set("period_from", q.PeriodFrom)
	}
	if q.PeriodTo != "" {
		query.Set("period_to", q.PeriodTo)
	}
	if len(q.GroupBy) > 0 {
		query.Set("group_by", strings.Join(q.GroupBy, ","))
	}
	raw, err := c.do(ctx, "trial-balance", q.LedgerID, http.MethodGet, balancesPath, query, nil, "", q.LegalEntityID, DutyBalanceRead)
	if err != nil {
		return TrialBalance{}, err
	}
	var body struct {
		Currency string `json:"currency"`
		Rows     []struct {
			Account    string            `json:"account"`
			Debit      string            `json:"debit"`
			Credit     string            `json:"credit"`
			Dimensions map[string]string `json:"dimensions"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Currency == "" {
		return TrialBalance{}, errors.New("ledger trial balance: response has no currency")
	}
	out := TrialBalance{Currency: body.Currency}
	for _, r := range body.Rows {
		debit, derr := decimal.NewFromString(r.Debit)
		credit, cerr := decimal.NewFromString(r.Credit)
		if err := errors.Join(derr, cerr); err != nil {
			return TrialBalance{}, fmt.Errorf("ledger trial balance: %w", err)
		}
		dims := map[string]string{}
		for k, v := range r.Dimensions {
			if v != "" {
				dims[k] = v
			}
		}
		out.Rows = append(out.Rows, BalanceRow{Account: r.Account, Dimensions: dims, Net: debit.Sub(credit)})
	}
	return out, nil
}
