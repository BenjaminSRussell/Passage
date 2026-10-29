package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
	analyticsevents "github.com/BenjaminSRussell/passage/services/analytics/internal/events"
)

const archiveColumns = `partition_id::text, domain, table_name, period, rows_oltp, rows_archive, content_hash_oltp,
	content_hash_archive, status, legal_hold, open_period, moved_at, restore_until`

func scanPartition(row pgx.Row) (domain.ArchivePartition, error) {
	var p domain.ArchivePartition
	err := row.Scan(&p.ID, &p.Domain, &p.Table, &p.Period, &p.RowsOLTP, &p.RowsArchive, &p.HashOLTP, &p.HashArchive,
		&p.Status, &p.LegalHold, &p.OpenPeriod, &p.MovedAt, &p.RestoreUntil)
	p.MovedAt, p.RestoreUntil = utc(p.MovedAt), utc(p.RestoreUntil)
	return p, err
}

// RegisterArchivePartition records a PENDING partition the archive mover will
// copy. Registering the same domain, table and period again returns the
// existing row unchanged, so a rerun of the mover is safe.
func (s *Postgres) RegisterArchivePartition(ctx context.Context, domainName, table, period string, legalHold, openPeriod bool) (domain.ArchivePartition, error) {
	in, verr := domain.NewArchivePartition(domainName, table, period)
	if verr != nil {
		return domain.ArchivePartition{}, verr
	}
	p, _ := tenant.FromContext(ctx)
	var out domain.ArchivePartition
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into archive_partition (tenant_id, partition_id, domain, table_name, period, status,
			legal_hold, open_period, legal_hold_checked_at) values ($1, $2, $3, $4, $5, $6, $7, $8, now())
			on conflict (tenant_id, domain, table_name, period) do nothing`,
			p.TenantID, ids.New(), in.Domain, in.Table, in.Period, domain.ArchivePending, legalHold, openPeriod); err != nil {
			return err
		}
		var err error
		out, err = scanPartition(tx.QueryRow(ctx, `select `+archiveColumns+` from archive_partition
			where `+ownTenant+` and domain = $1 and table_name = $2 and period = $3`, in.Domain, in.Table, in.Period))
		return err
	})
	return out, err
}

// lockPartition reads one partition for update or returns ANA-0112.
func lockPartition(ctx context.Context, tx pgx.Tx, id string) (domain.ArchivePartition, error) {
	if !ids.Valid(id) {
		return domain.ArchivePartition{}, domain.NotFound("archive partition " + id)
	}
	p, err := scanPartition(tx.QueryRow(ctx, `select `+archiveColumns+` from archive_partition
		where `+ownTenant+` and partition_id = $1 for update`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, domain.NotFound("archive partition " + id)
	}
	return p, err
}

func savePartition(ctx context.Context, tx pgx.Tx, p domain.ArchivePartition, restoredBy string) (domain.ArchivePartition, error) {
	return scanPartition(tx.QueryRow(ctx, `update archive_partition set rows_oltp = $2, rows_archive = $3,
		content_hash_oltp = $4, content_hash_archive = $5, status = $6, moved_at = $7, restore_until = $8,
		restored_by = case when $9::text = '' then restored_by else $9 end
		where `+ownTenant+` and partition_id = $1 returning `+archiveColumns,
		p.ID, p.RowsOLTP, p.RowsArchive, p.HashOLTP, p.HashArchive, p.Status, p.MovedAt, p.RestoreUntil, restoredBy))
}

// RecordArchiveVerification stores the mover's row counts and content hashes
// (BR-46.14). Matching copies make the partition VERIFIED and enqueue
// archive.partition.moved in the same transaction. A mismatch is stored too,
// the partition stays PENDING with both copies, and the ANA-0110 error is
// returned for the Sev-3 incident.
func (s *Postgres) RecordArchiveVerification(ctx context.Context, id string, rowsOLTP, rowsArchive int64, hashOLTP, hashArchive string, now time.Time) (domain.ArchivePartition, error) {
	pr, _ := tenant.FromContext(ctx)
	var (
		out      domain.ArchivePartition
		mismatch *domain.Error
	)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := lockPartition(ctx, tx, id)
		if err != nil {
			return err
		}
		if verr := p.Verify(rowsOLTP, rowsArchive, hashOLTP, hashArchive, now); verr != nil {
			if verr.Code != domain.CodeArchiveVerify {
				return verr
			}
			mismatch = verr
		}
		if out, err = savePartition(ctx, tx, p, ""); err != nil || mismatch != nil {
			return err
		}
		evt, err := events.New(analyticsevents.ArchivePartitionMoved, eventSource, out.ID, pr.TenantID, analyticsevents.ArchivePartitionMovedData{
			PartitionID: out.ID, Domain: out.Domain, Table: out.Table, Period: out.Period, Rows: out.RowsArchive,
			ContentHash: out.HashArchive, MovedAt: out.MovedAt.UTC().Format(time.RFC3339),
		})
		if err != nil {
			return err
		}
		return outbox.Enqueue(ctx, tx, "archive_partition", out.ID, evt)
	})
	if err != nil {
		return domain.ArchivePartition{}, err
	}
	if mismatch != nil {
		return out, mismatch
	}
	return out, nil
}

// DropArchivePartition marks a VERIFIED partition DROPPED after the mover
// detached it. A legal hold placed since the move blocks the drop.
func (s *Postgres) DropArchivePartition(ctx context.Context, id string) (domain.ArchivePartition, error) {
	var out domain.ArchivePartition
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := lockPartition(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := p.Drop(); err != nil {
			return domain.StateConflict(err.Error())
		}
		out, err = savePartition(ctx, tx, p, "")
		return err
	})
	return out, err
}

// RestoreArchivePartition restores a DROPPED partition into a side table for
// ttlDays (POST /archive/partitions/{id}/restore, idempotent by partition id).
// A partition whose restore is still live is returned unchanged with
// changed=false.
func (s *Postgres) RestoreArchivePartition(ctx context.Context, id string, ttlDays int, now time.Time) (domain.ArchivePartition, bool, error) {
	pr, _ := tenant.FromContext(ctx)
	var (
		out     domain.ArchivePartition
		changed bool
	)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		p, err := lockPartition(ctx, tx, id)
		if err != nil {
			return err
		}
		if p.Status == domain.ArchiveRestored && !p.RestoreExpired(now) {
			out = p
			return nil
		}
		switch err := p.Restore(now, ttlDays); {
		case errors.Is(err, domain.ErrRestoreTTL):
			return &domain.Error{Code: domain.CodeValidation, Status: 400, Detail: err.Error(), Fields: map[string]string{"ttlDays": "1 to 90 days"}}
		case err != nil:
			return domain.StateConflict("partition is " + p.Status + "; only a DROPPED partition can be restored")
		}
		changed = true
		out, err = savePartition(ctx, tx, p, pr.UserID)
		return err
	})
	return out, changed, err
}

// GetArchivePartition returns one partition or ANA-0112.
func (s *Postgres) GetArchivePartition(ctx context.Context, id string) (domain.ArchivePartition, error) {
	var out domain.ArchivePartition
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if !ids.Valid(id) {
			return domain.NotFound("archive partition " + id)
		}
		p, err := scanPartition(tx.QueryRow(ctx, `select `+archiveColumns+` from archive_partition
			where `+ownTenant+` and partition_id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("archive partition " + id)
		}
		out = p
		return err
	})
	return out, err
}

// ListArchivePartitions returns partitions filtered by domain and status,
// newest period first.
func (s *Postgres) ListArchivePartitions(ctx context.Context, domainName, status string) ([]domain.ArchivePartition, error) {
	items := []domain.ArchivePartition{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+archiveColumns+` from archive_partition
			where `+ownTenant+` and ($1::text = '' or domain = $1) and ($2::text = '' or status = $2)
			order by period desc, domain, table_name limit 500`, domainName, status)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanPartition(rows)
			if err != nil {
				return err
			}
			items = append(items, p)
		}
		return rows.Err()
	})
	return items, err
}
