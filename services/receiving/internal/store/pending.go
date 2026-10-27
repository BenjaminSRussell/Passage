package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

type PendingError struct {
	Subject   string
	Key       string
	CreatedAt time.Time
}

func (e *PendingError) Error() string {
	return fmt.Sprintf("an earlier ledger posting %s for %s (started %s) is unfinished or differs", e.Key, e.Subject, e.CreatedAt.UTC().Format(time.RFC3339))
}

func (s *Postgres) WithPendingPool(pool *pgxpool.Pool) *Postgres {
	s.pending = pool
	return s
}

func (s *Postgres) pendingPool() *pgxpool.Pool {
	if s.pending != nil {
		return s.pending
	}
	return s.pool
}

func markPostingsDone(ctx context.Context, tx pgx.Tx, tenantID, subject string) error {
	_, err := tx.Exec(ctx, `WITH done AS (
			UPDATE receiving_pending_posting SET done_at = now(), outcome = 'POSTED'
			WHERE tenant_id = $1 AND done_at IS NULL AND ledger_key IN (
				SELECT ledger_key FROM receiving_pending_posting_subject WHERE tenant_id = $1 AND subject = $2 AND done_at IS NULL)
			RETURNING ledger_key)
		UPDATE receiving_pending_posting_subject SET done_at = now() WHERE tenant_id = $1 AND ledger_key IN (SELECT ledger_key FROM done)`, tenantID, subject)
	return err
}

func lockSubjects(ctx context.Context, tx pgx.Tx, tenantID string, subjects []string) error {
	sorted := slices.Sorted(slices.Values(subjects))
	for _, subject := range slices.Compact(sorted) {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, tenantID+"|pending|"+subject); err != nil {
			return err
		}
	}
	return nil
}

func (s *Postgres) ReservePosting(ctx context.Context, p PendingPosting) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pendingPool(), func(tx pgx.Tx) error {
		if err := lockSubjects(ctx, tx, tenantID, p.Subjects); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT DISTINCT p.ledger_key, p.request_hash, p.created_at
			FROM receiving_pending_posting_subject c
			JOIN receiving_pending_posting p ON p.tenant_id = c.tenant_id AND p.ledger_key = c.ledger_key
			WHERE c.tenant_id = $1 AND c.subject = ANY($2)
			AND (p.done_at IS NULL OR (p.ledger_key = $3 AND p.outcome = 'POSTED'))`, tenantID, p.Subjects, p.Key)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key, hash string
			var created time.Time
			if err := rows.Scan(&key, &hash, &created); err != nil {
				return err
			}
			if key != p.Key || hash != p.Hash {
				return &PendingError{Subject: p.Subjects[0], Key: key, CreatedAt: created}
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		var armed string
		err = tx.QueryRow(ctx, `INSERT INTO receiving_pending_posting (tenant_id, ledger_key, base_key, subject, request_hash, journal) VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (tenant_id, ledger_key) DO UPDATE SET done_at = NULL, outcome = NULL, request_hash = EXCLUDED.request_hash, journal = EXCLUDED.journal, created_at = now()
			WHERE receiving_pending_posting.outcome = 'REJECTED' RETURNING ledger_key`, tenantID, p.Key, p.BaseKey, p.Subjects[0], p.Hash, p.Journal).Scan(&armed)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, subject := range slices.Compact(slices.Sorted(slices.Values(p.Subjects))) {
			if _, err := tx.Exec(ctx, `INSERT INTO receiving_pending_posting_subject (tenant_id, ledger_key, subject) VALUES ($1, $2, $3)
				ON CONFLICT (tenant_id, ledger_key, subject) DO UPDATE SET done_at = NULL`, tenantID, p.Key, subject); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Postgres) FinishPending(ctx context.Context, key, outcome string) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pendingPool(), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE receiving_pending_posting SET done_at = now(), outcome = $3 WHERE tenant_id = $1 AND ledger_key = $2 AND done_at IS NULL`, tenantID, key, outcome)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE receiving_pending_posting_subject SET done_at = now() WHERE tenant_id = $1 AND ledger_key = $2 AND done_at IS NULL`, tenantID, key)
		return err
	})
}

func (s *Postgres) ReleasePosting(ctx context.Context, key string) error {
	return s.FinishPending(ctx, key, OutcomeRejected)
}

func (s *Postgres) EnsureNoPending(ctx context.Context, subject string) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pendingPool(), func(tx pgx.Tx) error {
		if err := lockSubjects(ctx, tx, tenantID, []string{subject}); err != nil {
			return err
		}
		var key string
		var created time.Time
		err := tx.QueryRow(ctx, `SELECT p.ledger_key, p.created_at FROM receiving_pending_posting_subject c
			JOIN receiving_pending_posting p ON p.tenant_id = c.tenant_id AND p.ledger_key = c.ledger_key
			WHERE c.tenant_id = $1 AND c.subject = $2 AND p.done_at IS NULL LIMIT 1`, tenantID, subject).Scan(&key, &created)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return &PendingError{Subject: subject, Key: key, CreatedAt: created}
	})
}

func (s *Postgres) PostingGeneration(ctx context.Context, baseKey string) (int, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return 0, err
	}
	var n int
	err = dbx.WithTenantTx(ctx, s.pendingPool(), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM receiving_pending_posting WHERE tenant_id = $1 AND base_key = $2 AND outcome = 'REVERSED'`, tenantID, baseKey).Scan(&n)
	})
	return n, err
}

const pendingColumns = `p.ledger_key, p.base_key, p.request_hash, p.journal, COALESCE(p.outcome, ''), p.created_at, p.done_at,
	COALESCE((SELECT array_agg(c.subject ORDER BY c.subject) FROM receiving_pending_posting_subject c WHERE c.tenant_id = p.tenant_id AND c.ledger_key = p.ledger_key), '{}')`

func scanPending(row scanner) (PendingRow, error) {
	var r PendingRow
	err := row.Scan(&r.Key, &r.BaseKey, &r.Hash, &r.Journal, &r.Outcome, &r.CreatedAt, &r.DoneAt, &r.Subjects)
	return r, err
}

func (s *Postgres) GetPending(ctx context.Context, key string) (PendingRow, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return PendingRow{}, err
	}
	var out PendingRow
	err = dbx.WithTenantTx(ctx, s.pendingPool(), func(tx pgx.Tx) (err error) {
		out, err = scanPending(tx.QueryRow(ctx, `SELECT `+pendingColumns+` FROM receiving_pending_posting p WHERE p.tenant_id = $1 AND p.ledger_key = $2`, tenantID, key))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPendingNotFound
		}
		return err
	})
	return out, err
}

func (s *Postgres) ListPending(ctx context.Context) ([]PendingRow, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	var out []PendingRow
	err = dbx.WithTenantTx(ctx, s.pendingPool(), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+pendingColumns+` FROM receiving_pending_posting p WHERE p.tenant_id = $1 AND p.done_at IS NULL ORDER BY p.created_at LIMIT 200`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanPending(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
