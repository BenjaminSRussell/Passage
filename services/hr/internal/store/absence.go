package store

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/hr/internal/domain"
	hrevents "github.com/BenjaminSRussell/passage/services/hr/internal/events"
)

// Absence problem codes. HCM-ABS-001..003 are the 8.42.18 catalog (raised by
// the domain); the named ones are this service's and listed in the README.
const (
	CodeAbsencePlanNotFound    = "HCM-ABS-PLAN-NOT-FOUND"
	CodeAbsencePlanDuplicate   = "HCM-ABS-PLAN-DUPLICATE"
	CodeAbsenceRequestNotFound = "HCM-ABS-REQ-NOT-FOUND"
	CodeAccrualOverlap         = "HCM-ABS-RUN-OVERLAP"
	CodeYearOpen               = "HCM-ABS-YEAR-OPEN"
	CodeNotDue                 = "HCM-ABS-NOT-DUE"
	CodeNoExpiry               = "HCM-ABS-NO-EXPIRY"
	CodeNotOwnAbsence          = "HCM-ABS-NOT-OWN"
	CodeYearClosed             = "HCM-ABS-YEAR-CLOSED"

	absDateLayout   = "2006-01-02"
	bucketCurrent   = "CURRENT"
	bucketCarryOver = "CARRY_OVER"
	closeYearEnd    = "YEAR_END"
	closeCarry      = "CARRY_EXPIRY"
)

// AbsencePlan is a plan as stored. Decimal fields travel as strings and are
// parsed where arithmetic happens, so no float reaches the ledger.
type AbsencePlan struct {
	ID                     string    `json:"id"`
	Code                   string    `json:"code"`
	Unit                   string    `json:"unit"`
	AccrualFrequency       string    `json:"accrualFrequency"`
	AccrualRate            string    `json:"accrualRate"`
	AccrualDay             string    `json:"accrualDay"`
	Proration              string    `json:"proration"`
	MaxBalance             string    `json:"maxBalance"`
	CarryOverMax           string    `json:"carryOverMax"`
	CarryOverExpiryMonths  int       `json:"carryOverExpiryMonths"`
	PlanYearStart          string    `json:"planYearStart"`
	NegativeBalanceAllowed bool      `json:"negativeBalanceAllowed"`
	MinServiceDays         int       `json:"minServiceDays"`
	CreatedAt              time.Time `json:"createdAt"`
}

const absPlanColumns = `id::text, code, unit, accrual_frequency, accrual_rate::text, accrual_day, proration,
	max_balance::text, carry_over_max::text, carry_over_expiry_months, plan_year_start,
	negative_balance_allowed, min_service_days, created_at`

func absScanPlan(row pgx.Row) (AbsencePlan, error) {
	var p AbsencePlan
	err := row.Scan(&p.ID, &p.Code, &p.Unit, &p.AccrualFrequency, &p.AccrualRate, &p.AccrualDay, &p.Proration,
		&p.MaxBalance, &p.CarryOverMax, &p.CarryOverExpiryMonths, &p.PlanYearStart,
		&p.NegativeBalanceAllowed, &p.MinServiceDays, &p.CreatedAt)
	p.CreatedAt = utc(p.CreatedAt)
	return p, err
}

func sameDecimal(a, b string) bool {
	x, errX := decimal.NewFromString(a)
	y, errY := decimal.NewFromString(b)
	return errX == nil && errY == nil && x.Equal(y)
}

// samePlan is the replay check: a stored plan answers a retried key only if
// the retry asks for the same plan.
func samePlan(a, b AbsencePlan) bool {
	return a.Code == b.Code && a.Unit == b.Unit && a.AccrualFrequency == b.AccrualFrequency && a.AccrualDay == b.AccrualDay &&
		a.Proration == b.Proration && sameDecimal(a.AccrualRate, b.AccrualRate) && sameDecimal(a.MaxBalance, b.MaxBalance) &&
		sameDecimal(a.CarryOverMax, b.CarryOverMax) && a.CarryOverExpiryMonths == b.CarryOverExpiryMonths &&
		a.PlanYearStart == b.PlanYearStart && a.NegativeBalanceAllowed == b.NegativeBalanceAllowed && a.MinServiceDays == b.MinServiceDays
}

