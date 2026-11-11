// Package salesreport reports posted invoices to sales (#29): the post queues one report per (invoice, order) in its own
// transaction, and the Reporter sends the due ones to POST /v1/sales/orders/{id}:record-progress (kind INVOICED) outside any
// post, so a sales outage never blocks invoicing. Calls are signed as billing with only sales.order.record-invoice, on behalf of
// the service principal service:billing-sales-report, for the invoice's tenant and legal entity. The Reporter finds the tenants
// with due reports through a SECURITY DEFINER index, leases a batch per tenant, and records each attempt as reported, retried
// with back-off or parked.
package salesreport

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
	"sync/atomic"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

const (
	PrincipalName     = "billing-sales-report"
	DutyRecordInvoice = "sales.order.record-invoice"
	audience          = "sales"
	defaultTimeout    = 10 * time.Second
	maxResponseSize   = 64 << 10
	maxErrorDetail    = 500
)

const (
	DefaultLease       = 2 * time.Minute
	DefaultBatch       = int(DefaultLease / (2 * defaultTimeout))
	DefaultMaxAttempts = 20
	DefaultInterval    = 30 * time.Second
	MinInterval        = time.Second
	maxBackoff         = time.Hour
)

// Backoff is the wait before attempt n+1 after n failed attempts: 30 seconds doubling, at most an hour.
func Backoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	if n > 8 {
		return maxBackoff
	}
	return min(30*time.Second<<(n-1), maxBackoff)
}

// Answer is what sales answered: Status 0 means no answer (transport failure).
type Answer struct {
	Status int
	Detail string
}

type Sender interface {
	Send(ctx context.Context, legalEntityID, orderID, key string, body []byte) (Answer, error)
}

// Client is the sales client. It signs every call with the principal in ctx as actor and only DutyRecordInvoice.
type Client struct {
	baseURL string
	signer  *tenant.ServiceSigner
	http    *http.Client
}

func New(baseURL string, signer *tenant.ServiceSigner, hc *http.Client) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("sales client needs a base URL")
	}
	if signer == nil {
		return nil, errors.New("sales client needs a service signing key")
	}
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), signer: signer, http: hc}, nil
}

// FromEnv builds the client from SALES_URL and the service signing key. It returns nil, nil without SALES_URL (nothing is
// reported; the reports wait, PENDING); with it a signing key is required.
func FromEnv(getenv func(string) string) (*Client, error) {
	base := getenv("SALES_URL")
	if base == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("SALES_URL needs PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE")
	}
	return New(base, signer, nil)
}

// Send posts body to the order's :record-progress with the Idempotency-Key. An error means no answer was read (the call may or
// may not have reached sales; a retry with the same key and body is safe).
func (c *Client) Send(ctx context.Context, legalEntityID, orderID, key string, body []byte) (Answer, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return Answer{}, errors.New("sales: no principal in context")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/sales/orders/"+url.PathEscape(orderID)+":record-progress", bytes.NewReader(body))
	if err != nil {
		return Answer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	if _, err := c.signer.ApplyDuties(req, p, audience, legalEntityID, []string{DutyRecordInvoice}); err != nil {
		return Answer{}, fmt.Errorf("sign sales request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Answer{}, fmt.Errorf("sales request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return Answer{}, fmt.Errorf("read sales response: %w", err)
	}
	return Answer{Status: resp.StatusCode, Detail: detail(raw)}, nil
}

func detail(raw []byte) string {
	var p struct {
		Code   string            `json:"code"`
		Detail string            `json:"detail"`
		Errors map[string]string `json:"errors"`
	}
	s := strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &p) == nil && (p.Code != "" || p.Detail != "") {
		s = strings.TrimSpace(p.Code + " " + p.Detail)
		keys := make([]string, 0, len(p.Errors))
		for k := range p.Errors {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			s += "; " + k + ": " + p.Errors[k]
		}
	}
	return s[:min(len(s), maxErrorDetail)]
}

type Store interface {
	SalesReportDueTenants(ctx context.Context) ([]string, error)
	ClaimSalesReports(ctx context.Context, limit int, lease time.Duration) ([]store.ClaimedSalesReport, error)
	FinishSalesReport(ctx context.Context, id string, attempts int, o store.SalesReportOutcome) error
}

// Reporter sends due reports. Zero fields take the defaults.
type Reporter struct {
	Store       Store
	Sales       Sender
	Batch       int
	Lease       time.Duration
	MaxAttempts int
	Now         func() time.Time

	parked atomic.Int64
}

// WriteMetrics appends the reporter's counters to a Prometheus text response.
func (r *Reporter) WriteMetrics(w io.Writer) {
	fmt.Fprintf(w, "# HELP billing_sales_reports_parked_total Invoice reports to sales parked by this process: refused by sales or out of attempts.\n# TYPE billing_sales_reports_parked_total counter\nbilling_sales_reports_parked_total %d\n", r.parked.Load())
}

func (r *Reporter) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reporter) limits() (int, time.Duration, int) {
	batch, lease, attempts := r.Batch, r.Lease, r.MaxAttempts
	if batch <= 0 {
		batch = DefaultBatch
	}
	if lease <= 0 {
		lease = DefaultLease
	}
	if attempts <= 0 {
		attempts = DefaultMaxAttempts
	}
	return batch, lease, attempts
}

