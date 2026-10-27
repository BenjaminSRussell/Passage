package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

// Cursor is a keyset position in the message list, which is ordered newest
// first by (created_at, id).
type Cursor struct {
	At time.Time
	ID string
}

// MessageFilter selects messages for the monitor list (FR-35.10). Empty
// fields do not filter. Limit is 1..200.
type MessageFilter struct {
	PartnerID      string
	Direction      string
	Status         string
	TransactionSet string
	ControlNumber  string
	Limit          int
	After          *Cursor
}

// MessageRecord is one message as the monitor shows it, with the envelope of
// its interchange. Canonical is only set by GetMessage; the list leaves it out
// to keep pages small. The raw payload is never returned (BR-35.12).
type MessageRecord struct {
	ID             string          `json:"id"`
	InterchangeID  string          `json:"interchangeId"`
	PartnerID      string          `json:"partnerId"`
	PartnerName    string          `json:"partnerName"`
	Direction      string          `json:"direction"`
	Standard       string          `json:"standard"`
	ControlNumber  string          `json:"controlNumber"`
	AckStatus      string          `json:"ackStatus"`
	TransactionSet string          `json:"transactionSet"`
	STControl      string          `json:"stControl"`
	Status         string          `json:"status"`
	Error          string          `json:"error,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
	Canonical      json.RawMessage `json:"canonical,omitempty"`
}

const (
	messageColumns = `select m.id::text, m.interchange_id::text, i.partner_id::text, p.name, i.direction, i.standard,
	i.control_number, i.ack_status, m.transaction_set, m.st_control, m.status, coalesce(m.error, ''), m.created_at`
	messageFrom = `
	from message m
	join interchange i on i.tenant_id = m.tenant_id and i.id = m.interchange_id
	join trading_partner p on p.tenant_id = i.tenant_id and p.id = i.partner_id`
)

func messageTargets(r *MessageRecord) []any {
	return []any{&r.ID, &r.InterchangeID, &r.PartnerID, &r.PartnerName, &r.Direction, &r.Standard,
		&r.ControlNumber, &r.AckStatus, &r.TransactionSet, &r.STControl, &r.Status, &r.Error, &r.CreatedAt}
}

// ListMessages returns one page of messages, newest first. A non-nil After
// continues strictly after that position (keyset pagination, no offsets).
func (s *Store) ListMessages(ctx context.Context, f MessageFilter) ([]MessageRecord, error) {
	out := []MessageRecord{}
	var afterAt, afterID any
	if f.After != nil {
		afterAt, afterID = f.After.At, f.After.ID
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, messageColumns+messageFrom+`
			where ($1 = '' or i.partner_id::text = $1)
			and ($2 = '' or i.direction = $2)
			and ($3 = '' or m.status = $3)
			and ($4 = '' or m.transaction_set = $4)
			and ($5 = '' or i.control_number = $5)
			and ($6::timestamptz is null or (m.created_at, m.id) < ($6::timestamptz, $7::uuid))
			order by m.created_at desc, m.id desc limit $8`,
			f.PartnerID, f.Direction, f.Status, f.TransactionSet, f.ControlNumber, afterAt, afterID, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rec MessageRecord
			if err := rows.Scan(messageTargets(&rec)...); err != nil {
				return err
			}
			out = append(out, rec)
		}
		return rows.Err()
	})
	return out, err
}

// GetMessage returns one message with its canonical document, or ErrNotFound
// when the id is unknown in the caller's tenant.
func (s *Store) GetMessage(ctx context.Context, id string) (MessageRecord, error) {
	var rec MessageRecord
	var canonical []byte
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, messageColumns+`, m.canonical`+messageFrom+` where m.id = $1`, id).Scan(append(messageTargets(&rec), &canonical)...)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	if err != nil {
		return MessageRecord{}, err
	}
	if len(canonical) > 0 {
		rec.Canonical = canonical
	}
	return rec, nil
}
