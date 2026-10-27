// Package store persists printers, queues and print jobs in Postgres under
// row-level security. Completing a job writes its event to the outbox in the
// same transaction.
package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
	printevents "github.com/BenjaminSRussell/passage/services/print/internal/events"
)

const (
	CodeJobNotFound      = "PRN-0005"
	CodeIdempotencyReuse = "idempotency_key_reuse"
	uniqueViolation      = "23505"
	eventSource          = "print"
)

// JobFilter narrows the print job log (§6.32.14). Empty fields match everything.
type JobFilter struct {
	Status      string
	QueueCode   string
	PrinterCode string
	Limit       int
}

// NewJob is a job to insert with its request fingerprint and Idempotency-Key.
type NewJob struct {
	Job            domain.Job
	RequestHash    string
	IdempotencyKey string
}

// Store is the persistence port used by the API.
type Store interface {
	Ping(ctx context.Context) error
	CreatePrinter(ctx context.Context, p domain.Printer, idempotencyKey string) (domain.Printer, bool, error)
	ListPrinters(ctx context.Context) ([]domain.Printer, error)
	QueueDevices(ctx context.Context, queueCode string) ([]domain.Printer, error)
	CreateRoutingRule(ctx context.Context, r domain.RoutingRule, idempotencyKey string) (domain.RoutingRule, bool, error)
	ListRoutingRules(ctx context.Context) ([]domain.RoutingRule, error)
	CreateJob(ctx context.Context, in NewJob) (domain.Job, bool, error)
	ListJobs(ctx context.Context, f JobFilter) ([]domain.Job, error)
	CompleteJob(ctx context.Context, j domain.Job) error
	GetJob(ctx context.Context, id string) (domain.Job, error)
}

// Postgres implements Store.
type Postgres struct{ pool *pgxpool.Pool }

// New returns a Postgres store over pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database connection for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

const printerColumns = "id::text, code, name, device_type, dpi, host, port, queue_code, failover_rank, created_at"

func scanPrinter(row pgx.Row) (domain.Printer, error) {
	var p domain.Printer
	err := row.Scan(&p.ID, &p.Code, &p.Name, &p.DeviceType, &p.Dpi, &p.Host, &p.Port, &p.QueueCode, &p.FailoverRank, &p.CreatedAt)
	return p, err
}

