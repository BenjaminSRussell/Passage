package jobs

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

type PgStore struct {
	pool *pgxpool.Pool
}

func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{pool: pool}
}

func (s *PgStore) Checkpoint(
	ctx context.Context,
	tenantID, jobID string,
	partition int,
) (int64, error) {
	ctx = tenant.With(ctx, tenant.Principal{TenantID: tenantID})
	var next int64
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			select coalesce((
				select next_row
				from batch_checkpoints
				where tenant_id = $1 and job_id = $2 and partition_no = $3
			), 0)`,
			tenantID, jobID, partition,
		).Scan(&next)
	})
	if err != nil {
		return 0, fmt.Errorf("read checkpoint: %w", err)
	}
	return next, nil
}

func (s *PgStore) Execute(
	ctx context.Context,
	tenantID string,
	jobID string,
	partition int,
	row int64,
	effect func(pgx.Tx) error,
) (bool, error) {
	ctx = tenant.With(ctx, tenant.Principal{TenantID: tenantID})
	applied := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			insert into batch_processed_rows(tenant_id, job_id, partition_no, row_number)
			values ($1, $2, $3, $4)
			on conflict do nothing`,
			tenantID, jobID, partition, row,
		)
		if err != nil {
			return fmt.Errorf("claim row: %w", err)
		}
		applied = tag.RowsAffected() == 1
		if applied {
			if err := effect(tx); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			insert into batch_checkpoints(tenant_id, job_id, partition_no, next_row)
			values ($1, $2, $3, $4)
			on conflict (tenant_id, job_id, partition_no) do update
			set next_row = greatest(batch_checkpoints.next_row, excluded.next_row),
			    updated_at = now()`,
			tenantID, jobID, partition, row+1,
		); err != nil {
			return fmt.Errorf("advance checkpoint: %w", err)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}
