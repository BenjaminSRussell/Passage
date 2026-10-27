package importer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/BenjaminSRussell/passage/services/fx/internal/provider"
	"github.com/BenjaminSRussell/passage/services/fx/internal/store"
)

var ErrNoLastKnownRate = errors.New("provider outage and no last-known rate to fall back to")

type RunStore interface {
	StartRun(ctx context.Context, provider, rateType string, runDate time.Time) (store.Run, bool, error)
	RecordAttempt(ctx context.Context, runID string, attempts int) error
	PostRates(ctx context.Context, in []store.RateInput) ([]store.Posted, error)
	FinishRunSucceeded(ctx context.Context, runID string, imported, unchanged int) error
	FinishRunFailed(ctx context.Context, runID string, attempts int, cause error) error
	FinishRunDegraded(ctx context.Context, runID string, attempts int, cause error) error
	LastSuccess(ctx context.Context, provider, rateType string) (*time.Time, error)
}

type Config struct {
	MaxAttempts int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
}

var DefaultConfig = Config{MaxAttempts: 4, BaseBackoff: time.Second, MaxBackoff: 30 * time.Second}

type Result struct {
	RunID         string
	Attempts      int
	Imported      int
	Unchanged     int
	AlreadyDone   bool
	Degraded      bool
	LastSuccessAt *time.Time
}

type Importer struct {
	provider provider.Provider
	store    RunStore
	cfg      Config
	sleep    func(context.Context, time.Duration) error
	metrics  *Metrics
}

func New(p provider.Provider, s RunStore, cfg Config, m *Metrics) *Importer {
	return &Importer{provider: p, store: s, cfg: cfg, sleep: sleepContext, metrics: m}
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

func (i *Importer) fetch(ctx context.Context, rateType string, date time.Time, run store.Run) ([]provider.RatePoint, int, error) {
	var lastErr error
	attempts := 0
	for attempts < i.cfg.MaxAttempts {
		attempts++
		if err := i.store.RecordAttempt(ctx, run.ID, attempts); err != nil {
			return nil, attempts, err
		}
		points, err := i.provider.Fetch(ctx, rateType, date)
		if err == nil {
			return points, attempts, nil
		}
		lastErr = err
		slog.Warn("fx provider fetch failed", "provider", i.provider.Name(), "rateType", rateType, "attempt", attempts, "err", err)
		if ctx.Err() != nil || errors.Is(err, provider.ErrDisabled) || attempts == i.cfg.MaxAttempts {
			break
		}
		if err := i.sleep(ctx, i.backoff(attempts)); err != nil {
			break
		}
	}
	return nil, attempts, lastErr
}

func (i *Importer) Run(ctx context.Context, rateType string, date time.Time) (Result, error) {
	name := i.provider.Name()
	run, done, err := i.store.StartRun(ctx, name, rateType, date)
	if err != nil {
		return Result{}, fmt.Errorf("start run: %w", err)
	}
	if done {
		return Result{RunID: run.ID, AlreadyDone: true, Attempts: run.Attempts, Imported: run.PairsImported, Unchanged: run.PairsUnchanged}, nil
	}
	points, attempts, fetchErr := i.fetch(ctx, rateType, date, run)
	finishCtx := context.WithoutCancel(ctx)
	res := Result{RunID: run.ID, Attempts: attempts}
	if fetchErr != nil {
		return i.fallBack(finishCtx, name, rateType, res, fetchErr)
	}
	inputs := make([]store.RateInput, len(points))
	for n, pt := range points {
		inputs[n] = store.RateInput{
			RateType: rateType, From: pt.From, To: pt.To, ValidFrom: pt.ValidFrom, Rate: pt.Rate,
			Source: name, ProviderTimestamp: pt.ProviderTimestamp, PayloadHash: pt.PayloadHash[:],
		}
	}
	posted, err := i.store.PostRates(ctx, inputs)
	if err != nil {
		err = fmt.Errorf("post %s rates: %w", name, err)
		if ferr := i.store.FinishRunFailed(finishCtx, run.ID, attempts, err); ferr != nil {
			slog.Error("fx finish failed run", "runId", run.ID, "err", ferr)
		}
		return res, err
	}
	for _, p := range posted {
		if p.Unchanged {
			res.Unchanged++
		} else {
			res.Imported++
		}
	}
	if err := i.store.FinishRunSucceeded(finishCtx, run.ID, res.Imported, res.Unchanged); err != nil {
		return res, fmt.Errorf("finish run: %w", err)
	}
	i.metrics.set(name, rateType, false)
	return res, nil
}

func (i *Importer) fallBack(ctx context.Context, name, rateType string, res Result, cause error) (Result, error) {
	last, err := i.store.LastSuccess(ctx, name, rateType)
	if err != nil {
		return res, fmt.Errorf("look up last success: %w", err)
	}
	if last == nil {
		err := fmt.Errorf("%w: %s %s: %v", ErrNoLastKnownRate, name, rateType, cause)
		if ferr := i.store.FinishRunFailed(ctx, res.RunID, res.Attempts, err); ferr != nil {
			slog.Error("fx finish failed run", "runId", res.RunID, "err", ferr)
		}
		i.metrics.set(name, rateType, true)
		return res, err
	}
	if err := i.store.FinishRunDegraded(ctx, res.RunID, res.Attempts, cause); err != nil {
		return res, fmt.Errorf("finish degraded run: %w", err)
	}
	i.metrics.set(name, rateType, true)
	slog.Error("fx provider outage, serving last-known rates", "provider", name, "rateType", rateType, "lastSuccessAt", last, "err", cause)
	res.Degraded = true
	res.LastSuccessAt = last
	return res, nil
}
