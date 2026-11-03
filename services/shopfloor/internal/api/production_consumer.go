package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

const (
	// OperationStatusType is production's operation.statuschanged.v1 (w21): an operation was
	// finished (the finish command, or the last report as finished) or reopened.
	OperationStatusType  = "dev.passage.production.operation.statuschanged.v1"
	OperationStatusTopic = "passage.production.operation"
	// OperationStatusSource is the CloudEvents source production writes on its events. Who may
	// write to the topic at all is the broker's ACL (only production's outbox relay); the check
	// keeps an event another service mislabels or misroutes from closing operations.
	OperationStatusSource = "production"
	// ProductionConsumerName is the consumer (and Kafka group) that follows production's
	// operation status, and the service principal it runs as, service:shopfloor-production.
	ProductionConsumerName = "shopfloor-production"
	// DutyProductionSync lets the consumer store production's operation status. Only its
	// service principal holds it.
	DutyProductionSync = "shopfloor.job.sync"
)

// ProductionStore is the persistence the production consumer needs.
type ProductionStore interface {
	ApplyOperationStatus(ctx context.Context, tx pgx.Tx, in store.OperationStatusIn) (bool, error)
}

type operationStatusData struct {
	OrderID      string `json:"orderId"`
	OperNumber   int    `json:"operNumber"`
	Action       string `json:"action"`
	From         string `json:"from"`
	To           string `json:"to"`
	OrderVersion int    `json:"orderVersion"`
	Actor        string `json:"actor"`
	OccurredAt   string `json:"occurredAt"`
}

// ProductionConsumerHandlers handles production's operation.statuschanged.v1: an operation that
// went to finished stops new job starts on its jobs, one that went back to open or started
// (production reopened it) lets them start again. Register it with a consumer named
// ProductionConsumerName, which runs with the DutyProductionSync duty.
func ProductionConsumerHandlers(s ProductionStore) map[string]eventing.Handler {
	h := productionConsumer{store: s}
	return map[string]eventing.Handler{OperationStatusType: eventing.HandlerFunc(h.operationStatusChanged)}
}

type productionConsumer struct{ store ProductionStore }

func (c productionConsumer) operationStatusChanged(ctx context.Context, tx pgx.Tx, evt events.Event) error {
	actor, ok := tenant.FromContext(ctx)
	if !ok || !actor.HasDuty("shopfloor.job.sync") {
		return eventing.Permanent(errors.New("production consumer has no principal"))
	}
	if evt.Source != OperationStatusSource {
		return eventing.Permanent(fmt.Errorf("operation.statuschanged from source %q, want %q", evt.Source, OperationStatusSource))
	}
	var d operationStatusData
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return eventing.Permanent(fmt.Errorf("operation.statuschanged data: %w", err))
	}
	at, err := time.Parse(time.RFC3339Nano, d.OccurredAt)
	if err != nil || !ids.Valid(d.OrderID) || d.OperNumber <= 0 || d.To == "" || d.OrderVersion < 1 {
		return eventing.Permanent(errors.New("operation.statuschanged needs orderId, operNumber, to, orderVersion and occurredAt"))
	}
	changed, err := c.store.ApplyOperationStatus(ctx, tx, store.OperationStatusIn{
		OrderID: strings.ToLower(d.OrderID), OperNumber: d.OperNumber, Status: d.To, Finished: d.To == "finished", At: at.UTC(),
		OrderVersion: d.OrderVersion, Actor: d.Actor, EventID: evt.ID,
	})
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "production operation status applied", "orderId", d.OrderID, "operNumber", d.OperNumber, "to", d.To,
		"changed", changed, "tenantId", evt.TenantID, "eventId", evt.ID)
	return nil
}