// CreateAbsencePlan stores a plan. A replayed key with the same plan returns the
// stored plan with inserted false; with a different plan it is refused.
func (s *Postgres) CreateAbsencePlan(ctx context.Context, in AbsencePlan, idempotencyKey string) (AbsencePlan, bool, error) {
	p := principal(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out AbsencePlan
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			found, err := absScanPlan(tx.QueryRow(ctx, `select `+absPlanColumns+` from absence_plan where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				out = found
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		row, err := absScanPlan(tx.QueryRow(ctx, `insert into absence_plan (tenant_id, id, code, unit, accrual_frequency, accrual_rate,
				accrual_day, proration, max_balance, carry_over_max, carry_over_expiry_months, plan_year_start,
				negative_balance_allowed, min_service_days, idempotency_key)
			values ($1, $2, $3, $4, $5, $6::numeric, $7, $8, $9::numeric, $10::numeric, $11, $12, $13, $14, $15)
			returning `+absPlanColumns,
			p.TenantID, ids.New(), in.Code, in.Unit, in.AccrualFrequency, in.AccrualRate, in.AccrualDay, in.Proration,
			in.MaxBalance, in.CarryOverMax, in.CarryOverExpiryMonths, in.PlanYearStart, in.NegativeBalanceAllowed,
			in.MinServiceDays, key))
		if err != nil {
			return err
		}
		out, inserted = row, true
		return nil
	})
	if idempotencyRace(err) {
		return AbsencePlan{}, false, idempotencyInFlight()
	}
	if pgCode(err, uniqueViolation) {
		return AbsencePlan{}, false, problem(http.StatusConflict, CodeAbsencePlanDuplicate, "an absence plan with this code already exists")
	}
	if err == nil && !inserted && !samePlan(out, in) {
		return AbsencePlan{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

// AbsencePlanPage is one keyset page of plans ordered by code.
type AbsencePlanPage struct {
	Items     []AbsencePlan
	NextAfter string
}

// ListAbsencePlans pages the tenant's plans by code.
func (s *Postgres) ListAbsencePlans(ctx context.Context, after string, limit int) (AbsencePlanPage, error) {
	p := principal(ctx)
	page := AbsencePlanPage{Items: []AbsencePlan{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+absPlanColumns+` from absence_plan where tenant_id = $1 and code > $2 order by code limit $3`,
			p.TenantID, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			plan, err := absScanPlan(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, plan)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextAfter = page.Items[limit-1].Code
	}
	return page, err
}

// AccrualPeriodView summarises one accrual period of a plan.
type AccrualPeriodView struct {
	PeriodStart string `json:"periodStart"`
	PeriodEnd   string `json:"periodEnd"`
	Workers     int    `json:"workers"`
	Accrued     string `json:"accrued"`
	Discarded   string `json:"discarded"`
}

// CloseView summarises one year end or carry-over expiry of a plan.
type CloseView struct {
	PlanYearStart string `json:"planYearStart"`
	Kind          string `json:"kind"`
	Workers       int    `json:"workers"`
	Carried       string `json:"carried"`
	Expired       string `json:"expired"`
}

// AbsencePlanDetail is a plan with its latest accrual periods and closes.
type AbsencePlanDetail struct {
	AbsencePlan
	Periods []AccrualPeriodView `json:"periods"`
	Closes  []CloseView         `json:"closes"`
}

// recentLimit bounds the run history on the plan detail.
const recentLimit = 24

// GetAbsencePlan returns a plan with its most recent accrual periods and closes.
func (s *Postgres) GetAbsencePlan(ctx context.Context, id string) (AbsencePlanDetail, error) {
	p := principal(ctx)
	out := AbsencePlanDetail{Periods: []AccrualPeriodView{}, Closes: []CloseView{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		plan, err := absLoadPlan(ctx, tx, p.TenantID, id)
		if err != nil {
			return err
		}
		out.AbsencePlan = plan
		rows, err := tx.Query(ctx, `select to_char(ap.period_start, 'YYYY-MM-DD'), to_char(ap.period_end, 'YYYY-MM-DD'),
				count(l.worker_id)::int, round(coalesce(sum(l.amount), 0), 2)::text, round(coalesce(sum(l.discarded), 0), 2)::text
			from absence_accrual_period ap
			left join absence_accrual_line l on l.tenant_id = ap.tenant_id and l.plan_id = ap.plan_id and l.period_start = ap.period_start
			where ap.tenant_id = $1 and ap.plan_id = $2
			group by ap.period_start, ap.period_end order by ap.period_start desc limit $3`, p.TenantID, id, recentLimit)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v AccrualPeriodView
			if err := rows.Scan(&v.PeriodStart, &v.PeriodEnd, &v.Workers, &v.Accrued, &v.Discarded); err != nil {
				rows.Close()
				return err
			}
			out.Periods = append(out.Periods, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `select to_char(plan_year_start, 'YYYY-MM-DD'), kind, count(*)::int,
				round(sum(carried), 2)::text, round(sum(expired), 2)::text
			from absence_close_line where tenant_id = $1 and plan_id = $2
			group by plan_year_start, kind order by plan_year_start desc, kind limit $3`, p.TenantID, id, recentLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v CloseView
			if err := rows.Scan(&v.PlanYearStart, &v.Kind, &v.Workers, &v.Carried, &v.Expired); err != nil {
				return err
			}
			out.Closes = append(out.Closes, v)
		}
		return rows.Err()
	})
	return out, err
}

// absLoadPlan reads a plan inside tx. A malformed or unknown id is a 404.
func absLoadPlan(ctx context.Context, tx pgx.Tx, tenantID, id string) (AbsencePlan, error) {
	if !ids.Valid(id) {
		return AbsencePlan{}, problem(http.StatusNotFound, CodeAbsencePlanNotFound, "absence plan not found")
	}
	plan, err := absScanPlan(tx.QueryRow(ctx, `select `+absPlanColumns+` from absence_plan where tenant_id = $1 and id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return AbsencePlan{}, problem(http.StatusNotFound, CodeAbsencePlanNotFound, "absence plan not found")
	}
	return plan, err
}

// absLockWorker locks the worker row (FOR NO KEY UPDATE, so inserts that only
// reference the worker are not blocked) and returns its sign-in user. Every
// balance read-modify-write takes this lock first, so a concurrent approval,
// accrual or year end on the same worker cannot overwrite each other's ledger.
func absLockWorker(ctx context.Context, tx pgx.Tx, tenantID, workerID string) (*string, error) {
	var user *string
	err := tx.QueryRow(ctx, `select user_id::text from worker where tenant_id = $1 and id = $2 for no key update`, tenantID, workerID).Scan(&user)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
	}
	return user, err
}

// absWorkerUser reads the worker's sign-in user without locking.
func absWorkerUser(ctx context.Context, tx pgx.Tx, tenantID, workerID string) (*string, error) {
	var user *string
	err := tx.QueryRow(ctx, `select user_id::text from worker where tenant_id = $1 and id = $2`, tenantID, workerID).Scan(&user)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
	}
	return user, err
}

// absCheckOwn refuses an employee-only caller acting on another worker's
// absence (hr.absence.self is own data, 8.42.17).
func absCheckOwn(ownOnly bool, callerUserID string, workerUser *string) error {
	if !ownOnly {
		return nil
	}
	if workerUser == nil || callerUserID == "" || *workerUser != callerUserID {
		return problem(http.StatusForbidden, CodeNotOwnAbsence, "you can only see and request your own absence")
	}
	return nil
}

