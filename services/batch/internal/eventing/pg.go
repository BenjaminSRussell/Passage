package eventing

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	platform "github.com/BenjaminSRussell/passage/platform/eventing"
)

type PgSource struct{ pool *pgxpool.Pool }

func NewPgSource(pool *pgxpool.Pool) *PgSource { return &PgSource{pool: pool} }

func (s *PgSource) Drain(ctx context.Context, limit int, handle func(Row) error) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin drain: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	rows, err := tx.Query(ctx, `select id, tenant_id::text, aggregate_id, type, payload from outbox order by created_at, id limit $1 for update skip locked`, limit)
	if err != nil {
		return 0, fmt.Errorf("select outbox: %w", err)
	}
	pending, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Row, error) {
		var row Row
		err := r.Scan(&row.ID, &row.TenantID, &row.AggregateID, &row.Type, &row.Payload)
		return row, err
	})
	if err != nil {
		return 0, fmt.Errorf("read outbox: %w", err)
	}

	var acked []string
	var handleErr error
	for _, row := range pending {
		if handleErr = handle(row); handleErr != nil {
			break
		}
		acked = append(acked, row.ID)
	}
	if len(acked) > 0 {
		if _, err := tx.Exec(ctx, `delete from outbox where id = any($1)`, acked); err != nil {
			return 0, errors.Join(handleErr, fmt.Errorf("ack outbox: %w", err))
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, errors.Join(handleErr, fmt.Errorf("commit drain: %w", err))
	}
	return len(acked), handleErr
}

type PgInbox = platform.PgInbox

var NewPgInbox = platform.NewPgInbox
