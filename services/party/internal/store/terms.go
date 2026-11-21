package store

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/party/internal/domain"
)

const (
	CodeTermInvalid     = "PTY-TERM-INVALID"
	CodeTermExists      = "PTY-TERM-EXISTS"
	CodeCalendarInvalid = "PTY-CALENDAR-INVALID"
	CodeCalendarExists  = "PTY-CALENDAR-EXISTS"
	paymentTermFields   = `t.id::text, t.code, t.name, t.net_days, t.day_of_month, t.offset_months, t.version, coalesce(t.calendar_id::text, ''),
		t.discount1_percent, t.discount1_days, t.discount2_percent, t.discount2_days, t.discount3_percent, t.discount3_days`
	paymentCalendarFields = `c.id::text, c.code, c.name, c.holidays, c.weekend_days::int[], c.shift, c.version`
)

type TermStore interface {
	CreatePaymentTerm(ctx context.Context, in domain.NewPaymentTerm) (domain.PaymentTerm, error)
	CreatePaymentCalendar(ctx context.Context, in domain.NewPaymentCalendar) (domain.PaymentCalendar, error)
	TermForDates(ctx context.Context, id string) (domain.PaymentTerm, error)
}

func discountsOf(percents [3]*float64, days [3]*int) []domain.Discount {
	out := []domain.Discount{}
	for i, p := range percents {
		if p != nil && days[i] != nil {
			out = append(out, domain.Discount{Percent: *p, Days: *days[i]})
		}
	}
	return out
}

func scanPaymentTermRow(row pgx.Row) (domain.PaymentTerm, error) {
	var t domain.PaymentTerm
	var p [3]*float64
	var d [3]*int
	err := row.Scan(&t.ID, &t.Code, &t.Name, &t.NetDays, &t.DayOfMonth, &t.OffsetMonths, &t.Version, &t.CalendarID,
		&p[0], &d[0], &p[1], &d[1], &p[2], &d[2])
	t.Discounts = discountsOf(p, d)
	return t, err
}

func scanPaymentCalendarRow(row pgx.Row) (domain.PaymentCalendar, error) {
	var c domain.PaymentCalendar
	var holidays []time.Time
	err := row.Scan(&c.ID, &c.Code, &c.Name, &holidays, &c.WeekendDays, &c.Shift, &c.Version)
	c.Holidays = make([]string, 0, len(holidays))
	for _, h := range holidays {
		c.Holidays = append(c.Holidays, h.Format(time.DateOnly))
	}
	return c, err
}

func (s *Postgres) CreatePaymentCalendar(ctx context.Context, in domain.NewPaymentCalendar) (domain.PaymentCalendar, error) {
	var c domain.PaymentCalendar
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		c, err = scanPaymentCalendarRow(tx.QueryRow(ctx, `with c as (
			insert into payment_calendar (tenant_id, id, code, name, holidays, weekend_days, shift)
			values ($1, $2, $3, $4, $5::text[]::date[], $6::int[]::smallint[], $7) returning *)
			select `+paymentCalendarFields+` from c`,
			tenantOf(ctx), ids.New(), in.Code, in.Name, in.Holidays, *in.WeekendDays, in.Shift))
		return err
	})
	if isUniqueViolation(err) {
		return domain.PaymentCalendar{}, httpx.NewProblem(http.StatusConflict, CodeCalendarExists, "the payment calendar code is already taken")
	}
	return c, err
}

func (s *Postgres) CreatePaymentTerm(ctx context.Context, in domain.NewPaymentTerm) (domain.PaymentTerm, error) {
	var p [3]*float64
	var d [3]*int
	for i, tier := range in.Discounts {
		p[i], d[i] = &tier.Percent, &tier.Days
	}
	var t domain.PaymentTerm
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		t, err = scanPaymentTermRow(tx.QueryRow(ctx, `with t as (
			insert into payment_term (tenant_id, id, code, name, net_days, day_of_month, offset_months, calendar_id,
				discount1_percent, discount1_days, discount2_percent, discount2_days, discount3_percent, discount3_days)
			values ($1, $2, $3, $4, $5, $6, $7, nullif($8, '')::uuid, $9, $10, $11, $12, $13, $14) returning *)
			select `+paymentTermFields+` from t`,
			tenantOf(ctx), ids.New(), in.Code, in.Name, in.NetDays, in.DayOfMonth, in.OffsetMonths, in.CalendarID,
			p[0], d[0], p[1], d[1], p[2], d[2]))
		return err
	})
	switch {
	case isUniqueViolation(err):
		return domain.PaymentTerm{}, httpx.NewProblem(http.StatusConflict, CodeTermExists, "the payment term code is already taken")
	case isForeignKeyViolation(err):
		problem := httpx.NewProblem(http.StatusUnprocessableEntity, CodeTermInvalid, "payment term failed validation")
		problem.Errors = map[string]string{"calendarId": "no such payment calendar"}
		return domain.PaymentTerm{}, problem
	}
	return t, err
}

func (s *Postgres) TermForDates(ctx context.Context, id string) (domain.PaymentTerm, error) {
	return getRow(ctx, s, func(row pgx.Row) (domain.PaymentTerm, error) {
		var c struct {
			ID, Code, Name, Shift *string
			Holidays              []time.Time
			WeekendDays           []int
			Version               *int
		}
		var p [3]*float64
		var d [3]*int
		var t domain.PaymentTerm
		err := row.Scan(&t.ID, &t.Code, &t.Name, &t.NetDays, &t.DayOfMonth, &t.OffsetMonths, &t.Version, &t.CalendarID,
			&p[0], &d[0], &p[1], &d[1], &p[2], &d[2], &c.ID, &c.Code, &c.Name, &c.Holidays, &c.WeekendDays, &c.Shift, &c.Version)
		t.Discounts = discountsOf(p, d)
		if err == nil && c.ID != nil {
			cal := domain.PaymentCalendar{ID: *c.ID, Code: *c.Code, Name: *c.Name, WeekendDays: c.WeekendDays, Shift: *c.Shift, Version: *c.Version, Holidays: make([]string, 0, len(c.Holidays))}
			for _, h := range c.Holidays {
				cal.Holidays = append(cal.Holidays, h.Format(time.DateOnly))
			}
			t.Calendar = &cal
		}
		return t, err
	}, CodePaymentTermNotFound, `select `+paymentTermFields+`, c.id::text, c.code, c.name, c.holidays, c.weekend_days::int[], c.shift, c.version
		from payment_term t left join payment_calendar c on c.tenant_id = t.tenant_id and c.id = t.calendar_id
		where t.tenant_id = $1 and t.id = $2`, id)
}