// absLoadLedger reads both buckets of one worker's plan year. No rows means zero.
func absLoadLedger(ctx context.Context, tx pgx.Tx, tenantID, workerID, planID string, yearStart time.Time) (domain.Ledger, error) {
	var l domain.Ledger
	rows, err := tx.Query(ctx, `select bucket, accrued::text, used::text, adjusted::text, expired::text
		from absence_balance where tenant_id = $1 and worker_id = $2 and plan_id = $3 and period_start = $4::date`,
		tenantID, workerID, planID, yearStart)
	if err != nil {
		return l, err
	}
	defer rows.Close()
	for rows.Next() {
		var bucket, accrued, used, adjusted, expired string
		if err := rows.Scan(&bucket, &accrued, &used, &adjusted, &expired); err != nil {
			return l, err
		}
		b, err := parseBucket(accrued, used, adjusted, expired)
		if err != nil {
			return l, err
		}
		if bucket == bucketCarryOver {
			l.Carry = b
		} else {
			l.Current = b
		}
	}
	return l, rows.Err()
}

func parseBucket(accrued, used, adjusted, expired string) (domain.Bucket, error) {
	var b domain.Bucket
	var err error
	for _, f := range []struct {
		dst *decimal.Decimal
		raw string
	}{{&b.Accrued, accrued}, {&b.Used, used}, {&b.Adjusted, adjusted}, {&b.Expired, expired}} {
		if *f.dst, err = decimal.NewFromString(f.raw); err != nil {
			return b, err
		}
	}
	return b, nil
}

// absSaveLedger upserts both buckets of one plan year.
func absSaveLedger(ctx context.Context, tx pgx.Tx, tenantID, workerID, planID string, yearStart time.Time, l domain.Ledger) error {
	for _, row := range []struct {
		bucket string
		b      domain.Bucket
	}{{bucketCarryOver, l.Carry}, {bucketCurrent, l.Current}} {
		if _, err := tx.Exec(ctx, `insert into absence_balance (tenant_id, worker_id, plan_id, period_start, bucket, accrued, used, adjusted, expired, updated_at)
			values ($1, $2, $3, $4::date, $5, $6::numeric, $7::numeric, $8::numeric, $9::numeric, now())
			on conflict (tenant_id, worker_id, plan_id, period_start, bucket) do update set
				accrued = excluded.accrued, used = excluded.used, adjusted = excluded.adjusted,
				expired = excluded.expired, updated_at = now()`,
			tenantID, workerID, planID, yearStart, row.bucket, row.b.Accrued.String(), row.b.Used.String(),
			row.b.Adjusted.String(), row.b.Expired.String()); err != nil {
			return err
		}
	}
	return nil
}

