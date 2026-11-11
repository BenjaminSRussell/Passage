package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// Process message catalog codes raised by the store (see README).
const (
	CodeProcessNotFound  = "SF-0117"
	CodeDeliveryNotFound = "SF-0118"
	CodeMessageDeleted   = "SF-0120"

	processAggregate = "processmessage"
)

// ProcessMessage is a stored process message with its per-destination deliveries (FR-25.17).
type ProcessMessage struct {
	ID              string                  `json:"id"`
	Source          string                  `json:"source"`
	SourceMessageID string                  `json:"sourceMessageId"`
	CategoryCode    string                  `json:"categoryCode"`
	OrderID         string                  `json:"orderId"`
	PhasePath       string                  `json:"phasePath"`
	OccurredAt      time.Time               `json:"occurredAt"`
	ReceivedAt      time.Time               `json:"receivedAt"`
	Status          string                  `json:"status"`
	OutOfLimit      bool                    `json:"outOfLimit"`
	Values          []domain.Characteristic `json:"values"`
	Deliveries      []domain.Delivery       `json:"deliveries"`
	// DeletedAt, DeletedBy and DeleteReason are set once a supervisor deleted the
	// message; it is kept for audit but leaves the monitor.
	DeletedAt    *time.Time `json:"deletedAt"`
	DeletedBy    *string    `json:"deletedBy"`
	DeleteReason *string    `json:"deleteReason"`
}

// ProcessIngest reports the stored message and whether the call was a replay of
// an existing (source, sourceMessageId) pair.
type ProcessIngest struct {
	Message   ProcessMessage `json:"message"`
	Duplicate bool           `json:"duplicate"`
}

// ProcessFilter narrows the monitor list. Empty strings match everything.
// After, when set, starts the page after that message (keyset paging, newest first).
// Deleted messages never appear.
type ProcessFilter struct {
	Status      string
	Category    string
	Destination string
	OrderID     string
	Limit       int
	After       *ProcessCursor
}

// ProcessCursor is the position of a message in the monitor order (received_at desc, id desc).
type ProcessCursor struct {
	ReceivedAt time.Time
	ID         string
}

// RetryClaim is one destination the retry sweep published again (FR-25.17). The
// sweep will not publish it again before NextAttemptAt unless its consumer reports.
type RetryClaim struct {
	MessageID     string    `json:"messageId"`
	Destination   string    `json:"destination"`
	Attempts      int       `json:"attempts"`
	NextAttemptAt time.Time `json:"nextAttemptAt"`
}

func processNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeProcessNotFound, "process message not found")
}

func messageDeleted() error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeMessageDeleted, "the process message was deleted")
}

func deliveryNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeDeliveryNotFound, "the message has no delivery to that destination")
}

// IngestProcessMessage stores a validated message and one delivery per destination
// of its category, with dev.passage.shopfloor.processmessage.received.v1 in the same
// transaction. The Kafka destination is the outbox row itself, so it is delivered at
// commit. A replay of (source, sourceMessageId) returns the stored message and
// writes nothing (FR-25.17: idempotent by source message id).
func (s *Postgres) IngestProcessMessage(ctx context.Context, in domain.NewMessage, cat domain.Category, now time.Time) (ProcessIngest, error) {
	p, _ := tenant.FromContext(ctx)
	raw, deliveries, status, values, err := prepareProcessMessage(in, cat, now)
	if err != nil {
		return ProcessIngest{}, err
	}
	var out ProcessIngest
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = ingestProcessMessageTx(ctx, tx, p.TenantID, in, cat, now, raw, deliveries, status, values)
		return err
	})
	return out, err
}

// prepareProcessMessage builds what storing a message needs: its characteristics as JSON, one delivery per
// destination of the category (Kafka is delivered at commit, as the outbox row itself) and the rolled-up status.
func prepareProcessMessage(in domain.NewMessage, cat domain.Category, now time.Time) (raw []byte, deliveries []domain.Delivery, status string, values []domain.Characteristic, err error) {
	values = in.Values
	if values == nil {
		values = []domain.Characteristic{}
	}
	if raw, err = json.Marshal(values); err != nil {
		return nil, nil, "", nil, err
	}
	for _, dest := range cat.Destinations {
		at := now
		d := domain.Delivery{Destination: dest, Status: domain.DeliveryPending, NextAttemptAt: &at}
		if dest == domain.DestKafka {
			d = domain.Delivery{Destination: dest, Status: domain.DeliveryDelivered, Attempts: 1, DeliveredAt: &at}
		}
		deliveries = append(deliveries, d)
	}
	return raw, deliveries, domain.RollupStatus(deliveries), values, nil
}

