package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

func PendingConflict(subject string) *domain.Error {
	return &domain.Error{Code: domain.CodePostingPending, Detail: fmt.Sprintf("an earlier ledger posting for %s is unfinished or differs; repeat the original request first", subject)}
}

func markPostingsDone(ctx context.Context, tx pgx.Tx, tenantID, subject string) error {
	_, err := tx.Exec(ctx, `UPDATE receiving_pending_posting SET done_at = now(), outcome = 'POSTED' WHERE tenant_id = $1 AND subject = $2 AND done_at IS NULL`, tenantID, subject)
	return err
}

func (s *Postgres) ReservePosting(ctx context.Context, p PendingPosting) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, tenantID+"|pending|"+p.Subject); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT ledger_key, request_hash FROM receiving_pending_posting
			WHERE tenant_id = $1 AND subject = $2 AND (done_at IS NULL OR ledger_key = $3)`, tenantID, p.Subject, p.Key)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key, hash string
			if err := rows.Scan(&key, &hash); err != nil {
				return err
			}
			if key != p.Key || hash != p.Hash {
				return PendingConflict(p.Subject)
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		_, err = tx.Exec(ctx, `INSERT INTO receiving_pending_posting (tenant_id, ledger_key, subject, request_hash, journal) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (tenant_id, ledger_key) DO UPDATE SET done_at = NULL, outcome = NULL, request_hash = EXCLUDED.request_hash, journal = EXCLUDED.journal
			WHERE receiving_pending_posting.outcome = 'REJECTED'`, tenantID, p.Key, p.Subject, p.Hash, p.Journal)
		return err
	})
}

func (s *Postgres) ReleasePosting(ctx context.Context, key string) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE receiving_pending_posting SET done_at = now(), outcome = 'REJECTED' WHERE tenant_id = $1 AND ledger_key = $2 AND done_at IS NULL`, tenantID, key)
		return err
	})
}

func (s *Postgres) EnsureNoPending(ctx context.Context, subject string) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, tenantID+"|pending|"+subject); err != nil {
			return err
		}
		var pending int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM receiving_pending_posting WHERE tenant_id = $1 AND subject = $2 AND done_at IS NULL`, tenantID, subject).Scan(&pending); err != nil {
			return err
		}
		if pending > 0 {
			return PendingConflict(subject)
		}
		return nil
	})
}