// absYearClosed refuses a change to a plan year that has had its year end:
// its balance was already carried into the next year, so spending or
// crediting it again would count the same leave twice. An empty workerID
// checks the plan year for any worker.
func absYearClosed(ctx context.Context, tx pgx.Tx, tenantID, planID, workerID string, yearStart time.Time) error {
	var closed bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from absence_close_line where tenant_id = $1 and plan_id = $2
		and plan_year_start = $3::date and kind = 'YEAR_END' and ($4 = '' or worker_id::text = $4))`,
		tenantID, planID, yearStart, workerID).Scan(&closed); err != nil {
		return err
	}
	if closed {
		return problem(http.StatusConflict, CodeYearClosed, "the plan year starting "+yearStart.Format(absDateLayout)+" is closed; its balance has been carried over")
	}
	return nil
}

// today is the store clock's UTC day.
func (s *Postgres) today() time.Time {
	n := s.now().UTC()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

// AccrualRun is what one accrual call did for one (plan, period).
type AccrualRun struct {
	PlanID        string `json:"planId"`
	PeriodStart   string `json:"periodStart"`
	PeriodEnd     string `json:"periodEnd"`
	PlanYearStart string `json:"planYearStart"`
	// Credited is the number of workers this call accrued; AlreadyAccrued the
	// workers an earlier call for the same period had accrued.
	Credited       int    `json:"credited"`
	AlreadyAccrued int    `json:"alreadyAccrued"`
	Accrued        string `json:"accrued"`
	Discarded      string `json:"discarded"`
}

// absEmployed is one worker's PRIMARY employment in the period: the earliest
// hire date and the employed day spans.
type absEmployed struct {
	hire  time.Time
	spans []domain.DaySpan
}

// absEmploymentsIn reads PRIMARY employments overlapping the period (BR-42.14:
// balances accrue on PRIMARY only). Validity is [valid_from, valid_to) in
// instants, so the last employed day is valid_to − 1. The read runs under the
// caller's legal-entity scope; a worker outside it is left for a run with that
// scope, which the per-worker accrual line makes safe.
func absEmploymentsIn(ctx context.Context, tx pgx.Tx, tenantID string, from, to time.Time) (map[string]*absEmployed, error) {
	rows, err := tx.Query(ctx, `select worker_id::text, (valid_from at time zone 'UTC')::date,
			case when valid_to is null then null else (valid_to at time zone 'UTC')::date - 1 end, hire_date
		from employment
		where tenant_id = $1 and assignment_kind = 'PRIMARY'
			and validity && tstzrange($2::timestamptz, $3::timestamptz, '[)')`,
		tenantID, from, to.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*absEmployed{}
	for rows.Next() {
		var workerID string
		var start, hire time.Time
		var end *time.Time
		if err := rows.Scan(&workerID, &start, &end, &hire); err != nil {
			return nil, err
		}
		e := out[workerID]
		if e == nil {
			e = &absEmployed{hire: hire}
			out[workerID] = e
		} else if hire.Before(e.hire) {
			e.hire = hire
		}
		e.spans = append(e.spans, domain.DaySpan{From: start, To: end})
	}
	return out, rows.Err()
}

func absProblem(err error) error {
	var de *domain.Error
	if !errors.As(err, &de) {
		return err
	}
	status := http.StatusUnprocessableEntity
	if de.Code == domain.CodeSelfDecision {
		status = http.StatusForbidden
	}
	return problem(status, de.Code, de.Detail)
}

// RunAccrual accrues one plan for one whole accrual period (BR-42.3) across
// every worker with a PRIMARY employment in it. Each credited worker gets an
// accrual line keyed (plan, period, worker), so a rerun credits only workers
// not yet accrued and never doubles anyone (8.42.16 hr-accrue).
func (s *Postgres) RunAccrual(ctx context.Context, planID string, from, to time.Time) (AccrualRun, error) {
	p := principal(ctx)
	var run AccrualRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		plan, err := absLoadPlan(ctx, tx, p.TenantID, planID)
		if err != nil {
			return err
		}
		if err := domain.CheckAccrualPeriod(plan.AccrualFrequency, plan.PlanYearStart, from, to); err != nil {
			return absProblem(err)
		}
		accrualDay := to
		if plan.AccrualDay == "FIRST_DAY" {
			accrualDay = from
		}
		if accrualDay.After(s.today()) {
			return problem(http.StatusUnprocessableEntity, CodeNotDue, "this period accrues on "+accrualDay.Format(absDateLayout)+"; run it on or after that day")
		}
		yearStart := domain.PlanYearStart(plan.PlanYearStart, accrualDay)
		if err := absYearClosed(ctx, tx, p.TenantID, planID, "", yearStart); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx, `insert into absence_accrual_period (tenant_id, plan_id, period_start, period_end)
			values ($1, $2, $3::date, $4::date) on conflict do nothing`, p.TenantID, planID, from, to); err != nil {
			return err
		}
		var storedEnd time.Time
		err = tx.QueryRow(ctx, `select period_end from absence_accrual_period where tenant_id = $1 and plan_id = $2 and period_start = $3::date`,
			p.TenantID, planID, from).Scan(&storedEnd)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !storedEnd.Equal(to)) {
			return problem(http.StatusConflict, CodeAccrualOverlap, "another accrual period of this plan already covers some of these days")
		}
		if err != nil {
			return err
		}

		var open int
		prev := yearStart.AddDate(-1, 0, 0)
		if err := tx.QueryRow(ctx, `select count(distinct b.worker_id) from absence_balance b
			where b.tenant_id = $1 and b.plan_id = $2 and b.period_start = $3::date
				and not exists (select 1 from absence_close_line c where c.tenant_id = b.tenant_id and c.plan_id = b.plan_id
					and c.plan_year_start = b.period_start and c.kind = 'YEAR_END' and c.worker_id = b.worker_id)`,
			p.TenantID, planID, prev).Scan(&open); err != nil {
			return err
		}
		if open > 0 {
			return problem(http.StatusConflict, CodeYearOpen, "close the plan year starting "+prev.Format(absDateLayout)+" before accruing the next one")
		}

		done := map[string]bool{}
		rows, err := tx.Query(ctx, `select worker_id::text from absence_accrual_line where tenant_id = $1 and plan_id = $2 and period_start = $3::date`,
			p.TenantID, planID, from)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			done[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		employed, err := absEmploymentsIn(ctx, tx, p.TenantID, from, to)
		if err != nil {
			return err
		}
		rate, err := decimal.NewFromString(plan.AccrualRate)
		if err != nil {
			return err
		}
		limit, err := decimal.NewFromString(plan.MaxBalance)
		if err != nil {
			return err
		}
		workerIDs := make([]string, 0, len(employed))
		for id := range employed {
			workerIDs = append(workerIDs, id)
		}
		sort.Strings(workerIDs)

		run = AccrualRun{PlanID: planID, PeriodStart: from.Format(absDateLayout), PeriodEnd: to.Format(absDateLayout),
			PlanYearStart: yearStart.Format(absDateLayout), AlreadyAccrued: len(done)}
		accrued, discarded := decimal.Zero, decimal.Zero
		for _, workerID := range workerIDs {
			if done[workerID] {
				continue
			}
			e := employed[workerID]
			amount := domain.PeriodAccrual(domain.PeriodInput{
				Rate: rate, Prorate: plan.Proration == "DAYS_IN_PERIOD", From: from, To: to,
				Employed: e.spans, HireDate: e.hire, MinServiceDays: plan.MinServiceDays,
			})
			if !amount.IsPositive() {
				continue
			}
			if _, err := absLockWorker(ctx, tx, p.TenantID, workerID); err != nil {
				return err
			}
			ledger, err := absLoadLedger(ctx, tx, p.TenantID, workerID, planID, yearStart)
			if err != nil {
				return err
			}
			ledger, excess := ledger.Accrue(amount, limit)
			if excess.IsPositive() {
				slog.Info("absence accrual capped at max balance", "plan", planID, "worker", workerID, "discarded", excess.StringFixed(2))
			}
			if err := absSaveLedger(ctx, tx, p.TenantID, workerID, planID, yearStart, ledger); err != nil {
				return err
			}
			kept := amount.Sub(excess)
			if _, err := tx.Exec(ctx, `insert into absence_accrual_line (tenant_id, plan_id, period_start, worker_id, plan_year_start, employed_days, amount, discarded)
				values ($1, $2, $3::date, $4, $5::date, $6, $7::numeric, $8::numeric)`,
				p.TenantID, planID, from, workerID, yearStart, domain.CoveredDays(from, to, e.spans), kept.String(), excess.String()); err != nil {
				return err
			}
			accrued, discarded = accrued.Add(kept), discarded.Add(excess)
			run.Credited++
		}
		run.Accrued, run.Discarded = accrued.StringFixed(2), discarded.StringFixed(2)
		return nil
	})
	return run, err
}

// CloseRun is what one year-end or carry-over expiry call did.
type CloseRun struct {
	PlanID        string `json:"planId"`
	PlanYearStart string `json:"planYearStart"`
	Kind          string `json:"kind"`
	Workers       int    `json:"workers"`
	AlreadyClosed int    `json:"alreadyClosed"`
	Carried       string `json:"carried"`
	Expired       string `json:"expired"`
}

// absPlanYear checks that yearStart is the first day of one of the plan's years.
func absPlanYear(plan AbsencePlan, yearStart time.Time) error {
	if !domain.PlanYearStart(plan.PlanYearStart, yearStart).Equal(yearStart) {
		return problem(http.StatusUnprocessableEntity, domain.CodePeriod, "the plan year of "+plan.Code+" starts on "+plan.PlanYearStart+" (MM-DD)")
	}
	return nil
}

// absCloseWorkers lists workers with a bucket in the plan year that have no
// close line of kind yet, sorted so locks are always taken in the same order,
// and counts those already closed.
func absCloseWorkers(ctx context.Context, tx pgx.Tx, tenantID, planID string, yearStart time.Time, kind, bucket string) ([]string, int, error) {
	rows, err := tx.Query(ctx, `select distinct b.worker_id::text,
			exists (select 1 from absence_close_line c where c.tenant_id = b.tenant_id and c.plan_id = b.plan_id
				and c.plan_year_start = b.period_start and c.kind = $4 and c.worker_id = b.worker_id)
		from absence_balance b
		where b.tenant_id = $1 and b.plan_id = $2 and b.period_start = $3::date and ($5 = '' or b.bucket = $5)
		order by 1`, tenantID, planID, yearStart, kind, bucket)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var open []string
	closed := 0
	for rows.Next() {
		var id string
		var done bool
		if err := rows.Scan(&id, &done); err != nil {
			return nil, 0, err
		}
		if done {
			closed++
		} else {
			open = append(open, id)
		}
	}
	return open, closed, rows.Err()
}

func absInsertClose(ctx context.Context, tx pgx.Tx, tenantID, planID string, yearStart time.Time, kind, workerID string, carried, expired decimal.Decimal) error {
	_, err := tx.Exec(ctx, `insert into absence_close_line (tenant_id, plan_id, plan_year_start, kind, worker_id, carried, expired)
		values ($1, $2, $3::date, $4, $5, $6::numeric, $7::numeric)`, tenantID, planID, yearStart, kind, workerID, carried.String(), expired.String())
	return err
}

// CloseYear is the BR-42.3 year end for one plan year: each worker's total
// carries into the next plan year's CARRY_OVER bucket up to carry_over_max and
// the rest expires. It can run once the plan year has ended; a rerun closes
// only workers not yet closed.
func (s *Postgres) CloseYear(ctx context.Context, planID string, yearStart time.Time) (CloseRun, error) {
	p := principal(ctx)
	var run CloseRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		plan, err := absLoadPlan(ctx, tx, p.TenantID, planID)
		if err != nil {
			return err
		}
		if err := absPlanYear(plan, yearStart); err != nil {
			return err
		}
		next := yearStart.AddDate(1, 0, 0)
		if next.After(s.today()) {
			return problem(http.StatusUnprocessableEntity, CodeNotDue, "the plan year ends on "+next.AddDate(0, 0, -1).Format(absDateLayout)+"; close it after that day")
		}
		carryMax, err := decimal.NewFromString(plan.CarryOverMax)
		if err != nil {
			return err
		}
		workers, closed, err := absCloseWorkers(ctx, tx, p.TenantID, planID, yearStart, closeYearEnd, "")
		if err != nil {
			return err
		}
		run = CloseRun{PlanID: planID, PlanYearStart: yearStart.Format(absDateLayout), Kind: closeYearEnd, AlreadyClosed: closed}
		carriedSum, expiredSum := decimal.Zero, decimal.Zero
		for _, workerID := range workers {
			if _, err := absLockWorker(ctx, tx, p.TenantID, workerID); err != nil {
				return err
			}
			ledger, err := absLoadLedger(ctx, tx, p.TenantID, workerID, planID, yearStart)
			if err != nil {
				return err
			}
			closedLedger, carried, expired := domain.CloseYear(ledger, carryMax)
			if err := absSaveLedger(ctx, tx, p.TenantID, workerID, planID, yearStart, closedLedger); err != nil {
				return err
			}
			if !carried.IsZero() {
				following, err := absLoadLedger(ctx, tx, p.TenantID, workerID, planID, next)
				if err != nil {
					return err
				}
				following.Carry.Accrued = following.Carry.Accrued.Add(carried)
				if err := absSaveLedger(ctx, tx, p.TenantID, workerID, planID, next, following); err != nil {
					return err
				}
			}
			if err := absInsertClose(ctx, tx, p.TenantID, planID, yearStart, closeYearEnd, workerID, carried, expired); err != nil {
				return err
			}
			carriedSum, expiredSum = carriedSum.Add(carried), expiredSum.Add(expired)
			run.Workers++
		}
		run.Carried, run.Expired = carriedSum.StringFixed(2), expiredSum.StringFixed(2)
		return nil
	})
	return run, err
}

// ExpireCarryOver ends the CARRY_OVER buckets of one plan year once
// carry_over_expiry_months have passed since the year started (BR-42.3:
// unused carry-over expires). A plan with 0 months keeps carry-over for the
// whole year, and the call is refused.
func (s *Postgres) ExpireCarryOver(ctx context.Context, planID string, yearStart time.Time) (CloseRun, error) {
	p := principal(ctx)
	var run CloseRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		plan, err := absLoadPlan(ctx, tx, p.TenantID, planID)
		if err != nil {
			return err
		}
		if err := absPlanYear(plan, yearStart); err != nil {
			return err
		}
		if plan.CarryOverExpiryMonths == 0 {
			return problem(http.StatusUnprocessableEntity, CodeNoExpiry, "carry-over of "+plan.Code+" does not expire within the plan year")
		}
		expiry := yearStart.AddDate(0, plan.CarryOverExpiryMonths, 0)
		if expiry.After(s.today()) {
			return problem(http.StatusUnprocessableEntity, CodeNotDue, "carry-over expires at the end of "+expiry.AddDate(0, 0, -1).Format(absDateLayout)+"; run it after that day")
		}
		workers, closed, err := absCloseWorkers(ctx, tx, p.TenantID, planID, yearStart, closeCarry, bucketCarryOver)
		if err != nil {
			return err
		}
		run = CloseRun{PlanID: planID, PlanYearStart: yearStart.Format(absDateLayout), Kind: closeCarry, AlreadyClosed: closed}
		expiredSum := decimal.Zero
		for _, workerID := range workers {
			if _, err := absLockWorker(ctx, tx, p.TenantID, workerID); err != nil {
				return err
			}
			ledger, err := absLoadLedger(ctx, tx, p.TenantID, workerID, planID, yearStart)
			if err != nil {
				return err
			}
			ledger, gone := domain.ExpireCarryOver(ledger)
			if err := absSaveLedger(ctx, tx, p.TenantID, workerID, planID, yearStart, ledger); err != nil {
				return err
			}
			if err := absInsertClose(ctx, tx, p.TenantID, planID, yearStart, closeCarry, workerID, decimal.Zero, gone); err != nil {
				return err
			}
			expiredSum = expiredSum.Add(gone)
			run.Workers++
		}
		run.Carried, run.Expired = "0.00", expiredSum.StringFixed(2)
		return nil
	})
	return run, err
}

// BucketView is one bucket with derived balance, in display precision.
type BucketView struct {
	Accrued  string `json:"accrued"`
	Used     string `json:"used"`
	Adjusted string `json:"adjusted"`
	Expired  string `json:"expired"`
	Balance  string `json:"balance"`
}

// BalanceView is one worker's balance on one plan for the plan year that
// contains the as-of date (FR-42.6).
type BalanceView struct {
	PlanID        string     `json:"planId"`
	PlanCode      string     `json:"planCode"`
	Unit          string     `json:"unit"`
	PlanYearStart string     `json:"planYearStart"`
	CarryOver     BucketView `json:"carryOver"`
	Current       BucketView `json:"current"`
	Total         string     `json:"total"`
}

func absBucketView(b domain.Bucket) BucketView {
	return BucketView{
		Accrued: b.Accrued.StringFixed(2), Used: b.Used.StringFixed(2), Adjusted: b.Adjusted.StringFixed(2),
		Expired: b.Expired.StringFixed(2), Balance: b.Balance().StringFixed(2),
	}
}

// Balances lists a worker's balances as of a date: one entry per plan the
// worker has ever had a balance on, for the plan year containing asOf (zeros
// when that year has no rows yet), ordered by plan code. An ownOnly caller
// may read only their own worker.
func (s *Postgres) Balances(ctx context.Context, workerID string, asOf time.Time, ownOnly bool) ([]BalanceView, error) {
	p := principal(ctx)
	out := []BalanceView{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		user, err := absWorkerUser(ctx, tx, p.TenantID, workerID)
		if err != nil {
			return err
		}
		if err := absCheckOwn(ownOnly, p.UserID, user); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select p.id::text, p.code, p.unit, p.plan_year_start, b.period_start, b.bucket,
				b.accrued::text, b.used::text, b.adjusted::text, b.expired::text
			from absence_balance b join absence_plan p on p.tenant_id = b.tenant_id and p.id = b.plan_id
			where b.tenant_id = $1 and b.worker_id = $2 order by p.code, p.id`, p.TenantID, workerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		index := map[string]int{}
		ledgers := []domain.Ledger{}
		for rows.Next() {
			var planID, code, unit, pys, bucket, accrued, used, adjusted, expired string
			var period time.Time
			if err := rows.Scan(&planID, &code, &unit, &pys, &period, &bucket, &accrued, &used, &adjusted, &expired); err != nil {
				return err
			}
			year := domain.PlanYearStart(pys, asOf)
			i, seen := index[planID]
			if !seen {
				i = len(out)
				index[planID] = i
				out = append(out, BalanceView{PlanID: planID, PlanCode: code, Unit: unit, PlanYearStart: year.Format(absDateLayout)})
				ledgers = append(ledgers, domain.Ledger{})
			}
			if !period.Equal(year) {
				continue
			}
			b, err := parseBucket(accrued, used, adjusted, expired)
			if err != nil {
				return err
			}
			if bucket == bucketCarryOver {
				ledgers[i].Carry = b
			} else {
				ledgers[i].Current = b
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range out {
			out[i].CarryOver = absBucketView(ledgers[i].Carry)
			out[i].Current = absBucketView(ledgers[i].Current)
			out[i].Total = ledgers[i].Total().StringFixed(2)
		}
		return nil
	})
	return out, err
}