// ingestProcessMessageTx is IngestProcessMessage inside a transaction the caller owns, so another command (a
// control recipe report that finds a deviation) can record its message with the rest of its changes. A new
// message that is an end-of-batch message of a process control system also ends the recipe it names.
func ingestProcessMessageTx(ctx context.Context, tx pgx.Tx, tenantID string, in domain.NewMessage, cat domain.Category, now time.Time,
	raw []byte, deliveries []domain.Delivery, status string, values []domain.Characteristic) (ProcessIngest, error) {
	p := tenant.Principal{TenantID: tenantID}
	var out ProcessIngest
	id := ids.New()
	tag, err := tx.Exec(ctx, `insert into process_message (tenant_id, id, source, source_message_id, category_code, order_id, phase_path,
				characteristics, occurred_at, received_at, status)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			on conflict (tenant_id, source, source_message_id) do nothing`,
		p.TenantID, id, in.Source, in.SourceMessageID, in.CategoryCode, in.OrderID, in.PhasePath, raw, in.OccurredAt, now, status)
	if err != nil {
		return out, err
	}
	if tag.RowsAffected() == 0 {
		msgs, err := loadProcessMessages(ctx, tx, p.TenantID, "source = $2 and source_message_id = $3", []any{in.Source, in.SourceMessageID}, 1)
		if err != nil {
			return out, err
		}
		if len(msgs) == 0 {
			return out, fmt.Errorf("process message %s/%s vanished during replay", in.Source, in.SourceMessageID)
		}
		return ProcessIngest{Message: msgs[0], Duplicate: true}, nil
	}
	for _, d := range deliveries {
		if _, err := tx.Exec(ctx, `insert into process_message_delivery (tenant_id, message_id, destination, status, attempts, next_attempt_at, delivered_at)
				values ($1, $2, $3, $4, $5, $6, $7)`, p.TenantID, id, d.Destination, d.Status, d.Attempts, d.NextAttemptAt, d.DeliveredAt); err != nil {
			return out, err
		}
	}
	if err := enqueue(ctx, tx, processAggregate, id, sfevents.ProcessMessageReceived, p.TenantID, sfevents.ProcessMessageReceivedData{
		MessageID: id, Source: in.Source, SourceMessageID: in.SourceMessageID, CategoryCode: in.CategoryCode, OrderID: in.OrderID,
		PhasePath: in.PhasePath, Destinations: cat.Destinations, Values: values, OutOfLimit: domain.AnyOutOfLimit(values), OccurredAt: in.OccurredAt,
	}); err != nil {
		return out, err
	}
	if status == domain.MessageDelivered {
		if err := enqueue(ctx, tx, processAggregate, id, sfevents.ProcessMessageDelivered, p.TenantID, sfevents.ProcessMessageDeliveredData{
			MessageID: id, CategoryCode: in.CategoryCode, OrderID: in.OrderID,
		}); err != nil {
			return out, err
		}
	}
	if err := endRecipesFromMessage(ctx, tx, p.TenantID, id, in, values); err != nil {
		return out, err
	}
	msgs, err := loadProcessMessages(ctx, tx, p.TenantID, "id = $2::uuid", []any{id}, 1)
	if err != nil {
		return out, err
	}
	out = ProcessIngest{Message: msgs[0]}
	return out, nil
}

// StoredProcessMessage returns the message stored under (source, sourceMessageID), if any. The
// handler asks for it when a message fails its category checks, so a replay of a message stored
// before its category was deactivated or tightened still answers with the stored message.
func (s *Postgres) StoredProcessMessage(ctx context.Context, source, sourceMessageID string) (ProcessMessage, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var msgs []ProcessMessage
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		msgs, err = loadProcessMessages(ctx, tx, p.TenantID, "source = $2 and source_message_id = $3", []any{source, sourceMessageID}, 1)
		return err
	})
	if err != nil || len(msgs) == 0 {
		return ProcessMessage{}, false, err
	}
	return msgs[0], true, nil
}

