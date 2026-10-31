package tenant

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const replayPurgeInterval = time.Minute

// ReplayStore remembers service token ids across every replica of a target.
type ReplayStore interface {
	Remember(ctx context.Context, tenantID, id string, expires time.Time) (fresh bool, err error)
}

// PostgresReplayStore keeps token ids in service_token_replay under the tenant's
// row-level security. A conflicting insert is a replay. Each call also deletes
// the tenant's expired rows, at most once a minute per process.
type PostgresReplayStore struct {
	pool      *pgxpool.Pool
	nextPurge atomic.Int64
}

func NewPostgresReplayStore(pool *pgxpool.Pool) *PostgresReplayStore {
	return &PostgresReplayStore{pool: pool}
}

func (s *PostgresReplayStore) Remember(ctx context.Context, tenantID, id string, expires time.Time) (bool, error) {
	batch := &pgx.Batch{}
	batch.Queue(`select set_config('passage.tenant_id', $1, true)`, tenantID)
	purge := s.purgeDue()
	if purge {
		batch.Queue(`delete from service_token_replay where expires_at < now()`)
	}
	batch.Queue(`insert into service_token_replay (tenant_id, jti, expires_at) values ($1, $2, $3) on conflict do nothing`, tenantID, id, expires)
	fresh := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		results := tx.SendBatch(ctx, batch)
		defer func() { _ = results.Close() }()
		for i := 0; i < batch.Len(); i++ {
			tag, err := results.Exec()
			if err != nil {
				return err
			}
			if i == batch.Len()-1 {
				fresh = tag.RowsAffected() == 1
			}
		}
		return results.Close()
	})
	if err != nil && purge {
		s.nextPurge.Store(0)
	}
	return fresh, err
}

func (s *PostgresReplayStore) purgeDue() bool {
	now := time.Now().Unix()
	next := s.nextPurge.Load()
	return now >= next && s.nextPurge.CompareAndSwap(next, now+int64(replayPurgeInterval/time.Second))
}
