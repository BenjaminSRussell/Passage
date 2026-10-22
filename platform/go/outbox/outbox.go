// Package outbox writes events into the service's outbox table inside the
// caller's transaction. Debezium's outbox event router relays rows to Kafka,
// so a business change and its event are committed atomically or not at all.
package outbox

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/events"
)

// Enqueue stores evt in the outbox within tx. aggregateType/aggregateID become
// the Kafka key so events for one aggregate stay ordered.
func Enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID string, evt events.Event) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into outbox(id, tenant_id, aggregate_type, aggregate_id, type, payload) values($1,$2,$3,$4,$5,$6)`,
		evt.ID, evt.TenantID, aggregateType, aggregateID, evt.Type, payload)
	return err
}