// ListProcessMessages is the process message monitor (CO54 equivalent): newest
// first, filtered by status, category, destination and order, without deleted
// messages, one keyset page at a time.
func (s *Postgres) ListProcessMessages(ctx context.Context, f ProcessFilter) ([]ProcessMessage, error) {
	p, _ := tenant.FromContext(ctx)
	var afterAt *time.Time
	var afterID *string
	if f.After != nil {
		afterAt, afterID = &f.After.ReceivedAt, &f.After.ID
	}
	var out []ProcessMessage
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = loadProcessMessages(ctx, tx, p.TenantID,
			`deleted_at is null and ($2::text = '' or status = $2) and ($3::text = '' or category_code = $3)
			and ($4::text = '' or order_id = nullif($4, '')::uuid)
			and ($5::text = '' or exists (select 1 from process_message_delivery d
				where d.tenant_id = process_message.tenant_id and d.message_id = process_message.id and d.destination = $5))
			and ($6::timestamptz is null or (received_at, id) < ($6::timestamptz, $7::uuid))`,
			[]any{f.Status, f.Category, f.OrderID, f.Destination, afterAt, afterID}, f.Limit)
		return err
	})
	if out == nil {
		out = []ProcessMessage{}
	}
	return out, err
}

// GetProcessMessage returns one message with its deliveries, deleted or not.
func (s *Postgres) GetProcessMessage(ctx context.Context, id string) (ProcessMessage, error) {
	p, _ := tenant.FromContext(ctx)
	var out ProcessMessage
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		msgs, err := loadProcessMessages(ctx, tx, p.TenantID, "id = $2::uuid", []any{id}, 1)
		if err != nil {
			return err
		}
		if len(msgs) == 0 {
			return processNotFound()
		}
		out = msgs[0]
		return nil
	})
	return out, err
}

// ReportDelivery records the outcome a consuming context reports for its
// destination. Failures follow the 1, 5, 15, 60 minute schedule and end in
// error after five attempts. A message that becomes fully delivered emits
// processmessage.delivered once.
func (s *Postgres) ReportDelivery(ctx context.Context, messageID, destination, outcome string, errText *string, now time.Time, idem Idem) (ProcessMessage, error) {
	return s.mutateDelivery(ctx, messageID, destination, nil, idem, func(d domain.Delivery) (domain.Delivery, error) {
		return d.Report(outcome, errText, now)
	})
}

// ReprocessDelivery moves a destination in error back to pending, records the
// reason, and re-emits processmessage.received for that destination only.
func (s *Postgres) ReprocessDelivery(ctx context.Context, messageID, destination, reason string, now time.Time, idem Idem) (ProcessMessage, error) {
	return s.mutateDelivery(ctx, messageID, destination, &reason, idem, func(d domain.Delivery) (domain.Delivery, error) {
		return d.Reprocess(now)
	})
}

// mutateDelivery locks the message, applies op to one destination and stores the
// result with the rolled-up message status and the Idempotency-Key outcome, all
// in one transaction.
func (s *Postgres) mutateDelivery(ctx context.Context, messageID, destination string, reason *string, idem Idem,
	op func(domain.Delivery) (domain.Delivery, error)) (ProcessMessage, error) {
	p, _ := tenant.FromContext(ctx)
	var out ProcessMessage
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		msg, err := lockProcessMessage(ctx, tx, p.TenantID, messageID)
		if err != nil {
			return err
		}
		if msg.DeletedAt != nil {
			return messageDeleted()
		}
		idx := -1
		for i, d := range msg.Deliveries {
			if d.Destination == destination {
				idx = i
			}
		}
		if idx < 0 {
			return deliveryNotFound()
		}
		next, err := op(msg.Deliveries[idx])
		if err != nil {
			var de *domain.Error
			if errors.As(err, &de) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail)
			}
			return err
		}
		if _, err := tx.Exec(ctx, `update process_message_delivery set status = $4, attempts = $5, last_error = $6,
			next_attempt_at = $7, delivered_at = $8, reprocess_reason = coalesce($9::text, reprocess_reason)
			where tenant_id = $1 and message_id = $2::uuid and destination = $3`,
			p.TenantID, messageID, destination, next.Status, next.Attempts, next.LastError, next.NextAttemptAt, next.DeliveredAt, reason); err != nil {
			return err
		}
		msg.Deliveries[idx] = next
		status := domain.RollupStatus(msg.Deliveries)
		if status != msg.Status {
			if _, err := tx.Exec(ctx, `update process_message set status = $3 where tenant_id = $1 and id = $2::uuid`,
				p.TenantID, messageID, status); err != nil {
				return err
			}
		}
		if status == domain.MessageDelivered && msg.Status != domain.MessageDelivered {
			if err := enqueue(ctx, tx, processAggregate, messageID, sfevents.ProcessMessageDelivered, p.TenantID,
				sfevents.ProcessMessageDeliveredData{MessageID: messageID, CategoryCode: msg.CategoryCode, OrderID: msg.OrderID}); err != nil {
				return err
			}
		}
		if reason != nil {
			if err := enqueue(ctx, tx, processAggregate, messageID, sfevents.ProcessMessageReceived, p.TenantID,
				sfevents.ProcessMessageReceivedData{MessageID: messageID, Source: msg.Source, SourceMessageID: msg.SourceMessageID,
					CategoryCode: msg.CategoryCode, OrderID: msg.OrderID, PhasePath: msg.PhasePath, Destinations: []string{destination},
					Values: msg.Values, OutOfLimit: msg.OutOfLimit, OccurredAt: msg.OccurredAt}); err != nil {
				return err
			}
		}
		msg.Status = status
		out = msg
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, err
}

