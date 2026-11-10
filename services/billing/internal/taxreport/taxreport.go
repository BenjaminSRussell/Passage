// Package taxreport records the output tax of booked invoices and credit notes with the tax service (#29): booking a document to
// the GL queues one report in its own transaction, and the Reporter sends the due ones to POST /v1/tax/transactions outside any
// booking, so a tax outage never blocks the ledger. Calls are signed as billing with only tax.transaction.record, on behalf of the
// service principal service:billing-tax-report, for the document's tenant and legal entity. Leasing, back-off and parking follow
// salesreport.
package taxreport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/salesreport"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

const (
	PrincipalName   = "billing-tax-report"
	DutyRecord      = "tax.transaction.record"
	audience        = "tax"
	transactions    = "/v1/tax/transactions"
	defaultTimeout  = 10 * time.Second
	maxResponseSize = 64 << 10
	maxErrorDetail  = 500
	DefaultBatch    = int(salesreport.DefaultLease / (2 * defaultTimeout))
)

// Answer is what the tax service answered: Status 0 means no answer (transport failure).
type Answer struct {
	Status int
	Detail string
}

type Sender interface {
	Send(ctx context.Context, legalEntityID string, body []byte) (Answer, error)
}

// Client is the tax client. It signs every call with the principal in ctx as actor and only DutyRecord.
type Client struct {
	baseURL string
	signer  *tenant.ServiceSigner
	http    *http.Client
}

func New(baseURL string, signer *tenant.ServiceSigner, hc *http.Client) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("tax client needs a base URL")
	}
	if signer == nil {
		return nil, errors.New("tax client needs a service signing key")
	}
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), signer: signer, http: hc}, nil
}

// FromEnv builds the client from TAX_URL and the service signing key. It returns nil, nil without TAX_URL (nothing is sent; the
// reports wait, PENDING); with it a signing key is required.
func FromEnv(getenv func(string) string) (*Client, error) {
	base := getenv("TAX_URL")
	if base == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("TAX_URL needs PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE")
	}
	return New(base, signer, nil)
}

// Send posts body to the tax service. An error means no answer was read; a retry with the same body is safe because tax records a
// transaction id once and answers a repeat as a duplicate.
func (c *Client) Send(ctx context.Context, legalEntityID string, body []byte) (Answer, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return Answer{}, errors.New("tax: no principal in context")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+transactions, bytes.NewReader(body))
	if err != nil {
		return Answer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if _, err := c.signer.ApplyDuties(req, p, audience, legalEntityID, []string{DutyRecord}); err != nil {
		return Answer{}, fmt.Errorf("sign tax request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Answer{}, fmt.Errorf("tax request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return Answer{}, fmt.Errorf("read tax response: %w", err)
	}
	s := strings.TrimSpace(string(raw))
	return Answer{Status: resp.StatusCode, Detail: s[:min(len(s), maxErrorDetail)]}, nil
}

type Store interface {
	TaxReportDueTenants(ctx context.Context) ([]string, error)
	ClaimTaxReports(ctx context.Context, limit int, lease time.Duration) ([]store.ClaimedTaxReport, error)
	FinishTaxReport(ctx context.Context, id string, attempts int, o store.TaxReportOutcome) error
}

// Reporter sends due reports. Zero fields take the defaults.
type Reporter struct {
	Store       Store
	Tax         Sender
	Batch       int
	Lease       time.Duration
	MaxAttempts int
	Now         func() time.Time

	parked atomic.Int64
}

// WriteMetrics appends the reporter's counters to a Prometheus text response.
func (r *Reporter) WriteMetrics(w io.Writer) {
	fmt.Fprintf(w, "# HELP billing_tax_reports_parked_total Tax transaction reports parked by this process: refused by the tax service or out of attempts.\n# TYPE billing_tax_reports_parked_total counter\nbilling_tax_reports_parked_total %d\n", r.parked.Load())
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
		lease = salesreport.DefaultLease
	}
	if attempts <= 0 {
		attempts = salesreport.DefaultMaxAttempts
	}
	return batch, lease, attempts
}

// RunOnce sends every report due now, tenant by tenant, and returns how many attempts it made. A failing tenant does not stop
// the others.
func (r *Reporter) RunOnce(ctx context.Context) (int, error) {
	tenants, err := r.Store.TaxReportDueTenants(ctx)
	if err != nil {
		return 0, err
	}
	batch, lease, _ := r.limits()
	n := 0
	var failed []error
	for _, tid := range tenants {
		tctx := store.TaxReportSystemContext(ctx, tid)
		claimed, err := r.Store.ClaimTaxReports(tctx, batch, lease)
		if err != nil {
			failed = append(failed, fmt.Errorf("tenant %s: %w", tid, err))
			continue
		}
		for _, c := range claimed {
			o := r.attempt(ctx, tid, c)
			err := r.Store.FinishTaxReport(tctx, c.ID, c.Attempts, o)
			if errors.Is(err, store.ErrStaleTaxReport) {
				slog.WarnContext(ctx, "tax transaction report lease lost; the newer attempt records the outcome", "tenant", tid, "report", c.ID, "attempt", c.Attempts)
				continue
			}
			if err != nil {
				failed = append(failed, fmt.Errorf("tenant %s report %s: %w", tid, c.ID, err))
				continue
			}
			level := slog.LevelInfo
			if o.Status != store.TaxReportReported {
				level = slog.LevelWarn
			}
			if o.Status == store.TaxReportParked {
				r.parked.Add(1)
			}
			slog.Log(ctx, level, "tax transaction report", "tenant", tid, "report", c.ID, "sourceRef", c.SourceRef,
				"attempt", c.Attempts, "status", o.Status, "httpStatus", o.HTTPStatus, "err", o.Error)
			n++
		}
	}
	return n, errors.Join(failed...)
}

// attempt sends one claimed report and says what to record: REPORTED on 2xx; PARKED on a refusal (4xx other than a refused token,
// a timeout or throttling), which no retry of the same body can change; otherwise PENDING with a back-off, and PARKED once the
// attempts run out.
func (r *Reporter) attempt(ctx context.Context, tid string, c store.ClaimedTaxReport) store.TaxReportOutcome {
	principal, err := tenant.ServicePrincipal(PrincipalName, tid, c.LegalEntityID)
	if err != nil {
		return store.TaxReportOutcome{Status: store.TaxReportParked, Error: "service principal: " + err.Error()}
	}
	ans, err := r.Tax.Send(tenant.With(ctx, principal), c.LegalEntityID, c.Body)
	switch {
	case err == nil && ans.Status >= 200 && ans.Status < 300:
		return store.TaxReportOutcome{Status: store.TaxReportReported, HTTPStatus: ans.Status}
	case err == nil && refused(ans.Status):
		return store.TaxReportOutcome{Status: store.TaxReportParked, HTTPStatus: ans.Status, Error: fmt.Sprintf("tax refused the transactions (%d): %s", ans.Status, ans.Detail)}
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	} else {
		msg = fmt.Sprintf("tax answered %d: %s", ans.Status, ans.Detail)
	}
	if _, _, attempts := r.limits(); c.Attempts >= attempts {
		return store.TaxReportOutcome{Status: store.TaxReportParked, HTTPStatus: ans.Status, Error: fmt.Sprintf("gave up after %d attempts: %s", c.Attempts, msg)}
	}
	return store.TaxReportOutcome{Status: store.TaxReportPending, RetryAt: r.now().Add(salesreport.Backoff(c.Attempts)), HTTPStatus: ans.Status, Error: msg}
}

func refused(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false
	}
	return status >= 400 && status < 500
}