// AbsenceRequest is a request as stored, with the worker and plan it names.
// Dates are YYYY-MM-DD and inclusive.
type AbsenceRequest struct {
	ID              string     `json:"id"`
	WorkerID        string     `json:"workerId"`
	PersonnelNumber string     `json:"personnelNumber"`
	WorkerName      string     `json:"workerName"`
	PlanID          string     `json:"planId"`
	PlanCode        string     `json:"planCode"`
	Unit            string     `json:"unit"`
	From            string     `json:"from"`
	To              string     `json:"to"`
	Quantity        string     `json:"quantity"`
	Status          string     `json:"status"`
	Reason          string     `json:"reason,omitempty"`
	RequestedBy     string     `json:"requestedBy,omitempty"`
	DecidedBy       string     `json:"decidedBy,omitempty"`
	DecidedAt       *time.Time `json:"decidedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
}

// RequestInput is an absence request as submitted.
type RequestInput struct {
	WorkerID string
	PlanID   string
	From, To time.Time
	Quantity decimal.Decimal
	Reason   string
}

const absRequestSelect = `select r.id::text, r.worker_id::text, w.personnel_number, coalesce(w.preferred_name, w.legal_name),
	r.plan_id::text, p.code, p.unit, to_char(r.date_from, 'YYYY-MM-DD'), to_char(r.date_to, 'YYYY-MM-DD'),
	r.quantity::text, r.status, coalesce(r.reason, ''), coalesce(r.requested_by, ''), coalesce(r.decided_by, ''),
	r.decided_at, r.created_at
	from absence_request r
	join worker w on w.tenant_id = r.tenant_id and w.id = r.worker_id
	join absence_plan p on p.tenant_id = r.tenant_id and p.id = r.plan_id`

func absScanRequest(row pgx.Row) (AbsenceRequest, error) {
	var r AbsenceRequest
	err := row.Scan(&r.ID, &r.WorkerID, &r.PersonnelNumber, &r.WorkerName, &r.PlanID, &r.PlanCode, &r.Unit, &r.From, &r.To,
		&r.Quantity, &r.Status, &r.Reason, &r.RequestedBy, &r.DecidedBy, &r.DecidedAt, &r.CreatedAt)
	r.CreatedAt, r.DecidedAt = utc(r.CreatedAt), utcPtr(r.DecidedAt)
	return r, err
}

func absGetRequest(ctx context.Context, tx pgx.Tx, tenantID, id string) (AbsenceRequest, error) {
	r, err := absScanRequest(tx.QueryRow(ctx, absRequestSelect+` where r.tenant_id = $1 and r.id = $2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return AbsenceRequest{}, problem(http.StatusNotFound, CodeAbsenceRequestNotFound, "absence request not found")
	}
	return r, err
}

