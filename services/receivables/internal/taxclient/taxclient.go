package taxclient

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
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	timeout          = 3 * time.Second
	audience         = "tax"
	dutyRecord       = "tax.transaction.record"
	transactionsPath = "/v1/tax/transactions"
)

var ErrUnsigned = errors.New("tax client needs the service signing key")

type Txn struct {
	ID, Code, Date string
	Amount         decimal.Decimal
}

type Recorder interface {
	Record(ctx context.Context, legalEntityID string, txns []Txn) error
}

type StatusError struct{ Status int }

func (e *StatusError) Error() string { return fmt.Sprintf("tax: unexpected status %d", e.Status) }

func Rejected(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status >= 400 && se.Status < 500 && se.Status != http.StatusRequestTimeout && se.Status != http.StatusTooManyRequests
}

type Client struct {
	baseURL string
	http    *http.Client
	signer  *tenant.ServiceSigner
}

func FromEnv(getenv func(string) string) (*Client, error) {
	baseURL := getenv("TAX_URL")
	if baseURL == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, ErrUnsigned
	}
	d := timeout
	if v, err := time.ParseDuration(getenv("TAX_TIMEOUT")); err == nil && v > 0 {
		d = v
	}
	noRedirect := http.Client{Timeout: d, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &noRedirect, signer: signer}, nil
}

type txnBody struct {
	ID        string `json:"id"`
	Code      string `json:"code"`
	Direction string `json:"direction"`
	TaxDate   string `json:"tax_date"`
	Amount    string `json:"amount"`
}

func (c *Client) Record(ctx context.Context, legalEntityID string, txns []Txn) error {
	principal, ok := tenant.FromContext(ctx)
	if !ok {
		return errors.New("tax: no principal in context")
	}
	body := struct {
		Transactions []txnBody `json:"transactions"`
	}{}
	for _, t := range txns {
		body.Transactions = append(body.Transactions, txnBody{ID: t.ID, Code: t.Code, Direction: "output", TaxDate: t.Date, Amount: t.Amount.String()})
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("tax: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+transactionsPath, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("tax: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if _, err := c.signer.ApplyDuties(req, principal, audience, legalEntityID, []string{dutyRecord}); err != nil {
		return fmt.Errorf("tax: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "tax request failed", "err", err, "correlationId", middleware.GetReqID(ctx))
		return fmt.Errorf("tax: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		slog.ErrorContext(ctx, "tax rejected the transactions", "status", resp.StatusCode, "correlationId", middleware.GetReqID(ctx))
		return &StatusError{Status: resp.StatusCode}
	}
	return nil
}

type Fake struct {
	mu   sync.Mutex
	Err  error
	byID map[string]Txn
	LE   map[string]string
}

func NewFake() *Fake { return &Fake{byID: map[string]Txn{}, LE: map[string]string{}} }

func (f *Fake) Record(_ context.Context, legalEntityID string, txns []Txn) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return f.Err
	}
	for _, t := range txns {
		f.byID[t.ID] = t
		f.LE[t.ID] = legalEntityID
	}
	return nil
}

func (f *Fake) Recorded() []Txn {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Txn, 0, len(f.byID))
	for _, t := range f.byID {
		out = append(out, t)
	}
	return out
}