// DeleteProcessMessage deletes a message with a reason (FR-25.17). Only a message
// with a destination in error can be deleted (SF-0119); a deleted one cannot be
// deleted again (SF-0120). The row is kept for audit with who deleted it and why,
// leaves the monitor, cannot be reported or reprocessed, and the retry sweep skips
// it. dev.passage.shopfloor.processmessage.deleted.v1 names the destinations that
// never received the message, so the EBR consumer can record the exception.
func (s *Postgres) DeleteProcessMessage(ctx context.Context, messageID, reason string, now time.Time, idem Idem) (ProcessMessage, error) {
	p, _ := tenant.FromContext(ctx)
	var out ProcessMessage
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		msg, err := lockProcessMessage(ctx, tx, p.TenantID, messageID)
		if err != nil {
			return err
		}
		if msg.DeletedAt != nil {
			return messageDeleted()
		}
		if err := domain.CheckDeletable(msg.Deliveries); err != nil {
			var de *domain.Error
			if errors.As(err, &de) {
				return httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail)
			}
			return err
		}
		if _, err := tx.Exec(ctx, `update process_message set deleted_at = $3, deleted_by = $4, delete_reason = $5
			where tenant_id = $1 and id = $2::uuid`, p.TenantID, messageID, now, p.UserID, reason); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, processAggregate, messageID, sfevents.ProcessMessageDeleted, p.TenantID, sfevents.ProcessMessageDeletedData{
			MessageID: messageID, CategoryCode: msg.CategoryCode, OrderID: msg.OrderID, Reason: reason,
			Undelivered: domain.Undelivered(msg.Deliveries), DeletedBy: p.UserID, DeletedAt: now,
		}); err != nil {
			return err
		}
		at, by, why := now, p.UserID, reason
		msg.DeletedAt, msg.DeletedBy, msg.DeleteReason = &at, &by, &why
		out = msg
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, err
}

