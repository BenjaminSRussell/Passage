package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
)

const (
	planPage          = 200
	maxCatchUpPeriods = 24
)

type ScheduledRun struct {
	Plans   int
	Runs    int
	Skipped int
}

func (s *Postgres) eachPlan(ctx context.Context, fn func(AbsencePlan) error) (int, error) {
	after, n := "", 0
	for {
		page, err := s.ListAbsencePlans(ctx, after, planPage)
		if err != nil {
			return n, err
		}
		for _, plan := range page.Items {
			n++
			if err := fn(plan); err != nil {
				return n, err
			}
		}
		if page.NextAfter == "" {
			return n, nil
		}
		after = page.NextAfter
	}
}

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

func notDue(err error) bool {
	switch problemCode(err) {
	case CodeNotDue, CodeYearOpen, CodeNoExpiry, CodeAccrualOverlap:
		return true
	}
	return false
}

func (s *Postgres) lastAccrualEnd(ctx context.Context, planID string) (*time.Time, error) {
	p := principal(ctx)
	var end *time.Time
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select max(period_end) from absence_accrual_period where tenant_id = $1 and plan_id = $2`, p.TenantID, planID).Scan(&end)
	})
	return end, err
}

func nextAccrualPeriod(plan AbsencePlan, last *time.Time, today time.Time) (from, to time.Time, ok bool) {
	if last != nil {
		from = last.AddDate(0, 0, 1)
	} else {
		switch plan.AccrualFrequency {
		case domain.FrequencyMonthly:
			from = time.Date(today.Year(), today.Month()-1, 1, 0, 0, 0, 0, time.UTC)
		case domain.FrequencyAnnual:
			from = domain.PlanYearStart(plan.PlanYearStart, today)
		default:
			return from, to, false
		}
	}
	switch plan.AccrualFrequency {
	case domain.FrequencyMonthly:
		return from, from.AddDate(0, 1, -1), true
	case domain.FrequencyBiweekly:
		return from, from.AddDate(0, 0, 13), true
	case domain.FrequencyAnnual:
		return from, from.AddDate(1, 0, -1), true
	}
	return from, to, false
}

func (s *Postgres) RunScheduledAccrual(ctx context.Context) (ScheduledRun, error) {
	var run ScheduledRun
	var failed []error
	plans, err := s.eachPlan(ctx, func(plan AbsencePlan) error {
		last, err := s.lastAccrualEnd(ctx, plan.ID)
		if err != nil {
			return err
		}
		for i := 0; i < maxCatchUpPeriods; i++ {
			from, to, ok := nextAccrualPeriod(plan, last, s.today())
			if !ok {
				run.Skipped++
				return nil
			}
			_, err := s.RunAccrual(ctx, plan.ID, from, to)
			if problemCode(err) == CodeYearClosed {
				slog.WarnContext(ctx, "hr accrual skipped a period of a closed plan year", "plan", plan.Code, "periodStart", from.Format(absDateLayout))
				run.Skipped++
				last = &to
				continue
			}
			if notDue(err) {
				run.Skipped++
				return nil
			}
			if err != nil {
				slog.ErrorContext(ctx, "hr accrual failed", "plan", plan.Code, "periodStart", from.Format(absDateLayout), "err", err)
				failed = append(failed, fmt.Errorf("plan %s %s: %w", plan.Code, from.Format(absDateLayout), err))
				return nil
			}
			run.Runs++
			last = &to
		}
		return nil
	})
	run.Plans = plans
	return run, errors.Join(append(failed, err)...)
}

func (s *Postgres) RunScheduledCarryOverExpiry(ctx context.Context) (ScheduledRun, error) {
	var run ScheduledRun
	var failed []error
	plans, err := s.eachPlan(ctx, func(plan AbsencePlan) error {
		if plan.CarryOverExpiryMonths == 0 {
			run.Skipped++
			return nil
		}
		current := domain.PlanYearStart(plan.PlanYearStart, s.today())
		for _, yearStart := range []time.Time{current.AddDate(-1, 0, 0), current} {
			_, err := s.ExpireCarryOver(ctx, plan.ID, yearStart)
			if notDue(err) {
				run.Skipped++
				continue
			}
			if err != nil {
				slog.ErrorContext(ctx, "hr carry-over expiry failed", "plan", plan.Code, "planYearStart", yearStart.Format(absDateLayout), "err", err)
				failed = append(failed, fmt.Errorf("plan %s %s: %w", plan.Code, yearStart.Format(absDateLayout), err))
				continue
			}
			run.Runs++
		}
		return nil
	})
	run.Plans = plans
	return run, errors.Join(append(failed, err)...)
}
