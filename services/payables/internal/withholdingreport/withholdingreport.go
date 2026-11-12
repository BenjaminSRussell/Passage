// Package withholdingreport sends the tax payment runs withheld to the tax service (#13): post-journal queues one report per run
// (ap_withholding_report) and the Reporter sends the due ones outside any request, as the service principal
// service:payables-withholding with only tax.withholding.record, so a tax outage never fails post-journal. Each attempt is recorded
// as reported, retried with back-off or parked; a parked report puts its reason on the run (AP-RUN-019).
package withholdingreport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
	"github.com/BenjaminSRussell/passage/services/payables/internal/tax"
)

const (
	// PrincipalName is the service principal the reporter acts as.
	PrincipalName      = "payables-withholding"
	DefaultLease       = 2 * time.Minute
	DefaultBatch       = 6
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

// Recorder records withholding entries with the tax service; *tax.Client implements it.
type Recorder interface {
	RecordWithholding(ctx context.Context, legalEntityID string, entries []tax.Entry) error
}

// Store is the queue; *store.Postgres implements it.
type Store interface {
	WithholdingDueTenants(ctx context.Context) ([]string, error)
	ClaimWithholdingReports(ctx context.Context, limit int, lease time.Duration) ([]store.ClaimedWithholdingReport, error)
	FinishWithholdingReport(ctx context.Context, id string, attempts int, o store.WithholdingOutcome) error
}

// Reporter sends due reports. Zero fields take the defaults.
type Reporter struct {
	Store       Store
	Tax         Recorder
	Batch       int
	Lease       time.Duration
	MaxAttempts int
	Now         func() time.Time

	parked atomic.Int64
}

// WriteMetrics appends the reporter's counters to a Prometheus text response.
func (r *Reporter) WriteMetrics(w io.Writer) {
	_, _ = fmt.Fprintf(w, "# HELP payables_withholding_reports_parked_total Withholding reports parked by this process: refused by the tax service or out of attempts.\n# TYPE payables_withholding_reports_parked_total counter\npayables_withholding_reports_parked_total %d\n", r.parked.Load())
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

func (r *Reporter) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// RunOnce sends every report due now, tenant by tenant, and returns how many attempts it made.
func (r *Reporter) RunOnce(ctx context.Context) (int, error) {
	tenants, err := r.Store.WithholdingDueTenants(ctx)
	if err != nil {
		return 0, err
	}
	batch, lease, _ := r.limits()
	n := 0
	var failed []error
	for _, tid := range tenants {
		tctx := store.WithholdingSystemContext(ctx, tid)
		for page := 0; page < maxBatchesPerTick && ctx.Err() == nil; page++ {
			claimed, err := r.Store.ClaimWithholdingReports(tctx, batch, lease)
			if err != nil {
				failed = append(failed, fmt.Errorf("tenant %s: %w", tid, err))
				break
			}
			for _, c := range claimed {
				o := r.attempt(ctx, tid, c)
				err := r.Store.FinishWithholdingReport(tctx, c.ID, c.Attempts, o)
				if errors.Is(err, store.ErrStaleWithholdingReport) {
					continue
				}
				if err != nil {
					failed = append(failed, fmt.Errorf("tenant %s report %s: %w", tid, c.ID, err))
					continue
				}
				if o.Status == store.WithholdingParked {
					r.parked.Add(1)
				}
				level := slog.LevelInfo
				if o.Status != store.WithholdingReported {
					level = slog.LevelWarn
				}
				slog.Log(ctx, level, "withholding report", "tenant", tid, "report", c.ID, "run", c.RunID, "attempt", c.Attempts, "status", o.Status, "err", o.Error)
				n++
			}
			if len(claimed) < batch {
				break
			}
		}
	}
	return n, errors.Join(failed...)
}

// attempt sends one report: REPORTED on success; PARKED when tax refuses the entries (a 4xx other than a refused token, a timeout or
// throttling, which pass once the deployment is fixed or the load falls) or the body cannot be read; otherwise PENDING with a back-off, and PARKED once the attempts run out.
func (r *Reporter) attempt(ctx context.Context, tid string, c store.ClaimedWithholdingReport) store.WithholdingOutcome {
	var entries []tax.Entry
	if err := json.Unmarshal(c.Body, &entries); err != nil {
		return store.WithholdingOutcome{Status: store.WithholdingParked, Error: "the queued entries cannot be read: " + err.Error()}
	}
	principal, err := tenant.ServicePrincipal(PrincipalName, tid, c.LegalEntityID)
	if err != nil {
		return store.WithholdingOutcome{Status: store.WithholdingParked, Error: "service principal: " + err.Error()}
	}
	err = r.Tax.RecordWithholding(tenant.With(ctx, principal), c.LegalEntityID, entries)
	if err == nil {
		return store.WithholdingOutcome{Status: store.WithholdingReported, HTTPStatus: http.StatusCreated}
	}
	var ts *tax.StatusError
	status := 0
	if errors.As(err, &ts) {
		status = ts.Status
		if refused(status) {
			return store.WithholdingOutcome{Status: store.WithholdingParked, HTTPStatus: status,
				Error: fmt.Sprintf("the tax service refused the withholding entries with status %d", status)}
		}
	}
	if _, _, attempts := r.limits(); c.Attempts >= attempts {
		return store.WithholdingOutcome{Status: store.WithholdingParked, HTTPStatus: status, Error: fmt.Sprintf("gave up after %d attempts: %s", c.Attempts, err)}
	}
	return store.WithholdingOutcome{Status: store.WithholdingPending, RetryAt: r.now().Add(Backoff(c.Attempts)), HTTPStatus: status, Error: err.Error()}
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
			slog.Error("payables withholding reporter run failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
