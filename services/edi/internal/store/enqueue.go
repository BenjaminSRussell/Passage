package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/outbox"
)

// enqueueEvent writes one event of the aggregate to the outbox in tx.
func enqueueEvent(ctx context.Context, tx pgx.Tx, tid, eventType, aggregateType, aggregateID string, data any) error {
	evt, err := events.New(eventType, eventSource, aggregateID, tid, data)
	if err != nil {
		return err
	}
	if err := outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt); err != nil {
		return fmt.Errorf("edi: outbox: %w", err)
	}
	return nil
}
