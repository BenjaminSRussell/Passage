package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
)

const (
	CodeJobNotFound       = "HCM-JOB-NOT-FOUND"
	CodeJobDuplicate      = "HCM-JOB-DUPLICATE"
	CodeCompPlanNotFound  = "HCM-CMP-PLAN-NOT-FOUND"
	CodeCompPlanDuplicate = "HCM-CMP-PLAN-DUPLICATE"
)

type Job struct {
	ID          string  `json:"id"`
	Code        string  `json:"code"`
	Title       string  `json:"title"`
	JobType     string  `json:"jobType"`
	JobFunction *string `json:"jobFunction,omitempty"`
	PayCurrency *string `json:"payCurrency,omitempty"`
	PayMin      *string `json:"payMin,omitempty"`
	PayMax      *string `json:"payMax,omitempty"`
}

const jobSelect = `select id::text, code, title, job_type, job_function, pay_currency, pay_min::text, pay_max::text from hr_job`

func scanJob(row pgx.Row) (Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.Code, &j.Title, &j.JobType, &j.JobFunction, &j.PayCurrency, &j.PayMin, &j.PayMax)
	return j, err
}

func sameJob(a, b Job) bool {
	return a.Code == b.Code && a.Title == b.Title && a.JobType == b.JobType && sameStringPtr(a.JobFunction, b.JobFunction) &&
		sameStringPtr(a.PayCurrency, b.PayCurrency) && sameDecimalPtr(a.PayMin, b.PayMin) && sameDecimalPtr(a.PayMax, b.PayMax)
}

func (s *Postgres) CreateJob(ctx context.Context, in Job, idempotencyKey string) (Job, bool, error) {
	p := principal(ctx)
	key := idemKey(idempotencyKey)
	var out Job
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanJob(tx.QueryRow(ctx, jobSelect+` where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var err error
		out, err = scanJob(tx.QueryRow(ctx, `insert into hr_job (tenant_id, id, code, title, job_type, job_function, pay_currency, pay_min, pay_max, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9::numeric, $10)
			returning id::text, code, title, job_type, job_function, pay_currency, pay_min::text, pay_max::text`,
			p.TenantID, ids.New(), in.Code, in.Title, in.JobType, in.JobFunction, in.PayCurrency, in.PayMin, in.PayMax, key))
		inserted = err == nil
		return err
	})
	if idempotencyRace(err) {
		return Job{}, false, idempotencyInFlight()
	}
	if pgCode(err, uniqueViolation) {
		return Job{}, false, problem(http.StatusConflict, CodeJobDuplicate, "a job with this code already exists")
	}
	if err == nil && key != nil && !inserted && !sameJob(out, in) {
		return Job{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

func (s *Postgres) GetJob(ctx context.Context, id string) (Job, error) {
	p := principal(ctx)
	var out Job
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		out, err = scanJob(tx.QueryRow(ctx, jobSelect+` where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, problem(http.StatusNotFound, CodeJobNotFound, "job not found")
	}
	return out, err
}

type JobPage struct {
	Items     []Job
	NextAfter string
}

func (s *Postgres) ListJobs(ctx context.Context, after string, limit int) (JobPage, error) {
	p := principal(ctx)
	page := JobPage{Items: []Job{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, jobSelect+` where tenant_id = $1 and code > $2 order by code limit $3`, p.TenantID, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			j, err := scanJob(rows)
			if err != nil {
				return err
			}
			page.Items = append(page.Items, j)
		}
		return rows.Err()
	})
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextAfter = page.Items[limit-1].Code
	}
	return page, err
}

type CompensationPlan struct {
	ID       string `json:"id"`
	Code     string `json:"code"`
	Name     string `json:"name"`
	PayBasis string `json:"payBasis"`
	Currency string `json:"currency"`
}

const compPlanSelect = `select id::text, code, name, pay_basis, currency from hr_compensation_plan`

func scanCompPlan(row pgx.Row) (CompensationPlan, error) {
	var c CompensationPlan
	err := row.Scan(&c.ID, &c.Code, &c.Name, &c.PayBasis, &c.Currency)
	return c, err
}

func (s *Postgres) CreateCompensationPlan(ctx context.Context, in CompensationPlan, idempotencyKey string) (CompensationPlan, bool, error) {
	p := principal(ctx)
	key := idemKey(idempotencyKey)
	var out CompensationPlan
	inserted := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != nil {
			replay, err := scanCompPlan(tx.QueryRow(ctx, compPlanSelect+` where tenant_id = $1 and idempotency_key = $2`, p.TenantID, *key))
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var err error
		out, err = scanCompPlan(tx.QueryRow(ctx, `insert into hr_compensation_plan (tenant_id, id, code, name, pay_basis, currency, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7) returning id::text, code, name, pay_basis, currency`,
			p.TenantID, ids.New(), in.Code, in.Name, in.PayBasis, in.Currency, key))
		inserted = err == nil
		return err
	})
	if idempotencyRace(err) {
		return CompensationPlan{}, false, idempotencyInFlight()
	}
	if pgCode(err, uniqueViolation) {
		return CompensationPlan{}, false, problem(http.StatusConflict, CodeCompPlanDuplicate, "a compensation plan with this code already exists")
	}
	if err == nil && key != nil && !inserted && out != withoutID(in, out.ID) {
		return CompensationPlan{}, false, idempotencyReuse()
	}
	return out, inserted, err
}

func withoutID(c CompensationPlan, id string) CompensationPlan {
	c.ID = id
	return c
}

func (s *Postgres) ListCompensationPlans(ctx context.Context, after string, limit int) ([]CompensationPlan, string, error) {
	p := principal(ctx)
	out := []CompensationPlan{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, compPlanSelect+` where tenant_id = $1 and code > $2 order by code limit $3`, p.TenantID, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCompPlan(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].Code
	}
	return out, next, err
}

type FixedCompensation struct {
	ID           string     `json:"id"`
	EmploymentID string     `json:"employmentId"`
	WorkerID     string     `json:"workerId"`
	PlanID       string     `json:"planId"`
	PlanCode     string     `json:"planCode"`
	PayBasis     string     `json:"payBasis"`
	Amount       string     `json:"amount"`
	Currency     string     `json:"currency"`
	ValidFrom    time.Time  `json:"validFrom"`
	ValidTo      *time.Time `json:"validTo,omitempty"`
}

func (s *Postgres) ListWorkerCompensation(ctx context.Context, workerID string) ([]FixedCompensation, error) {
	p := principal(ctx)
	out := []FixedCompensation{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from worker where tenant_id = $1 and id = $2)`, p.TenantID, workerID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return problem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
		}
		rows, err := tx.Query(ctx, `select c.id::text, c.employment_id::text, c.worker_id::text, c.plan_id::text, pl.code, pl.pay_basis, c.amount::text, c.currency, c.valid_from, c.valid_to
			from hr_fixed_compensation c join hr_compensation_plan pl on pl.tenant_id = c.tenant_id and pl.id = c.plan_id
			where c.tenant_id = $1 and c.worker_id = $2 order by c.valid_from, c.id`, p.TenantID, workerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c FixedCompensation
			if err := rows.Scan(&c.ID, &c.EmploymentID, &c.WorkerID, &c.PlanID, &c.PlanCode, &c.PayBasis, &c.Amount, &c.Currency, &c.ValidFrom, &c.ValidTo); err != nil {
				return err
			}
			c.ValidFrom, c.ValidTo = utc(c.ValidFrom), utcPtr(c.ValidTo)
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

func idemKey(k string) *string {
	if k == "" {
		return nil
	}
	return &k
}