// quantityProblem is the 400 a quantity above the request's span gets.
func quantityProblem(max decimal.Decimal, unit string) error {
	pr := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
	pr.Errors = map[string]string{"quantity": "must be at most " + max.String() + " " + strings.ToLower(unit) + " for these dates"}
	return pr
}

// CreateAbsenceRequest validates a request against its dates, the balance
// (FR-42.7, AC-42.4) and overlapping requests (BR-42.5), then stores it
// SUBMITTED. Warnings come back on success; a replayed key with the same
// request returns the stored one with inserted false. An ownOnly caller may
// request only for their own worker.
func (s *Postgres) CreateAbsenceRequest(ctx context.Context, in RequestInput, idempotencyKey string, ownOnly bool) (AbsenceRequest, []domain.RuleWarning, bool, error) {
	p := principal(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var out AbsenceRequest
	warnings := []domain.RuleWarning{}
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			found, err := absScanRequest(tx.QueryRow(ctx, absRequestSelect+` where r.tenant_id = $1 and r.idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				out = found
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		user, err := absLockWorker(ctx, tx, p.TenantID, in.WorkerID)
		if err != nil {
			return err
		}
		if err := absCheckOwn(ownOnly, p.UserID, user); err != nil {
			return err
		}
		plan, err := absLoadPlan(ctx, tx, p.TenantID, in.PlanID)
		if err != nil {
			return err
		}
		if max := domain.MaxRequestQuantity(plan.Unit, in.From, in.To); in.Quantity.GreaterThan(max) {
			return quantityProblem(max, plan.Unit)
		}
		yearStart := domain.PlanYearStart(plan.PlanYearStart, in.From)
		if !domain.PlanYearStart(plan.PlanYearStart, in.To).Equal(yearStart) {
			return problem(http.StatusUnprocessableEntity, domain.CodeYearSpan, "the request crosses the plan year starting "+
				domain.PlanYearStart(plan.PlanYearStart, in.To).Format(absDateLayout)+"; split it at that day")
		}
		if err := absYearClosed(ctx, tx, p.TenantID, in.PlanID, in.WorkerID, yearStart); err != nil {
			return err
		}
		spans, err := absOverlapping(ctx, tx, p.TenantID, in.WorkerID, in.From, in.To)
		if err != nil {
			return err
		}
		if warnings, err = domain.CheckOverlap("", in.From, in.To, spans); err != nil {
			return absProblem(err)
		}
		if warnings == nil {
			warnings = []domain.RuleWarning{}
		}
		ledger, err := absLoadLedger(ctx, tx, p.TenantID, in.WorkerID, in.PlanID, yearStart)
		if err != nil {
			return err
		}
		floor, err := absFloor(plan)
		if err != nil {
			return err
		}
		if _, err := ledger.Consume(in.Quantity, floor); err != nil {
			return absProblem(err)
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into absence_request (tenant_id, id, worker_id, plan_id, date_from, date_to,
				quantity, status, reason, requested_by, idempotency_key)
			values ($1, $2, $3, $4, $5::date, $6::date, $7::numeric, 'SUBMITTED', nullif($8, ''), nullif($9, ''), $10)`,
			p.TenantID, id, in.WorkerID, in.PlanID, in.From, in.To, in.Quantity.String(), in.Reason, p.UserID, key); err != nil {
			return err
		}
		out, err = absGetRequest(ctx, tx, p.TenantID, id)
		inserted = err == nil
		return err
	})
	if idempotencyRace(err) {
		return AbsenceRequest{}, nil, false, idempotencyInFlight()
	}
	if err == nil && !inserted && (out.WorkerID != in.WorkerID || out.PlanID != in.PlanID || out.From != in.From.Format(absDateLayout) ||
		out.To != in.To.Format(absDateLayout) || !sameDecimal(out.Quantity, in.Quantity.String())) {
		return AbsenceRequest{}, nil, false, idempotencyReuse()
	}
	return out, warnings, inserted, err
}

