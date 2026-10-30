package importer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/BenjaminSRussell/passage/services/fx/internal/provider"
	"github.com/BenjaminSRussell/passage/services/fx/internal/store"
)

var (
	ErrNoLastKnownRate  = errors.New("provider outage and no last-known rate to fall back to")
	ErrDateMismatch     = errors.New("provider returned rates for a different date than requested")
	ErrCubeNotPublished = errors.New("provider has not published a cube for the run date yet")
)

type RunStore interface {
	StartRun(ctx context.Context, provider, rateType string, runDate time.Time) (store.Run, bool, error)
	RecordAttempt(ctx context.Context, runID string, attempts int) error
	PostRates(ctx context.Context, in []store.RateInput) ([]store.Posted, error)
	FinishRunSucceeded(ctx context.Context, runID string, imported, unchanged int) error
	FinishRunFailed(ctx context.Context, runID string, attempts int, cause error, retryable bool) error
	FinishRunDegraded(ctx context.Context, runID string, attempts int, cause error, retryable bool) error
	FinishRunAborted(ctx context.Context, runID string, attempts int, cause error) error
	LastSuccess(ctx context.Context, provider, rateType string) (*time.Time, error)
}

type Config struct {
	MaxAttempts          int
	BaseBackoff          time.Duration
	MaxBackoff           time.Duration
	PublishCutoffHourUTC int
}

var DefaultConfig = Config{MaxAttempts: 4, BaseBackoff: time.Second, MaxBackoff: 30 * time.Second, PublishCutoffHourUTC: 18}

type Result struct {
	RunID         string
	Attempts      int
	Imported      int
	Unchanged     int
	AlreadyDone   bool
	InProgress    bool
	Degraded      bool
	Pending       bool
	ValidFrom     time.Time
	LastSuccessAt *time.Time
}

type Importer struct {
	provider provider.Provider
	store    RunStore
	cfg      Config
	sleep    func(context.Context, time.Duration) error
	now      func() time.Time
	metrics  *Metrics
}

func New(p provider.Provider, s RunStore, cfg Config, m *Metrics) *Importer {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultConfig.MaxAttempts
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = DefaultConfig.BaseBackoff
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = DefaultConfig.MaxBackoff
	}
	if cfg.PublishCutoffHourUTC <= 0 || cfg.PublishCutoffHourUTC > 24 {
		cfg.PublishCutoffHourUTC = DefaultConfig.PublishCutoffHourUTC
	}
	return &Importer{provider: p, store: s, cfg: cfg, sleep: sleepContext, now: time.Now, metrics: m}
}

func retryable(err error) bool {
	if errors.Is(err, provider.ErrDisabled) || errors.Is(err, provider.ErrMalformed) {
		return false
	}
	var se *provider.StatusError
	if errors.As(err, &se) {
		return se.Code >= http.StatusInternalServerError || se.Code == http.StatusTooManyRequests
	}
	return true
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (i *Importer) backoff(attempt int) time.Duration {
	d := i.cfg.BaseBackoff << (attempt - 1)
	if d <= 0 || d > i.cfg.MaxBackoff {
		return i.cfg.MaxBackoff
	}
	return d
}

func (i *Importer) fetch(ctx context.Context, rateType string, date time.Time, run store.Run) (points []provider.RatePoint, attempts int, fetchErr, storeErr error) {
	for attempts < i.cfg.MaxAttempts {
		attempts++
		if err := i.store.RecordAttempt(ctx, run.ID, attempts); err != nil {
			return nil, attempts, nil, fmt.Errorf("record attempt: %w", err)
		}
		pts, err := i.provider.Fetch(ctx, rateType, date)
		if err == nil {
			return pts, attempts, nil, nil
		}
		fetchErr = err
		slog.Warn("fx provider fetch failed", "provider", i.provider.Name(), "rateType", rateType, "attempt", attempts, "err", err)
		if ctx.Err() != nil || !retryable(err) || attempts == i.cfg.MaxAttempts {
			break
		}
		if err := i.sleep(ctx, i.backoff(attempts)); err != nil {
			break
		}
	}
	return nil, attempts, fetchErr, nil
}

func (i *Importer) Run(ctx context.Context, rateType string, date time.Time) (Result, error) {
	name := i.provider.Name()
	run, done, err := i.store.StartRun(ctx, name, rateType, date)
	if err != nil {
		return Result{}, fmt.Errorf("start run: %w", err)
	}
	if done {
		if run.Status == store.RunRunning {
			return Result{RunID: run.ID, InProgress: true, Attempts: run.Attempts}, nil
		}
		i.metrics.record(ctx, name, rateType, false, run.LastSuccessAt)
		return Result{RunID: run.ID, AlreadyDone: true, Attempts: run.Attempts, Imported: run.PairsImported, Unchanged: run.PairsUnchanged}, nil
	}
	points, attempts, fetchErr, storeErr := i.fetch(ctx, rateType, date, run)
	if storeErr != nil {
		return Result{RunID: run.ID, Attempts: attempts}, storeErr
	}
	finishCtx := context.WithoutCancel(ctx)
	res := Result{RunID: run.ID, Attempts: attempts}
	if fetchErr != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return res, i.abort(finishCtx, res, fmt.Errorf("import cancelled: %w", ctx.Err()))
		}
		if errors.Is(fetchErr, provider.ErrDisabled) {
			return res, i.abort(finishCtx, res, fmt.Errorf("%s: %w", name, fetchErr))
		}
		return i.fallBack(finishCtx, name, rateType, res, fetchErr)
	}
	if i.provider.SupportsHistory() {
		if err := checkDate(points, date); err != nil {
			return res, i.reject(finishCtx, name, rateType, res, err)
		}
	}
	inputs := make([]store.RateInput, len(points))
	for n, pt := range points {
		if pt.ValidFrom.After(res.ValidFrom) {
			res.ValidFrom = pt.ValidFrom
		}
		inputs[n] = store.RateInput{
			RateType: rateType, From: pt.From, To: pt.To, ValidFrom: pt.ValidFrom, Rate: pt.Rate,
			Source: name, ProviderTimestamp: pt.ProviderTimestamp, PayloadHash: pt.PayloadHash[:],
		}
	}
	posted, err := i.store.PostRates(ctx, inputs)
	if err != nil {
		return res, i.abort(finishCtx, res, fmt.Errorf("post %s rates: %w", name, err))
	}
	for _, p := range posted {
		if p.Unchanged {
			res.Unchanged++
		} else {
			res.Imported++
		}
	}
	now := i.now()
	if i.awaitingPublication(date, res.ValidFrom, now) {
		res.Pending = true
		cause := fmt.Errorf("%w: cube dated %s, run date %s", ErrCubeNotPublished, res.ValidFrom.Format(time.DateOnly), date.UTC().Format(time.DateOnly))
		if err := i.store.FinishRunAborted(finishCtx, run.ID, res.Attempts, cause); err != nil {
			return res, fmt.Errorf("finish pending run: %w", err)
		}
		return res, nil
	}
	if err := i.store.FinishRunSucceeded(finishCtx, run.ID, res.Imported, res.Unchanged); err != nil {
		return res, fmt.Errorf("finish run: %w", err)
	}
	i.metrics.record(ctx, name, rateType, false, &now)
	return res, nil
}

