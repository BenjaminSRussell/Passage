package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

// PrinterStatuses returns every printer of the tenant with its queue depth (QUEUED jobs
// on its queue) and the outcome of the last job it handled (§6.32.14 printer status).
func (s *Postgres) PrinterStatuses(ctx context.Context) ([]domain.PrinterStatus, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.PrinterStatus{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select d.id::text, d.code, d.queue_code,
				(select count(*) from print_job q
					where q.tenant_id = d.tenant_id and q.queue_code = d.queue_code and q.status = 'QUEUED')::int,
				coalesce(last.status, ''), coalesce(last.last_error, ''), last.updated_at
			from print_device d
			left join lateral (
				select j.status, j.last_error, j.updated_at from print_job j
				where j.tenant_id = d.tenant_id and j.printer_code = d.code
				order by j.created_at desc, j.id desc
				limit 1
			) last on true
			where d.tenant_id = $1
			order by d.code`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var st domain.PrinterStatus
			var lastStatus string
			if err := rows.Scan(&st.PrinterID, &st.Code, &st.QueueCode, &st.QueueDepth, &lastStatus, &st.LastError, &st.LastJobAt); err != nil {
				return err
			}
			st.State = domain.PrinterState(lastStatus)
			out = append(out, st)
		}
		return rows.Err()
	})
	return out, err
}

// PurgeJobs deletes the tenant's finished job log rows created before cutoff. QUEUED
// jobs are kept because dispatch still owns them. It returns the number of rows removed.
func (s *Postgres) PurgeJobs(ctx context.Context, cutoff time.Time) (int64, error) {
	p, _ := tenant.FromContext(ctx)
	var deleted int64
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from print_job where tenant_id = $1 and created_at < $2 and status <> 'QUEUED'`, p.TenantID, cutoff)
		if err != nil {
			return err
		}
		deleted = tag.RowsAffected()
		return nil
	})
	return deleted, err
}
