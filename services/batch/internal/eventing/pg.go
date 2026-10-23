package eventing

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

type PgInbox struct{ pool *pgxpool.Pool }

func NewPgInbox(pool *pgxpool.Pool) *PgInbox { return &PgInbox{pool: pool} }

func (i *PgInbox) Once(ctx context.Context, consumer, eventID string, effect func(pgx.Tx) error) (bool, error) {
	if consumer == "" || eventID == "" || effect == nil {
		return false, fmt.Errorf("consumer, event id and effect are required: %w", ErrInvalidArgument)
	}
	tx, err := i.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin inbox: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	tag, err := tx.Exec(ctx, `insert into consumer_inbox(consumer, event_id) values($1,$2) on conflict do nothing`, consumer, eventID)
	if err != nil {
		return false, fmt.Errorf("insert inbox: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if err := effect(tx); err != nil {
		return false, fmt.Errorf("apply event %s: %w", eventID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit inbox: %w", err)
	}
	return true, nil
}