// CreatePrinter registers a printer. A repeated Idempotency-Key returns the
// stored printer (inserted=false); a different body under that key is refused.
func (s *Postgres) CreatePrinter(ctx context.Context, in domain.Printer, idempotencyKey string) (domain.Printer, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Printer
	var inserted bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanPrinter(tx.QueryRow(ctx, `insert into print_device (tenant_id, id, code, name, device_type, dpi, host, port, queue_code, failover_rank, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			on conflict (tenant_id, idempotency_key) do nothing
			returning `+printerColumns, p.TenantID, ids.New(), in.Code, in.Name, in.DeviceType, in.Dpi, in.Host, in.Port, in.QueueCode, in.FailoverRank, idempotencyKey))
		if errors.Is(err, pgx.ErrNoRows) {
			out, err = scanPrinter(tx.QueryRow(ctx, `select `+printerColumns+` from print_device where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idempotencyKey))
			if err == nil {
				inserted = false
			}
			return err
		}
		if err != nil {
			return err
		}
		inserted = true
		return nil
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		if pgErr.ConstraintName == rankConstraint {
			return domain.Printer{}, false, httpx.NewProblem(http.StatusConflict, domain.CodeRankTaken, "queue "+in.QueueCode+" already has a printer at this failover rank")
		}
		return domain.Printer{}, false, httpx.NewProblem(http.StatusConflict, domain.CodeDuplicatePrinter, "a printer with this code already exists")
	}
	if err != nil {
		return domain.Printer{}, false, err
	}
	if !inserted && !samePrinter(out, in) {
		return domain.Printer{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return out, inserted, nil
}

// rankConstraint is the generated name of UNIQUE (tenant_id, queue_code, failover_rank).
const rankConstraint = "print_device_tenant_id_queue_code_failover_rank_key"

func samePrinter(a, b domain.Printer) bool {
	return a.Code == b.Code && a.Name == b.Name && a.DeviceType == b.DeviceType && a.Dpi == b.Dpi &&
		a.Host == b.Host && a.Port == b.Port && a.QueueCode == b.QueueCode && a.FailoverRank == b.FailoverRank
}

// ListPrinters returns every printer of the tenant, ordered by code.
func (s *Postgres) ListPrinters(ctx context.Context) ([]domain.Printer, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.Printer{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+printerColumns+` from print_device where tenant_id = $1 order by code`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanPrinter(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// QueueDevices returns the devices of a queue in failover-rank order.
func (s *Postgres) QueueDevices(ctx context.Context, queueCode string) ([]domain.Printer, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.Printer{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+printerColumns+` from print_device where tenant_id = $1 and queue_code = $2 order by failover_rank`, p.TenantID, queueCode)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanPrinter(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

const jobColumns = "id::text, template_code, queue_code, coalesce(printer_id::text, ''), coalesce(printer_code, ''), status, attempts, coalesce(last_error, ''), reprint_of::text, created_by, created_at, updated_at, zpl, request_hash"

type jobRow struct {
	job  domain.Job
	hash string
}

func scanJob(row pgx.Row) (jobRow, error) {
	var r jobRow
	err := row.Scan(&r.job.ID, &r.job.TemplateCode, &r.job.QueueCode, &r.job.PrinterID, &r.job.PrinterCode, &r.job.Status, &r.job.Attempts,
		&r.job.LastError, &r.job.ReprintOf, &r.job.CreatedBy, &r.job.CreatedAt, &r.job.UpdatedAt, &r.job.ZPL, &r.hash)
	return r, err
}

// CreateJob inserts a QUEUED job. A repeated Idempotency-Key returns the stored
// job with inserted=false; a different request fingerprint is refused.
func (s *Postgres) CreateJob(ctx context.Context, in NewJob) (domain.Job, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out jobRow
	var inserted bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanJob(tx.QueryRow(ctx, `insert into print_job (tenant_id, id, template_code, queue_code, zpl, status, request_hash, idempotency_key, created_by, reprint_of)
			values ($1, $2, $3, $4, $5, 'QUEUED', $6, $7, $8, $9::uuid)
			on conflict (tenant_id, idempotency_key) do nothing
			returning `+jobColumns, p.TenantID, in.Job.ID, in.Job.TemplateCode, in.Job.QueueCode, in.Job.ZPL, in.RequestHash, in.IdempotencyKey, in.Job.CreatedBy, in.Job.ReprintOf))
		if err == nil {
			inserted = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		out, err = scanJob(tx.QueryRow(ctx, `select `+jobColumns+` from print_job where tenant_id = $1 and idempotency_key = $2`, p.TenantID, in.IdempotencyKey))
		return err
	})
	if err != nil {
		return domain.Job{}, false, err
	}
	if !inserted && out.hash != in.RequestHash {
		return domain.Job{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return out.job, inserted, nil
}

// CompleteJob stores the terminal state of a QUEUED job and enqueues
// dev.passage.print.job.completed.v1 in the same transaction.
func (s *Postgres) CompleteJob(ctx context.Context, j domain.Job) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update print_job set status = $3, attempts = $4, printer_id = nullif($5, '')::uuid, printer_code = nullif($6, ''),
			last_error = nullif($7, ''), updated_at = now()
			where tenant_id = $1 and id = $2 and status = 'QUEUED'`, p.TenantID, j.ID, j.Status, j.Attempts, j.PrinterID, j.PrinterCode, j.LastError)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrTerminal
		}
		evt, err := events.New(printevents.JobCompleted, eventSource, j.ID, p.TenantID, printevents.JobCompletedData{
			JobID: j.ID, Printer: j.PrinterCode, Status: j.Status,
		})
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, "job", j.ID, evt)
	})
}

// GetJob returns a job of the tenant, including its stored ZPL for reprints.
func (s *Postgres) GetJob(ctx context.Context, id string) (domain.Job, error) {
	p, _ := tenant.FromContext(ctx)
	var out jobRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanJob(tx.QueryRow(ctx, `select `+jobColumns+` from print_job where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Job{}, httpx.NewProblem(http.StatusNotFound, CodeJobNotFound, "print job not found")
	}
	return out.job, err
}

// ListJobs returns the tenant's newest jobs first (the print job log, §6.32.14).
func (s *Postgres) ListJobs(ctx context.Context, f JobFilter) ([]domain.Job, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.Job{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+jobColumns+` from print_job
			where tenant_id = $1
			  and ($2 = '' or status = $2)
			  and ($3 = '' or queue_code = $3)
			  and ($4 = '' or printer_code = $4)
			order by created_at desc, id
			limit $5`, p.TenantID, f.Status, f.QueueCode, f.PrinterCode, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanJob(rows)
			if err != nil {
				return err
			}
			out = append(out, r.job)
		}
		return rows.Err()
	})
	return out, err
}
