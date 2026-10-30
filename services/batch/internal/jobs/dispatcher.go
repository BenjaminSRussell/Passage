package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	DefaultDispatchInterval    = 5 * time.Second
	DefaultDispatchConcurrency = 4
	DefaultRunAttempts         = 5
	DefaultRetryAfter          = time.Minute
)

type Runnable struct {
	TenantID      string
	RunID         string
	Handler       string
	LegalEntityID string
	UserID        string
}

type RunSource interface {
	Runnable(ctx context.Context, handlers []string, maxAttempts int, retryAfter time.Duration, limit int) ([]Runnable, error)
}

type RunExecutor interface {
	Run(ctx context.Context, handler, runID string) (Summary, error)
}

type PgRunSource struct{ pool *pgxpool.Pool }

func NewPgRunSource(pool *pgxpool.Pool) *PgRunSource { return &PgRunSource{pool: pool} }

func (s *PgRunSource) Runnable(ctx context.Context, handlers []string, maxAttempts int, retryAfter time.Duration, limit int) ([]Runnable, error) {
	rows, err := s.pool.Query(ctx, `select tenant_id::text, run_id::text, handler, legal_entity_id::text, triggered_by::text
		from batch_dispatchable_runs($1, $2, $3, $4)`, handlers, maxAttempts, int(retryAfter.Seconds()), limit)
	if err != nil {
		return nil, fmt.Errorf("find runnable runs: %w", err)
	}
	defer rows.Close()
	var out []Runnable
	for rows.Next() {
		var r Runnable
		if err := rows.Scan(&r.TenantID, &r.RunID, &r.Handler, &r.LegalEntityID, &r.UserID); err != nil {
			return nil, fmt.Errorf("scan runnable run: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Dispatcher finds executing or retryable runs of the product handlers and runs
// them with bounded concurrency. Replicas may all run a Dispatcher: the claim
// lease and lease token in the page store admit one worker per run.
type Dispatcher struct {
	Source      RunSource
	Executor    RunExecutor
	Interval    time.Duration
	Concurrency int
	MaxAttempts int
	RetryAfter  time.Duration

	inflight sync.Map
	active   atomic.Int64
	started  atomic.Int64
	ended    atomic.Int64
	failed   atomic.Int64
	refused  atomic.Int64
	scanErrs atomic.Int64
}

func (d *Dispatcher) Run(ctx context.Context) {
	interval := d.Interval
	if interval <= 0 {
		interval = DefaultDispatchInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		d.tick(ctx, &wg)
		select {
		case <-ctx.Done():
			slog.Info("batch dispatcher stopping", "inflight", d.active.Load())
			return
		case <-ticker.C:
		}
	}
}

func (d *Dispatcher) tick(ctx context.Context, wg *sync.WaitGroup) {
	slots := d.limit() - int(d.active.Load())
	if slots <= 0 || ctx.Err() != nil {
		return
	}
	runs, err := d.Source.Runnable(ctx, []string{HandlerProductExplode, HandlerProductGroupChange}, d.attempts(), d.retryAfter(), slots)
	if err != nil {
		if ctx.Err() == nil {
			d.scanErrs.Add(1)
			slog.Error("batch dispatcher scan failed", "err", err)
		}
		return
	}
	for _, r := range runs {
		if d.active.Load() >= int64(d.limit()) {
			return
		}
		if _, busy := d.inflight.LoadOrStore(r.RunID, struct{}{}); busy {
			continue
		}
		d.active.Add(1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer d.active.Add(-1)
			defer d.inflight.Delete(r.RunID)
			d.execute(ctx, r)
		}()
	}
}

func (d *Dispatcher) execute(ctx context.Context, r Runnable) {
	ctx = tenant.With(ctx, tenant.Principal{TenantID: r.TenantID, LegalEntityID: r.LegalEntityID, UserID: r.UserID, Service: "batch"})
	ctx = context.WithValue(ctx, middleware.RequestIDKey, "batch-run-"+r.RunID)
	log := slog.With("runId", r.RunID, "tenantId", r.TenantID, "handler", r.Handler, "correlationId", "batch-run-"+r.RunID)
	d.started.Add(1)
	start := time.Now()
	summary, err := d.Executor.Run(ctx, r.Handler, r.RunID)
	switch {
	case errors.Is(err, ErrRunNotActive), errors.Is(err, ErrLostLease), errors.Is(err, ErrRunNotFound):
		d.refused.Add(1)
		log.Info("batch run not taken", "reason", err.Error())
	case err != nil:
		d.failed.Add(1)
		log.Error("batch run failed", "err", err, "pages", summary.Pages, "items", summary.Items, "durationMs", time.Since(start).Milliseconds())
	default:
		d.ended.Add(1)
		log.Info("batch run ended", "pages", summary.Pages, "items", summary.Items, "succeeded", summary.Succeeded, "skipped", summary.Skipped, "durationMs", time.Since(start).Milliseconds())
	}
}

func (d *Dispatcher) limit() int {
	if d.Concurrency > 0 {
		return d.Concurrency
	}
	return DefaultDispatchConcurrency
}

func (d *Dispatcher) attempts() int {
	if d.MaxAttempts > 0 {
		return d.MaxAttempts
	}
	return DefaultRunAttempts
}

func (d *Dispatcher) retryAfter() time.Duration {
	if d.RetryAfter > 0 {
		return d.RetryAfter
	}
	return DefaultRetryAfter
}

func (d *Dispatcher) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		for _, m := range []struct {
			name, kind, help string
			value            int64
		}{
			{"batch_dispatch_inflight", "gauge", "Runs this replica is executing.", d.active.Load()},
			{"batch_dispatch_started_total", "counter", "Runs this replica started executing.", d.started.Load()},
			{"batch_dispatch_ended_total", "counter", "Dispatched runs that ended cleanly.", d.ended.Load()},
			{"batch_dispatch_failed_total", "counter", "Dispatched runs that ended in error.", d.failed.Load()},
			{"batch_dispatch_refused_total", "counter", "Runs another worker held or that were no longer executable.", d.refused.Load()},
			{"batch_dispatch_scan_errors_total", "counter", "Failed scans for runnable runs.", d.scanErrs.Load()},
		} {
			_, _ = fmt.Fprintf(w, "# HELP %[1]s %[3]s\n# TYPE %[1]s %[2]s\n%[1]s %[4]d\n", m.name, m.kind, m.help, m.value)
		}
	})
}
