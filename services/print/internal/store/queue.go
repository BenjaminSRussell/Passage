package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

// GetPrinter returns a printer of the tenant, retired or not.
func (s *Postgres) GetPrinter(ctx context.Context, id string) (domain.Printer, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Printer
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanPrinter(tx.QueryRow(ctx, `select `+printerColumns+` from print_device where tenant_id = $1 and id = $2`, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Printer{}, httpx.NewProblem(http.StatusNotFound, domain.CodePrinterNotFound, "printer not found")
	}
	return out, err
}

// UpdatePrinter rewrites the editable fields of an active printer: name, dpi, media, host, port,
// queue and failover rank. Its code never changes. A queue and rank that are taken give PRN-0006.
func (s *Postgres) UpdatePrinter(ctx context.Context, in domain.Printer) (domain.Printer, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Printer
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanPrinter(tx.QueryRow(ctx, `update print_device set name = $3, dpi = $4, media_size = $5, host = $6, port = $7, queue_code = $8, failover_rank = $9
			where tenant_id = $1 and id = $2 and retired_at is null
			returning `+printerColumns, p.TenantID, in.ID, in.Name, in.Dpi, in.MediaSize, in.Host, in.Port, in.QueueCode, in.FailoverRank))
		return err
	})
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.Printer{}, httpx.NewProblem(http.StatusNotFound, domain.CodePrinterNotFound, "printer not found or already retired")
	case errors.As(err, &pgErr) && pgErr.Code == uniqueViolation:
		return domain.Printer{}, httpx.NewProblem(http.StatusConflict, domain.CodeRankTaken, "queue "+in.QueueCode+" already has a printer at this failover rank")
	}
	return out, err
}

// RetirePrinter takes a printer out of its queue. Its row stays so the job log still names it.
func (s *Postgres) RetirePrinter(ctx context.Context, id string) (domain.Printer, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Printer
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanPrinter(tx.QueryRow(ctx, `update print_device set retired_at = now() where tenant_id = $1 and id = $2 and retired_at is null returning `+printerColumns, p.TenantID, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Printer{}, httpx.NewProblem(http.StatusNotFound, domain.CodePrinterNotFound, "printer not found or already retired")
	}
	return out, err
}

// ClaimStaleJobs returns QUEUED jobs untouched since olderThan, oldest first, with their payload,
// and stamps them so a second sweep skips them until olderThan passes again. A job whose
// process died between insert and completion is found this way (§6.32.16).
func (s *Postgres) ClaimStaleJobs(ctx context.Context, olderThan time.Time, limit int) ([]domain.Job, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.Job{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `with picked as materialized (
				select id as job_id from print_job where tenant_id = $1 and status = 'QUEUED' and updated_at < $2
				order by created_at, id limit $3 for update skip locked)
			update print_job set updated_at = now()
			from picked where tenant_id = $1 and id = picked.job_id
			returning `+jobColumns+`, `+payloadColumns, p.TenantID, olderThan, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanJobWithPayload(rows)
			if err != nil {
				return err
			}
			out = append(out, r.job)
		}
		return rows.Err()
	})
	return out, err
}

// UpdateRoutingRule replaces a rule's document type, scope, queue and sequence.
func (s *Postgres) UpdateRoutingRule(ctx context.Context, in domain.RoutingRule) (domain.RoutingRule, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.RoutingRule
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanRule(tx.QueryRow(ctx, `update print_routing_rule set document_type = $3, user_id = nullif($4, ''), zone = nullif($5, ''),
			warehouse_id = nullif($6, ''), queue_code = $7, sequence = $8
			where tenant_id = $1 and id = $2 returning `+routingColumns, p.TenantID, in.ID, in.DocumentType, in.UserID, in.Zone, in.WarehouseID, in.QueueCode, in.Sequence))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RoutingRule{}, httpx.NewProblem(http.StatusNotFound, domain.CodeRuleNotFound, "routing rule not found")
	}
	return out, err
}

// DeleteRoutingRule removes a rule. Documents it matched fall to the next rule or, with none, MOB-0112.
func (s *Postgres) DeleteRoutingRule(ctx context.Context, id string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from print_routing_rule where tenant_id = $1 and id = $2`, p.TenantID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NewProblem(http.StatusNotFound, domain.CodeRuleNotFound, "routing rule not found")
		}
		return nil
	})
}