func absFloor(plan AbsencePlan) (decimal.Decimal, error) {
	rate, err := decimal.NewFromString(plan.AccrualRate)
	if err != nil {
		return decimal.Zero, err
	}
	return domain.BalanceFloor(rate, plan.NegativeBalanceAllowed), nil
}

// absOverlapping lists SUBMITTED and APPROVED requests of the worker that touch
// the range. Only those two states can block or warn (BR-42.5).
func absOverlapping(ctx context.Context, tx pgx.Tx, tenantID, workerID string, from, to time.Time) ([]domain.RequestSpan, error) {
	rows, err := tx.Query(ctx, `select id::text, status, date_from, date_to from absence_request
		where tenant_id = $1 and worker_id = $2 and status in ('SUBMITTED', 'APPROVED')
			and date_from <= $3::date and date_to >= $4::date`, tenantID, workerID, to, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.RequestSpan
	for rows.Next() {
		var sp domain.RequestSpan
		if err := rows.Scan(&sp.ID, &sp.Status, &sp.From, &sp.To); err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// RequestFilter narrows the request list. OwnUserID, when set, keeps only the
// requests of the worker signed in as that user.
type RequestFilter struct {
	Status    string
	WorkerID  string
	OwnUserID string
}

// AbsenceRequestPage is one keyset page of requests ordered by start date.
type AbsenceRequestPage struct {
	Items     []AbsenceRequest
	NextAfter string
}

// ListAbsenceRequests pages requests by (start date, id), oldest first: the
// approval queue order. after is "YYYY-MM-DD|id" from the previous page.
func (s *Postgres) ListAbsenceRequests(ctx context.Context, f RequestFilter, after string, limit int) (AbsenceRequestPage, error) {
	p := principal(ctx)
	page := AbsenceRequestPage{Items: []AbsenceRequest{}}
	afterDate, afterID := "0001-01-01", ""
	if after != "" {
		d, id, ok := strings.Cut(after, "|")
		if _, err := time.Parse(absDateLayout, d); !ok || err != nil || !ids.Valid(id) {
			return page, problem(http.StatusBadRequest, "COM-0001", "cursor must come from a previous page")
		}
		afterDate, afterID = d, id
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, absRequestSelect+`
			where r.tenant_id = $1 and ($2 = '' or r.status = $2) and ($3 = '' or r.worker_id::text = $3)
				and ($4 = '' or w.user_id::text = $4)
				and (r.date_from, r.id::text) > ($5::date, $6)
			order by r.date_from, r.id limit $7`, p.TenantID, f.Status, f.WorkerID, f.OwnUserID, afterDate, afterID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := absScanRequest(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, r)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.NextAfter = last.From + "|" + last.ID
	}
	return page, err
}

// DecideAbsenceRequest applies APPROVE or REJECT. Approval rechecks the
// approved overlap (BR-42.5) and the balance, consumes it (CARRY_OVER first)
// and writes absence.approved in the same transaction (FR-42.7). The request
// and worker rows are locked, so two approvals cannot both spend one balance.
// Replaying the decision a request already has changes nothing.
func (s *Postgres) DecideAbsenceRequest(ctx context.Context, requestID, decision string) (AbsenceRequest, bool, error) {
	p := principal(ctx)
	var out AbsenceRequest
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status, workerID string
		err := tx.QueryRow(ctx, `select status, worker_id::text from absence_request where tenant_id = $1 and id = $2 for update`,
			p.TenantID, requestID).Scan(&status, &workerID)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeAbsenceRequestNotFound, "absence request not found")
		}
		if err != nil {
			return err
		}
		requester, err := absLockWorker(ctx, tx, p.TenantID, workerID)
		if err != nil {
			return err
		}
		if err := domain.CheckNotSelf(p.UserID, requester); err != nil {
			return absProblem(err)
		}
		next, moved, err := domain.NextRequestStatus(status, decision)
		if err != nil {
			return absProblem(err)
		}
		req, err := absGetRequest(ctx, tx, p.TenantID, requestID)
		if err != nil {
			return err
		}
		if !moved {
			out = req
			return nil
		}
		if decision == domain.DecisionApprove {
			if err := s.approve(ctx, tx, p.TenantID, req); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update absence_request set status = $3, decided_by = nullif($4, ''), decided_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, requestID, next, p.UserID); err != nil {
			return err
		}
		out, err = absGetRequest(ctx, tx, p.TenantID, requestID)
		changed = err == nil
		return err
	})
	return out, changed, err
}

