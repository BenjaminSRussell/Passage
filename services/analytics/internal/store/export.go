package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
)

const exportColumns = `export_id::text, requested_by, query_hash, plan, format, mode, row_count, link_ttl_hours, status,
	created_at, delete_at`

func scanExport(row pgx.Row) (domain.Export, error) {
	var (
		e    domain.Export
		plan []byte
	)
	if err := row.Scan(&e.ID, &e.RequestedBy, &e.QueryHash, &plan, &e.Format, &e.Mode, &e.RowCount, &e.LinkTTLHours,
		&e.Status, &e.CreatedAt, &e.DeleteAt); err != nil {
		return e, err
	}
	e.CreatedAt, e.DeleteAt = e.CreatedAt.UTC(), e.DeleteAt.UTC()
	e.LinkExpiresAt = domain.LinkExpiresAt(e.CreatedAt, e.LinkTTLHours)
	return e, json.Unmarshal(plan, &e.Plan)
}

// CreateExport stores an export request under an Idempotency-Key (POST
// /analytics/exports). in carries the compiled plan, format, mode, row count,
// link lifetime and query hash; the requester is the caller in ctx. An
// identical request (same query hash, not DELETED) created within
// domain.ExportReuseWindow is returned with Reused set and nothing new is
// written (BR-46.7, answer 200); otherwise a QUEUED job is inserted (202).
// The export runner that writes the file is not built yet.
func (s *Postgres) CreateExport(ctx context.Context, key, requestHash string, in domain.Export, now time.Time) (Created, error) {
	p, _ := tenant.FromContext(ctx)
	now = now.UTC().Truncate(time.Microsecond)
	plan, err := json.Marshal(in.Plan)
	if err != nil {
		return Created{}, err
	}
	var out Created
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay, err := claimKey(ctx, tx, p.TenantID, key, requestHash)
		if err != nil || replay != nil {
			if replay != nil {
				out = *replay
			}
			return err
		}
		prev, err := scanExport(tx.QueryRow(ctx, `select `+exportColumns+` from export_job
			where `+ownTenant+` and query_hash = $1 and status <> 'DELETED'
			order by created_at desc, export_id limit 1`, in.QueryHash))
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil && domain.CanReuseExport(prev.CreatedAt, now) {
			prev.Reused = true
			out, err = storeResponse(ctx, tx, key, 200, prev)
			return err
		}
		job, err := scanExport(tx.QueryRow(ctx, `insert into export_job (tenant_id, export_id, requested_by, query_hash, plan,
			format, mode, row_count, link_ttl_hours, created_at, delete_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) returning `+exportColumns,
			p.TenantID, ids.New(), p.UserID, in.QueryHash, plan, in.Format, in.Mode, in.RowCount, in.LinkTTLHours,
			now, domain.ExportDeleteAt(now)))
		if err != nil {
			return err
		}
		out, err = storeResponse(ctx, tx, key, 202, job)
		return err
	})
	return out, err
}

// ExportFilter narrows the export log (§9.46.14: user, date, format). From and
// To are inclusive UTC days as YYYY-MM-DD; an empty field matches everything.
type ExportFilter struct {
	RequestedBy string
	Format      string
	From        string
	To          string
}

// ListExports returns the tenant's export log matching f, newest first.
func (s *Postgres) ListExports(ctx context.Context, f ExportFilter) ([]domain.Export, error) {
	items := []domain.Export{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+exportColumns+` from export_job
			where `+ownTenant+` and ($1::text = '' or requested_by = $1) and ($2::text = '' or format = $2)
				and ($3::text = '' or created_at >= nullif($3, '')::date::timestamp at time zone 'UTC')
				and ($4::text = '' or created_at < (nullif($4, '')::date + 1)::timestamp at time zone 'UTC')
			order by created_at desc, export_id limit 500`, f.RequestedBy, f.Format, f.From, f.To)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := scanExport(rows)
			if err != nil {
				return err
			}
			items = append(items, e)
		}
		return rows.Err()
	})
	return items, err
}