func (i *Importer) awaitingPublication(date, validFrom, now time.Time) bool {
	if i.provider.SupportsHistory() || !validFrom.Before(dayOf(date)) {
		return false
	}
	return dayOf(date).Equal(dayOf(now)) && now.UTC().Hour() < i.cfg.PublishCutoffHourUTC
}

func dayOf(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func (i *Importer) SupportsHistory() bool { return i.provider.SupportsHistory() }

func checkDate(points []provider.RatePoint, date time.Time) error {
	want := dayOf(date)
	for _, pt := range points {
		if !pt.ValidFrom.Equal(want) {
			return fmt.Errorf("%w: requested %s, got %s", ErrDateMismatch, want.Format(time.DateOnly), pt.ValidFrom.Format(time.DateOnly))
		}
	}
	return nil
}

func (i *Importer) reject(ctx context.Context, name, rateType string, res Result, cause error) error {
	if err := i.store.FinishRunFailed(ctx, res.RunID, res.Attempts, cause, false); err != nil {
		slog.Error("fx finish failed run", "runId", res.RunID, "err", err)
		if errors.Is(err, store.ErrLostOwnership) {
			return err
		}
	}
	i.metrics.record(ctx, name, rateType, true, nil)
	return cause
}

func (i *Importer) abort(ctx context.Context, res Result, cause error) error {
	if err := i.store.FinishRunAborted(ctx, res.RunID, res.Attempts, fmt.Errorf("aborted: %w", cause)); err != nil {
		slog.Error("fx finish aborted run", "runId", res.RunID, "err", err)
	}
	return cause
}

func (i *Importer) fallBack(ctx context.Context, name, rateType string, res Result, cause error) (Result, error) {
	last, err := i.store.LastSuccess(ctx, name, rateType)
	if err != nil {
		return res, fmt.Errorf("look up last success: %w", err)
	}
	if last == nil {
		err := fmt.Errorf("%w: %s %s: %v", ErrNoLastKnownRate, name, rateType, cause)
		if ferr := i.store.FinishRunFailed(ctx, res.RunID, res.Attempts, err, retryable(cause)); ferr != nil {
			slog.Error("fx finish failed run", "runId", res.RunID, "err", ferr)
			if errors.Is(ferr, store.ErrLostOwnership) {
				return res, ferr
			}
		}
		i.metrics.record(ctx, name, rateType, true, nil)
		return res, err
	}
	if err := i.store.FinishRunDegraded(ctx, res.RunID, res.Attempts, cause, retryable(cause)); err != nil {
		return res, fmt.Errorf("finish degraded run: %w", err)
	}
	i.metrics.record(ctx, name, rateType, true, last)
	slog.Error("fx provider outage, serving last-known rates", "provider", name, "rateType", rateType, "lastSuccessAt", last, "err", cause)
	res.Degraded = true
	res.LastSuccessAt = last
	return res, nil
}