// ClaimRetries is the retry sweep (FR-25.17, every minute per tenant). It takes up
// to limit destinations that failed, are still pending and whose wait has elapsed,
// leases each (domain.Delivery.Claim) and publishes processmessage.received again,
// once per message, naming only its due destinations. Messages locked by another
// sweep, a report, a reprocess or a delete are skipped, so concurrent sweeps never
// publish the same retry twice. Deleted messages are never retried.
func (s *Postgres) ClaimRetries(ctx context.Context, now time.Time, limit int) ([]RetryClaim, error) {
	p, _ := tenant.FromContext(ctx)
	out := []RetryClaim{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select d.message_id::text, d.destination, d.status, d.attempts, d.next_attempt_at
			from process_message_delivery d
			join process_message m on m.tenant_id = d.tenant_id and m.id = d.message_id
			where d.tenant_id = $1 and d.status = $2 and d.attempts > 0 and d.next_attempt_at <= $3 and m.deleted_at is null
			order by d.next_attempt_at, d.message_id, d.destination
			limit $4
			for update of m skip locked`, p.TenantID, domain.DeliveryPending, now, limit)
		if err != nil {
			return err
		}
		due := map[string][]domain.Delivery{}
		var order []string
		for rows.Next() {
			var msgID string
			var d domain.Delivery
			if err := rows.Scan(&msgID, &d.Destination, &d.Status, &d.Attempts, &d.NextAttemptAt); err != nil {
				rows.Close()
				return err
			}
			if !d.DueForRetry(now) {
				continue
			}
			if _, seen := due[msgID]; !seen {
				order = append(order, msgID)
			}
			due[msgID] = append(due[msgID], d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(order) == 0 {
			return nil
		}
		msgs, err := loadProcessMessages(ctx, tx, p.TenantID, "id = any($2::uuid[])", []any{order}, 0)
		if err != nil {
			return err
		}
		byID := make(map[string]ProcessMessage, len(msgs))
		for _, m := range msgs {
			byID[m.ID] = m
		}
		for _, msgID := range order {
			msg := byID[msgID]
			dests := make([]string, 0, len(due[msgID]))
			for _, d := range due[msgID] {
				claimed := d.Claim(now)
				if _, err := tx.Exec(ctx, `update process_message_delivery set next_attempt_at = $4
					where tenant_id = $1 and message_id = $2::uuid and destination = $3`,
					p.TenantID, msgID, d.Destination, claimed.NextAttemptAt); err != nil {
					return err
				}
				dests = append(dests, d.Destination)
				out = append(out, RetryClaim{MessageID: msgID, Destination: d.Destination, Attempts: d.Attempts, NextAttemptAt: *claimed.NextAttemptAt})
			}
			if err := enqueue(ctx, tx, processAggregate, msgID, sfevents.ProcessMessageReceived, p.TenantID,
				sfevents.ProcessMessageReceivedData{MessageID: msgID, Source: msg.Source, SourceMessageID: msg.SourceMessageID,
					CategoryCode: msg.CategoryCode, OrderID: msg.OrderID, PhasePath: msg.PhasePath, Destinations: dests,
					Values: msg.Values, OutOfLimit: msg.OutOfLimit, OccurredAt: msg.OccurredAt}); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// lockProcessMessage locks one message for a change and loads it with its deliveries.
func lockProcessMessage(ctx context.Context, tx pgx.Tx, tenantID, messageID string) (ProcessMessage, error) {
	var locked string
	err := tx.QueryRow(ctx, `select id::text from process_message where tenant_id = $1 and id = $2::uuid for update`,
		tenantID, messageID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProcessMessage{}, processNotFound()
	}
	if err != nil {
		return ProcessMessage{}, err
	}
	msgs, err := loadProcessMessages(ctx, tx, tenantID, "id = $2::uuid", []any{messageID}, 1)
	if err != nil {
		return ProcessMessage{}, err
	}
	if len(msgs) == 0 {
		return ProcessMessage{}, processNotFound()
	}
	return msgs[0], nil
}

// loadProcessMessages reads messages matching where (which uses placeholders from $2)
// and attaches their deliveries. limit 0 means no limit.
func loadProcessMessages(ctx context.Context, tx pgx.Tx, tenantID, where string, args []any, limit int) ([]ProcessMessage, error) {
	q := `select id::text, source, source_message_id, category_code, order_id::text, phase_path, characteristics,
		occurred_at, received_at, status, deleted_at, deleted_by, delete_reason from process_message where tenant_id = $1 and (` + where + `)
		order by received_at desc, id desc`
	if limit > 0 {
		q += fmt.Sprintf(" limit %d", limit)
	}
	rows, err := tx.Query(ctx, q, append([]any{tenantID}, args...)...)
	if err != nil {
		return nil, err
	}
	var msgs []ProcessMessage
	index := map[string]int{}
	var msgIDs []string
	for rows.Next() {
		var m ProcessMessage
		var chars []byte
		if err := rows.Scan(&m.ID, &m.Source, &m.SourceMessageID, &m.CategoryCode, &m.OrderID, &m.PhasePath, &chars,
			&m.OccurredAt, &m.ReceivedAt, &m.Status, &m.DeletedAt, &m.DeletedBy, &m.DeleteReason); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal(chars, &m.Values); err != nil {
			rows.Close()
			return nil, err
		}
		if m.Values == nil {
			m.Values = []domain.Characteristic{}
		}
		m.OutOfLimit = domain.AnyOutOfLimit(m.Values)
		m.Deliveries = []domain.Delivery{}
		index[m.ID] = len(msgs)
		msgIDs = append(msgIDs, m.ID)
		msgs = append(msgs, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return msgs, nil
	}
	drows, err := tx.Query(ctx, `select message_id::text, destination, status, attempts, last_error, next_attempt_at, delivered_at
		from process_message_delivery where tenant_id = $1 and message_id = any($2::uuid[]) order by destination`, tenantID, msgIDs)
	if err != nil {
		return nil, err
	}
	defer drows.Close()
	for drows.Next() {
		var msgID string
		var d domain.Delivery
		if err := drows.Scan(&msgID, &d.Destination, &d.Status, &d.Attempts, &d.LastError, &d.NextAttemptAt, &d.DeliveredAt); err != nil {
			return nil, err
		}
		i := index[msgID]
		msgs[i].Deliveries = append(msgs[i].Deliveries, d)
	}
	return msgs, drows.Err()
}