// RunOnce sends every report due now, tenant by tenant, and returns how many attempts it made. A failing tenant does not stop
// the others.
func (r *Reporter) RunOnce(ctx context.Context) (int, error) {
	tenants, err := r.Store.SalesReportDueTenants(ctx)
	if err != nil {
		return 0, err
	}
	batch, lease, _ := r.limits()
	n := 0
	var failed []error
	for _, tid := range tenants {
		tctx := store.SalesReportSystemContext(ctx, tid)
		// A batch is sized to finish inside its lease, so a backlog is drained one batch after another within the
		// tick (at most maxBatchesPerTick) instead of one batch per interval; a claimed report is out of reach of the
		// next claim (its next attempt moves past the lease), so no report is taken twice.
		for page := 0; page < maxBatchesPerTick && ctx.Err() == nil; page++ {
			claimed, err := r.Store.ClaimSalesReports(tctx, batch, lease)
			if err != nil {
				failed = append(failed, fmt.Errorf("tenant %s: %w", tid, err))
				break
			}
			n += r.sendClaimed(ctx, tctx, tid, claimed, &failed)
			if len(claimed) < batch {
				break
			}
		}
	}
	return n, errors.Join(failed...)
}

// maxBatchesPerTick bounds how long one tick spends on one tenant's backlog.
const maxBatchesPerTick = 50

// sendClaimed attempts each claimed report, records its outcome and returns how many attempts it made.
func (r *Reporter) sendClaimed(ctx, tctx context.Context, tid string, claimed []store.ClaimedSalesReport, failed *[]error) int {
	n := 0
	for _, c := range claimed {
		o := r.attempt(ctx, tid, c)
		err := r.Store.FinishSalesReport(tctx, c.ID, c.Attempts, o)
		if errors.Is(err, store.ErrStaleSalesReport) {
			slog.WarnContext(ctx, "sales invoice report lease lost; the newer attempt records the outcome", "tenant", tid, "report", c.ID, "attempt", c.Attempts)
			continue
		}
		if err != nil {
			*failed = append(*failed, fmt.Errorf("tenant %s report %s: %w", tid, c.ID, err))
			continue
		}
		level := slog.LevelInfo
		if o.Status != store.SalesReportReported {
			level = slog.LevelWarn
		}
		if o.Status == store.SalesReportParked {
			r.parked.Add(1)
		}
		slog.Log(ctx, level, "sales invoice report", "tenant", tid, "report", c.ID, "order", c.OrderID, "sourceRef", c.SourceRef,
			"attempt", c.Attempts, "status", o.Status, "httpStatus", o.HTTPStatus, "err", o.Error)
		n++
	}
	return n
}

// attempt sends one claimed report and says what to record: REPORTED on 2xx; PARKED on a refusal (4xx other than a refused
// token or throttling), which no retry of the same body can change; otherwise PENDING with a back-off, and PARKED once the
// attempts run out.
func (r *Reporter) attempt(ctx context.Context, tid string, c store.ClaimedSalesReport) store.SalesReportOutcome {
	principal, err := tenant.ServicePrincipal(PrincipalName, tid, c.LegalEntityID)
	if err != nil {
		return store.SalesReportOutcome{Status: store.SalesReportParked, Error: "service principal: " + err.Error()}
	}
	ans, err := r.Sales.Send(tenant.With(ctx, principal), c.LegalEntityID, c.OrderID, c.IdempotencyKey, c.Body)
	switch {
	case err == nil && ans.Status >= 200 && ans.Status < 300:
		return store.SalesReportOutcome{Status: store.SalesReportReported, HTTPStatus: ans.Status}
	case err == nil && refused(ans):
		return store.SalesReportOutcome{Status: store.SalesReportParked, HTTPStatus: ans.Status, Error: fmt.Sprintf("sales refused the invoice report (%d): %s", ans.Status, ans.Detail)}
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	} else {
		msg = fmt.Sprintf("sales answered %d: %s", ans.Status, ans.Detail)
	}
	if _, _, attempts := r.limits(); c.Attempts >= attempts {
		return store.SalesReportOutcome{Status: store.SalesReportParked, HTTPStatus: ans.Status, Error: fmt.Sprintf("gave up after %d attempts: %s", c.Attempts, msg)}
	}
	return store.SalesReportOutcome{Status: store.SalesReportPending, RetryAt: r.now().Add(Backoff(c.Attempts)), HTTPStatus: ans.Status, Error: msg}
}

// refused reports the answers a retry of the same body cannot change: sales understood the report and refused it (validation,
// unknown order or line). Not yet: SLS-0058 (the order is not shipped) and SLS-0059 (the invoice is above what was delivered)
// pass once fulfillment's delivery report arrives, which travels separately and may come later. A refused token (401, 403), a
// timeout (408) and throttling (429) pass once the deployment is fixed or the load falls, and 5xx is an outage; those are
// retried with backoff and park only when the attempts run out.
func refused(ans Answer) bool {
	if ans.Status < 400 || ans.Status >= 500 {
		return false
	}
	switch ans.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false
	}
	return !strings.HasPrefix(ans.Detail, "SLS-0058") && !strings.HasPrefix(ans.Detail, "SLS-0059")
}

// Run calls once every interval until ctx ends.
func Run(ctx context.Context, interval time.Duration, once func(context.Context) (int, error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := once(ctx); err != nil && ctx.Err() == nil {
			slog.Error("billing sales invoice reporter run failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
