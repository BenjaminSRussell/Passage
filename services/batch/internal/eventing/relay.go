package eventing

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	platform "github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
)

var ErrInvalidArgument = platform.ErrInvalidArgument

type Row struct {
	ID          string
	TenantID    string
	AggregateID string
	Type        string
	Payload     []byte
}

type Message struct {
	ID       string
	TenantID string
	Type     string
	Key      string
	Topic    string
	Payload  []byte
}

type Source interface {
	Drain(ctx context.Context, limit int, handle func(Row) error) (int, error)
}

type Publisher interface {
	Publish(ctx context.Context, msg Message) error
}

type Inbox interface {
	Once(ctx context.Context, consumer, eventID string, effect func(pgx.Tx) error) (bool, error)
}

type Relay struct {
	source    Source
	publisher Publisher
	log       *slog.Logger
}

func NewRelay(source Source, publisher Publisher, log *slog.Logger) *Relay {
	return &Relay{source: source, publisher: publisher, log: log}
}

func (r *Relay) Drain(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("limit %d: %w", limit, ErrInvalidArgument)
	}
	return r.source.Drain(ctx, limit, func(row Row) error {
		msg, err := messageFromRow(row)
		if err != nil {
			r.log.Error("outbox row rejected", "outboxId", row.ID, "correlationId", correlationID(row.Payload), "error", err)
			return fmt.Errorf("outbox row %s: %w", row.ID, err)
		}
		if err := r.publisher.Publish(ctx, msg); err != nil {
			r.log.Error("outbox publish failed", "outboxId", row.ID, "correlationId", correlationID(row.Payload), "error", err)
			return fmt.Errorf("publish outbox row %s: %w", row.ID, err)
		}
		return nil
	})
}

func messageFromRow(row Row) (Message, error) {
	if !events.ValidType(row.Type) {
		return Message{}, fmt.Errorf("event type %q: %w", row.Type, ErrInvalidArgument)
	}
	if !ids.Valid(row.TenantID) {
		return Message{}, fmt.Errorf("tenant id %q: %w", row.TenantID, ErrInvalidArgument)
	}
	var evt events.Event
	if err := json.Unmarshal(row.Payload, &evt); err != nil {
		return Message{}, fmt.Errorf("decode payload: %w", err)
	}
	if evt.Type != row.Type || evt.TenantID != row.TenantID {
		return Message{}, fmt.Errorf("payload type or tenantid disagrees with row: %w", ErrInvalidArgument)
	}
	return Message{
		ID:       row.ID,
		TenantID: row.TenantID,
		Type:     row.Type,
		Key:      row.TenantID + ":" + row.AggregateID,
		Topic:    events.Topic(row.Type),
		Payload:  row.Payload,
	}, nil
}

func correlationID(payload []byte) string {
	var evt struct {
		CorrelationID string `json:"correlationid"`
	}
	_ = json.Unmarshal(payload, &evt)
	return evt.CorrelationID
}
