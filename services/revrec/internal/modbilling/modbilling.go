// Package modbilling sends the price change of contract modifications to billing (#29): the modification queues one request in
// its own transaction (modification_billing), and the Reporter sends the due ones to POST /v1/billing/contract-modifications outside
// any modification, so a billing outage never blocks one. Calls are signed as revrec with only billing.contract-modification.bill,
// on behalf of the service principal service:revrec-billing, for the request's tenant and legal entity. The Reporter finds the
// tenants with due requests through a SECURITY DEFINER index, leases a batch per tenant, and records each attempt as billed,
// retried with back-off or parked.
package modbilling

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
	"sync/atomic"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/revrec/internal/store"
)

const (
	// PrincipalName is the service principal the reporter acts as.
	PrincipalName = "revrec-billing"
	// DutyBill is the one duty of revrec's grant on billing.
	DutyBill        = "billing.contract-modification.bill"
	audience        = "billing"
	path            = "/v1/billing/contract-modifications"
	defaultTimeout  = 10 * time.Second
	maxResponseSize = 64 << 10
	maxErrorDetail  = 500

	DefaultLease       = 2 * time.Minute
	DefaultBatch       = int(DefaultLease / (2 * defaultTimeout))
	DefaultMaxAttempts = 20
	DefaultInterval    = 30 * time.Second
	maxBackoff         = time.Hour
	maxBatchesPerTick  = 50
)

// Backoff is the wait before attempt n+1 after n failed attempts: 30 seconds doubling, at most an hour.
func Backoff(n int) time.Duration {
	n = max(n, 1)
	if n > 8 {
		return maxBackoff
	}
	return min(30*time.Second<<(n-1), maxBackoff)
}

// Answer is what billing answered: Status 0 means no answer (transport failure).
type Answer struct {
	Status int
	Detail string
}

// Sender posts one billing request.
type Sender interface {
	Send(ctx context.Context, legalEntityID, key string, body []byte) (Answer, error)
}

// Client is the billing client; it signs each call with the principal in ctx as actor and only DutyBill.
type Client struct {
	baseURL string
	signer  *tenant.ServiceSigner
	http    *http.Client
}

// New returns the billing client.
func New(baseURL string, signer *tenant.ServiceSigner, hc *http.Client) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("billing client needs a base URL")
	}
	if signer == nil {
		return nil, errors.New("billing client needs a service signing key")
	}
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), signer: signer, http: hc}, nil
}

// FromEnv builds the client from BILLING_URL and the service signing key. Without BILLING_URL it returns nil, nil: the requests
// wait, PENDING.
func FromEnv(getenv func(string) string) (*Client, error) {
	base := getenv("BILLING_URL")
	if base == "" {
		return nil, nil
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("BILLING_URL needs PASSAGE_SERVICE_SIGNING_KEY or PASSAGE_SERVICE_SIGNING_KEY_FILE")
	}
	return New(base, signer, nil)
}

