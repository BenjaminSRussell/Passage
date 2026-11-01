package recon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
)

const (
	MetricRevenue = "revenue"
	RunHour       = 2
)

type SemanticReader interface {
	Revenue(ctx context.Context, day string) (map[string]*decimal.Decimal, error)
}

type GLRevenue struct {
	LegalEntityID string
	Currency      string
	Amount        decimal.Decimal
}

type GLReader interface {
	Revenue(ctx context.Context, day string) ([]GLRevenue, error)
}

type Recorder interface {
	RecordReconciliation(ctx context.Context, in domain.Reconciliation) (domain.Reconciliation, bool, error)
}

type Job struct {
	Semantic  SemanticReader
	GL        GLReader
	Recorder  Recorder
	Principal tenant.Principal
	Location  *time.Location
	Now       func() time.Time
}

type Result struct {
	Checked int
	Failed  int
}

func (j Job) RunDay(ctx context.Context, day string) (Result, error) {
	ctx = tenant.With(ctx, j.Principal)
	semantic, err := j.Semantic.Revenue(ctx, day)
	if err != nil {
		return Result{}, fmt.Errorf("semantic revenue for %s: %w", day, err)
	}
	gl, err := j.GL.Revenue(ctx, day)
	if err != nil {
		return Result{}, fmt.Errorf("ledger revenue for %s: %w", day, err)
	}
	var res Result
	var errs []error
	for _, g := range gl {
		value := decimal.Zero
		if v := semantic[g.LegalEntityID]; v != nil {
			value = *v
		}
		rec, _, err := j.Recorder.RecordReconciliation(ctx, domain.Reconciliation{
			MetricID: MetricRevenue, LegalEntityID: g.LegalEntityID, Date: day, Currency: g.Currency,
			SemanticValue: value, SourceValue: g.Amount, Tolerance: domain.DefaultTolerance(),
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("record %s %s: %w", g.LegalEntityID, day, err))
			continue
		}
		res.Checked++
		if rec.Status != domain.ReconOK {
			res.Failed++
			slog.WarnContext(ctx, "metric failed reconciliation", "code", "ANA-0104", "metric", MetricRevenue, "legalEntityId", g.LegalEntityID, "date", day, "delta", rec.Delta.String())
		}
	}
	return res, errors.Join(errs...)
}

func NextRun(now time.Time, loc *time.Location) time.Time {
	local := now.In(loc)
	next := time.Date(local.Year(), local.Month(), local.Day(), RunHour, 0, 0, 0, loc)
	if !next.After(local) {
		next = time.Date(local.Year(), local.Month(), local.Day()+1, RunHour, 0, 0, 0, loc)
	}
	return next
}

func (j Job) Loop(ctx context.Context) {
	for {
		now := j.Now()
		timer := time.NewTimer(NextRun(now, j.Location).Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		day := j.Now().In(j.Location).AddDate(0, 0, -1).Format(time.DateOnly)
		res, err := j.RunDay(ctx, day)
		if err != nil {
			slog.ErrorContext(ctx, "revenue reconciliation failed", "date", day, "err", err)
			continue
		}
		slog.InfoContext(ctx, "revenue reconciliation finished", "date", day, "checked", res.Checked, "failed", res.Failed)
	}
}