func (s *Postgres) approve(ctx context.Context, tx pgx.Tx, tenantID string, req AbsenceRequest) error {
	from, err := time.Parse(absDateLayout, req.From)
	if err != nil {
		return err
	}
	to, err := time.Parse(absDateLayout, req.To)
	if err != nil {
		return err
	}
	spans, err := absOverlapping(ctx, tx, tenantID, req.WorkerID, from, to)
	if err != nil {
		return err
	}
	if _, err := domain.CheckOverlap(req.ID, from, to, spans); err != nil {
		return absProblem(err)
	}
	plan, err := absLoadPlan(ctx, tx, tenantID, req.PlanID)
	if err != nil {
		return err
	}
	yearStart := domain.PlanYearStart(plan.PlanYearStart, from)
	if err := absYearClosed(ctx, tx, tenantID, req.PlanID, req.WorkerID, yearStart); err != nil {
		return err
	}
	ledger, err := absLoadLedger(ctx, tx, tenantID, req.WorkerID, req.PlanID, yearStart)
	if err != nil {
		return err
	}
	qty, err := decimal.NewFromString(req.Quantity)
	if err != nil {
		return err
	}
	floor, err := absFloor(plan)
	if err != nil {
		return err
	}
	if ledger, err = ledger.Consume(qty, floor); err != nil {
		return absProblem(err)
	}
	if err := absSaveLedger(ctx, tx, tenantID, req.WorkerID, req.PlanID, yearStart, ledger); err != nil {
		return err
	}
	return enqueue(ctx, tx, "absencerequest", req.ID, hrevents.AbsenceApproved, tenantID, hrevents.AbsenceApprovedData{
		ID: req.ID, WorkerID: req.WorkerID, PlanID: req.PlanID, Unit: plan.Unit,
		DateFrom: req.From, DateTo: req.To, Quantity: qty.StringFixed(2),
	})
}

// CancelAbsenceRequest withdraws a SUBMITTED request (8.42.7 state machine). No
// balance was spent, so nothing is reversed. An ownOnly caller may cancel only
// their own request. Cancelling an already cancelled request changes nothing.
func (s *Postgres) CancelAbsenceRequest(ctx context.Context, requestID string, ownOnly bool) (AbsenceRequest, bool, error) {
	p := principal(ctx)
	var out AbsenceRequest
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var status, workerID string
		err := tx.QueryRow(ctx, `select status, worker_id::text from absence_request where tenant_id = $1 and id = $2 for update`,
			p.TenantID, requestID).Scan(&status, &workerID)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeAbsenceRequestNotFound, "absence request not found")
		}
		if err != nil {
			return err
		}
		user, err := absWorkerUser(ctx, tx, p.TenantID, workerID)
		if err != nil {
			return err
		}
		if err := absCheckOwn(ownOnly, p.UserID, user); err != nil {
			return err
		}
		moved, err := domain.CanCancel(status)
		if err != nil {
			return absProblem(err)
		}
		if moved {
			if _, err := tx.Exec(ctx, `update absence_request set status = 'CANCELED', decided_by = nullif($3, ''), decided_at = now()
				where tenant_id = $1 and id = $2`, p.TenantID, requestID, p.UserID); err != nil {
				return err
			}
		}
		out, err = absGetRequest(ctx, tx, p.TenantID, requestID)
		changed = moved && err == nil
		return err
	})
	return out, changed, err
}