// Send posts body with the Idempotency-Key. An error means no answer was read; a retry with the same key and body is safe.
func (c *Client) Send(ctx context.Context, legalEntityID, key string, body []byte) (Answer, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return Answer{}, errors.New("billing: no principal in context")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return Answer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	if _, err := c.signer.ApplyDuties(req, p, audience, legalEntityID, []string{DutyBill}); err != nil {
		return Answer{}, fmt.Errorf("sign billing request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Answer{}, fmt.Errorf("billing request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return Answer{}, fmt.Errorf("read billing response: %w", err)
	}
	return Answer{Status: resp.StatusCode, Detail: detail(raw)}, nil
}

func detail(raw []byte) string {
	var p struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	s := strings.TrimSpace(string(raw))
	if json.Unmarshal(raw, &p) == nil && (p.Code != "" || p.Detail != "") {
		s = strings.TrimSpace(p.Code + " " + p.Detail)
	}
	return s[:min(len(s), maxErrorDetail)]
}

// Store is the queue the reporter works on; *store.Postgres implements it.
type Store interface {
	BillingDueTenants(ctx context.Context) ([]string, error)
	ClaimBillings(ctx context.Context, limit int, lease time.Duration) ([]store.ClaimedBilling, error)
	FinishBilling(ctx context.Context, id string, attempts int, o store.BillingOutcome) error
}

// Reporter sends due billing requests. Zero fields take the defaults.
type Reporter struct {
	Store       Store
	Billing     Sender
	Batch       int
	Lease       time.Duration
	MaxAttempts int
	Now         func() time.Time

	parked atomic.Int64
}

// WriteMetrics appends the reporter's counters to a Prometheus text response.
func (r *Reporter) WriteMetrics(w io.Writer) {
	_, _ = fmt.Fprintf(w, "# HELP revrec_modification_billing_parked_total Modification billing requests parked by this process: refused by billing or out of attempts.\n# TYPE revrec_modification_billing_parked_total counter\nrevrec_modification_billing_parked_total %d\n", r.parked.Load())
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

// RunOnce sends every request due now, tenant by tenant, and returns how many attempts it made; a failing tenant does not stop
// the others.
func (r *Reporter) RunOnce(ctx context.Context) (int, error) {
	tenants, err := r.Store.BillingDueTenants(ctx)
	if err != nil {
		return 0, err
	}
	batch, lease, _ := r.limits()
	n := 0
	var failed []error
	for _, tid := range tenants {
		tctx := store.BillingSystemContext(ctx, tid)
		for page := 0; page < maxBatchesPerTick && ctx.Err() == nil; page++ {
			claimed, err := r.Store.ClaimBillings(tctx, batch, lease)
			if err != nil {
				failed = append(failed, fmt.Errorf("tenant %s: %w", tid, err))
				break
			}
			for _, c := range claimed {
				o := r.attempt(ctx, tid, c)
				err := r.Store.FinishBilling(tctx, c.ID, c.Attempts, o)
				if errors.Is(err, store.ErrStaleBilling) {
					slog.WarnContext(ctx, "modification billing lease lost; the newer attempt records the outcome", "tenant", tid, "request", c.ID)
					continue
				}
				if err != nil {
					failed = append(failed, fmt.Errorf("tenant %s request %s: %w", tid, c.ID, err))
					continue
				}
				if o.Status == store.BillingParked {
					r.parked.Add(1)
				}
				level := slog.LevelInfo
				if o.Status != store.BillingBilled {
					level = slog.LevelWarn
				}
				slog.Log(ctx, level, "modification billing", "tenant", tid, "request", c.ID, "modification", c.ModificationID,
					"attempt", c.Attempts, "status", o.Status, "httpStatus", o.HTTPStatus, "err", o.Error)
				n++
			}
			if len(claimed) < batch {
				break
			}
		}
	}
	return n, errors.Join(failed...)
}

// attempt sends one claimed request: BILLED on 2xx, PARKED on a refusal (4xx other than a refused token, a timeout or throttling),
// otherwise PENDING with a back-off, and PARKED once the attempts run out.
func (r *Reporter) attempt(ctx context.Context, tid string, c store.ClaimedBilling) store.BillingOutcome {
	principal, err := tenant.ServicePrincipal(PrincipalName, tid, c.LegalEntityID)
	if err != nil {
		return store.BillingOutcome{Status: store.BillingParked, Error: "service principal: " + err.Error()}
	}
	ans, err := r.Billing.Send(tenant.With(ctx, principal), c.LegalEntityID, c.IdempotencyKey, c.Body)
	switch {
	case err == nil && ans.Status >= 200 && ans.Status < 300:
		return store.BillingOutcome{Status: store.BillingBilled, HTTPStatus: ans.Status}
	case err == nil && refused(ans.Status):
		return store.BillingOutcome{Status: store.BillingParked, HTTPStatus: ans.Status, Error: fmt.Sprintf("billing refused the modification (%d): %s", ans.Status, ans.Detail)}
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	} else {
		msg = fmt.Sprintf("billing answered %d: %s", ans.Status, ans.Detail)
	}
	if _, _, attempts := r.limits(); c.Attempts >= attempts {
		return store.BillingOutcome{Status: store.BillingParked, HTTPStatus: ans.Status, Error: fmt.Sprintf("gave up after %d attempts: %s", c.Attempts, msg)}
	}
	return store.BillingOutcome{Status: store.BillingPending, RetryAt: r.now().Add(Backoff(c.Attempts)), HTTPStatus: ans.Status, Error: msg}
}

func refused(status int) bool {
	if status < 400 || status >= 500 {
		return false
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false
	}
	return true
}

// Run calls once every interval until ctx ends.
func Run(ctx context.Context, interval time.Duration, once func(context.Context) (int, error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := once(ctx); err != nil && ctx.Err() == nil {
			slog.Error("revrec modification billing run failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
