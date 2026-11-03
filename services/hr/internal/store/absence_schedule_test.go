package store

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

func TestScheduledAccrualAndCarryOverExpiry(t *testing.T) {
	s, ctx := newStore(t)
	at(s, date(2026, time.April, 1))
	plan := mustPlan(t, s, ctx, usPTO15("US-SCHED"))
	w := mustWorker(t, s, ctx, "sched")
	mustEmployment(t, s, ctx, w.ID, ids.New(), "PRIMARY", day(time.January, 1))
	biweekly := usPTO15("US-SCHED-BI")
	biweekly.AccrualFrequency = "BIWEEKLY"
	mustPlan(t, s, ctx, biweekly)

	first, err := s.RunScheduledAccrual(ctx)
	if err != nil || first.Plans != 2 || first.Runs != 1 {
		t.Fatalf("first scheduled accrual = %+v err %v, want March accrued for the monthly plan only", first, err)
	}
	if got := balanceOn(t, s, ctx, w.ID, day(time.March, 31)); got.Current.Accrued != "1.25" {
		t.Fatalf("accrued %s, want 1.25 for March", got.Current.Accrued)
	}
	again, err := s.RunScheduledAccrual(ctx)
	if err != nil || again.Runs != 0 {
		t.Fatalf("rerun = %+v err %v, want nothing new", again, err)
	}

	at(s, date(2026, time.June, 2))
	caught, err := s.RunScheduledAccrual(ctx)
	if err != nil || caught.Runs != 2 {
		t.Fatalf("catch-up = %+v err %v, want April and May accrued", caught, err)
	}
	if got := balanceOn(t, s, ctx, w.ID, day(time.May, 31)); got.Current.Accrued != "3.75" {
		t.Fatalf("accrued %s, want 3.75 after March to May", got.Current.Accrued)
	}

	at(s, date(2027, time.February, 1))
	if _, err := s.CloseYear(ctx, plan.ID, day(time.January, 1)); err != nil {
		t.Fatalf("close year: %v", err)
	}
	closedYear, err := s.RunScheduledAccrual(ctx)
	if err != nil || closedYear.Skipped < 7 {
		t.Fatalf("accrual after year end = %+v err %v, want June to December 2026 reported as skipped", closedYear, err)
	}
	if _, err := s.RunScheduledCarryOverExpiry(ctx); err != nil {
		t.Fatalf("expiry before its day: %v", err)
	}
	if got := balanceOn(t, s, ctx, w.ID, date(2027, time.February, 1)); got.CarryOver.Balance != "3.75" {
		t.Fatalf("carry-over %s, want 3.75 kept until its expiry day", got.CarryOver.Balance)
	}
	at(s, date(2027, time.April, 1))
	expiry, err := s.RunScheduledCarryOverExpiry(ctx)
	if err != nil || expiry.Runs == 0 {
		t.Fatalf("expiry = %+v err %v, want the 2027 carry-over expired", expiry, err)
	}
	if got := balanceOn(t, s, ctx, w.ID, date(2027, time.April, 1)); got.CarryOver.Balance != "0.00" {
		t.Fatalf("carry-over %s, want 0.00 after the scheduled expiry", got.CarryOver.Balance)
	}
}
