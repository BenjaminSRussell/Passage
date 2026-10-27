package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	ProviderDegraded = "dev.passage.fx.provider.degraded.v1"
	RunRunning       = "running"
	RunSucceeded     = "succeeded"
	RunFailed        = "failed"
	RunDegraded      = "degraded"

	FallbackLastKnown = "last_known"
	FallbackNone      = "none"
)

var ErrRunNotFound = errors.New("import run not found")

type Run struct {
	ID             string     `json:"id"`
	Provider       string     `json:"provider"`
	RateType       string     `json:"rateType"`
	RunDate        time.Time  `json:"runDate"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	PairsImported  int        `json:"pairsImported"`
	PairsUnchanged int        `json:"pairsUnchanged"`
	Error          string     `json:"error,omitempty"`
	StartedAt      time.Time  `json:"startedAt"`
	FinishedAt     *time.Time `json:"finishedAt,omitempty"`
	LastSuccessAt  *time.Time `json:"lastSuccessAt,omitempty"`
}

const runColumns = `r.id, r.provider, t.code, r.run_date, r.status, r.attempts, r.pairs_imported, r.pairs_unchanged,
	coalesce(r.error, ''), r.started_at, r.finished_at, r.last_success_at
	from provider_import_run r join exchange_rate_type t on t.tenant_id = r.tenant_id and t.id = r.rate_type_id`

func scanRun(row pgx.Row) (Run, error) {
	var r Run
	err := row.Scan(&r.ID, &r.Provider, &r.RateType, &r.RunDate, &r.Status, &r.Attempts, &r.PairsImported, &r.PairsUnchanged,
		&r.Error, &r.StartedAt, &r.FinishedAt, &r.LastSuccessAt)
	return r, err
}

func rateTypeID(ctx context.Context, tx pgx.Tx, tenantID, code string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `select id from exchange_rate_type where tenant_id=$1 and code=$2`, tenantID, code).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", invalid("unknown rate type %q", code)
	}
	return id, err
}

func (s *Postgres) StartRun(ctx context.Context, provider, rateType string, runDate time.Time) (Run, bool, error) {
	if provider == "" || rateType == "" {
		return Run{}, false, invalid("provider and rate type are required")
	}
	p, _ := tenant.FromContext(ctx)
	d := runDate.UTC()
	day := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
	var run Run
	var done bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		typeID, err := rateTypeID(ctx, tx, p.TenantID, rateType)
		if err != nil {
			return err
		}
		var id string
		err = tx.QueryRow(ctx, `insert into provider_import_run (tenant_id, id, provider, rate_type_id, run_date, status, started_at)
			values ($1,$2,$3,$4,$5,'running',$6)
			on conflict (tenant_id, provider, rate_type_id, run_date) do update
			set status='running', attempts=0, error=null, started_at=excluded.started_at, finished_at=null
			where provider_import_run.status <> 'succeeded'
			returning id`, p.TenantID, ids.New(), provider, typeID, day, s.now()).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			done = true
			err = tx.QueryRow(ctx, `select id from provider_import_run where tenant_id=$1 and provider=$2 and rate_type_id=$3 and run_date=$4`,
				p.TenantID, provider, typeID, day).Scan(&id)
		}
		if err != nil {
			return err
		}
		run, err = scanRun(tx.QueryRow(ctx, `select `+runColumns+` where r.tenant_id=$1 and r.id=$2`, p.TenantID, id))
		return err
	})
	return run, done, err
}

func exactlyOne(tag interface{ RowsAffected() int64 }, err error) error {
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrRunNotFound
	}
	return nil
}

func (s *Postgres) RecordAttempt(ctx context.Context, runID string, attempts int) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return exactlyOne(tx.Exec(ctx, `update provider_import_run set attempts=$3 where tenant_id=$1 and id=$2`, p.TenantID, runID, attempts))
	})
}

func (s *Postgres) FinishRunSucceeded(ctx context.Context, runID string, imported, unchanged int) error {
	p, _ := tenant.FromContext(ctx)
	now := s.now()
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return exactlyOne(tx.Exec(ctx, `update provider_import_run
			set status='succeeded', pairs_imported=$3, pairs_unchanged=$4, error=null, finished_at=$5, last_success_at=$5
			where tenant_id=$1 and id=$2`, p.TenantID, runID, imported, unchanged, now))
	})
}

func (s *Postgres) FinishRunFailed(ctx context.Context, runID string, attempts int, cause error, retryable bool) error {
	return s.finishUnhealthy(ctx, runID, attempts, cause, RunFailed, FallbackNone, retryable)
}

func (s *Postgres) FinishRunDegraded(ctx context.Context, runID string, attempts int, cause error, retryable bool) error {
	return s.finishUnhealthy(ctx, runID, attempts, cause, RunDegraded, FallbackLastKnown, retryable)
}

func (s *Postgres) FinishRunAborted(ctx context.Context, runID string, attempts int, cause error) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return exactlyOne(tx.Exec(ctx, `update provider_import_run set status='failed', attempts=$3, error=$4, finished_at=$5
			where tenant_id=$1 and id=$2`, p.TenantID, runID, attempts, errorMessage(cause), s.now()))
	})
}

func errorMessage(cause error) string {
	if cause == nil {
		return "unknown error"
	}
	return cause.Error()
}

func (s *Postgres) finishUnhealthy(ctx context.Context, runID string, attempts int, cause error, status, fallback string, retryable bool) error {
	p, _ := tenant.FromContext(ctx)
	message := errorMessage(cause)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var provider, typeID string
		err := tx.QueryRow(ctx, `update provider_import_run set status=$6, attempts=$3, error=$4, finished_at=$5
			where tenant_id=$1 and id=$2 returning provider, rate_type_id`, p.TenantID, runID, attempts, message, s.now(), status).Scan(&provider, &typeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRunNotFound
		}
		if err != nil {
			return err
		}
		var code string
		var lastSuccess *time.Time
		if err := tx.QueryRow(ctx, `select t.code, (select max(last_success_at) from provider_import_run
				where tenant_id=$1 and provider=$2 and rate_type_id=$3)
			from exchange_rate_type t where t.tenant_id=$1 and t.id=$3`, p.TenantID, provider, typeID).Scan(&code, &lastSuccess); err != nil {
			return err
		}
		evt, err := events.New(ProviderDegraded, "passage/fx", runID, p.TenantID, map[string]any{
			"provider": provider, "last_success_at": lastSuccess, "rate_type": code, "error": message,
			"status": status, "fallback": fallback, "retryable": retryable,
		})
		if err != nil {
			return fmt.Errorf("build %s: %w", ProviderDegraded, err)
		}
		return outbox.Enqueue(ctx, tx, "provider_import_run", runID, evt)
	})
}

func (s *Postgres) ListRuns(ctx context.Context, provider string) ([]Run, error) {
	p, _ := tenant.FromContext(ctx)
	out := []Run{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+runColumns+` where r.tenant_id=$1 and ($2::text = '' or r.provider=$2::text)
			order by r.started_at desc, r.id desc`, p.TenantID, provider)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			run, err := scanRun(rows)
			if err != nil {
				return err
			}
			out = append(out, run)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) LastSuccess(ctx context.Context, provider, rateType string) (*time.Time, error) {
	p, _ := tenant.FromContext(ctx)
	var at *time.Time
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select max(r.last_success_at) from provider_import_run r
			join exchange_rate_type t on t.tenant_id = r.tenant_id and t.id = r.rate_type_id
			where r.tenant_id=$1 and r.provider=$2 and t.code=$3`, p.TenantID, provider, rateType).Scan(&at)
	})
	return at, err
}
