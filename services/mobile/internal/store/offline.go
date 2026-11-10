package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	evts "github.com/BenjaminSRussell/passage/services/mobile/internal/events"
)

const (
	TxPending  = "PENDING"
	TxApplied  = "APPLIED"
	TxConflict = "CONFLICT"
	TxRejected = "REJECTED"

	ResolveReapply = "REAPPLY"
	ResolveReject  = "REJECT"
)

// GapError is a transaction whose seq is not the device's next (MOB-0108).
type GapError struct {
	DeviceID      string
	Expected, Got int64
}

func (e *GapError) Error() string {
	return fmt.Sprintf("sequence gap on device %s: expected %d, received %d", e.DeviceID, e.Expected, e.Got)
}

type OfflineTx struct {
	IdempotencyKey string          `json:"idempotencyKey"`
	DeviceID       string          `json:"deviceId"`
	SessionID      string          `json:"sessionId,omitempty"`
	UserID         string          `json:"userId"`
	Seq            int64           `json:"seq"`
	Command        string          `json:"command"`
	Payload        json.RawMessage `json:"payload"`
	CapturedAt     time.Time       `json:"capturedAt"`
	Status         string          `json:"replayStatus"`
	ConflictReason string          `json:"conflictReason,omitempty"`
	Resolution     string          `json:"resolution,omitempty"`
	ResolvedBy     string          `json:"resolvedBy,omitempty"`
	Version        int             `json:"version"`
}

const txColumns = `idempotency_key, device_id, coalesce(session_id::text, ''), user_id, seq, command, payload, captured_at, replay_status, conflict_reason, resolution, resolved_by, version`

func scanTx(row scanner) (OfflineTx, error) {
	var t OfflineTx
	err := row.Scan(&t.IdempotencyKey, &t.DeviceID, &t.SessionID, &t.UserID, &t.Seq, &t.Command, &t.Payload, &t.CapturedAt, &t.Status, &t.ConflictReason, &t.Resolution, &t.ResolvedBy, &t.Version)
	return t, err
}

func OfflineKey(deviceID string, seq int64) string { return fmt.Sprintf("%s:%d", deviceID, seq) }

type OfflineIn struct {
	DeviceID   string
	SessionID  string
	Seq        int64
	Command    string
	Payload    json.RawMessage
	CapturedAt time.Time
	Hash       string
}

// Record stores a queued transaction as PENDING, or returns the stored one
// when its key was already recorded (a repeat changes nothing; a repeat with
// another body is ErrIdempotencyReuse; one queued by another user is
// ErrNotOwner). A seq that is not the device's next is
// a GapError and nothing is stored.
func (s *Store) Record(ctx context.Context, in OfflineIn) (OfflineTx, error) {
	var out OfflineTx
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		key := OfflineKey(in.DeviceID, in.Seq)
		var hash, owner string
		err := tx.QueryRow(ctx, `select payload_hash, user_id from offline_transaction where tenant_id = $1 and idempotency_key = $2`, tenantID(ctx), key).Scan(&hash, &owner)
		if err == nil {
			if owner != userID(ctx) {
				return ErrNotOwner
			}
			if hash != in.Hash {
				return ErrIdempotencyReuse
			}
			out, err = scanTx(tx.QueryRow(ctx, `select `+txColumns+` from offline_transaction where tenant_id = $1 and idempotency_key = $2`, tenantID(ctx), key))
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var last *int64
		if err := tx.QueryRow(ctx, `select max(seq) from offline_transaction where tenant_id = $1 and device_id = $2`, tenantID(ctx), in.DeviceID).Scan(&last); err != nil {
			return err
		}
		if last != nil && in.Seq != *last+1 {
			return &GapError{DeviceID: in.DeviceID, Expected: *last + 1, Got: in.Seq}
		}
		var session any
		if in.SessionID != "" {
			session = in.SessionID
		}
		out, err = scanTx(tx.QueryRow(ctx, `insert into offline_transaction (tenant_id, idempotency_key, device_id, session_id, user_id, seq, command, payload, payload_hash, captured_at, replay_status)
			values ($1, $2, $3, $4::uuid, $5, $6, $7, $8, $9, $10, 'PENDING') returning `+txColumns,
			tenantID(ctx), key, in.DeviceID, session, userID(ctx), in.Seq, in.Command, in.Payload, in.Hash, in.CapturedAt))
		return err
	})
	return out, err
}

// NextPending is the device's lowest-seq PENDING transaction when nothing
// before it is unresolved: an earlier unresolved CONFLICT blocks it (BR-32.3).
func (s *Store) NextPending(ctx context.Context, deviceID string) (*OfflineTx, error) {
	var out *OfflineTx
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		t, err := scanTx(tx.QueryRow(ctx, `select `+txColumns+` from offline_transaction p
			where p.tenant_id = $1 and p.device_id = $2 and p.replay_status = 'PENDING'
			  and not exists (select 1 from offline_transaction e where e.tenant_id = p.tenant_id and e.device_id = p.device_id and e.seq < p.seq
			                  and (e.replay_status = 'PENDING' or (e.replay_status = 'CONFLICT' and e.resolution = '')))
			order by p.seq limit 1`, tenantID(ctx), deviceID))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		out = &t
		return nil
	})
	return out, err
}

// Settle moves a PENDING transaction to APPLIED, CONFLICT or REJECTED. A
// transaction that is no longer PENDING (another replay settled it first) is
// returned unchanged. A CONFLICT emits dev.passage.mobile.offline.conflict.v1.
func (s *Store) Settle(ctx context.Context, key, status, reason string) (OfflineTx, error) {
	var out OfflineTx
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		t, err := scanTx(tx.QueryRow(ctx, `update offline_transaction set replay_status = $3, conflict_reason = $4, version = version + 1
			where tenant_id = $1 and idempotency_key = $2 and replay_status = 'PENDING' returning `+txColumns, tenantID(ctx), key, status, reason))
		if errors.Is(err, pgx.ErrNoRows) {
			out, err = scanTx(tx.QueryRow(ctx, `select `+txColumns+` from offline_transaction where tenant_id = $1 and idempotency_key = $2`, tenantID(ctx), key))
			return err
		}
		if err != nil {
			return err
		}
		out = t
		if status == TxConflict {
			return enqueue(ctx, tx, "offline-transaction", key, evts.OfflineConflict,
				evts.OfflineConflictData{IdempotencyKey: key, Command: t.Command, Reason: reason})
		}
		return nil
	})
	return out, err
}

func (s *Store) GetOffline(ctx context.Context, key string) (OfflineTx, error) {
	var out OfflineTx
	err := s.inTx(ctx, func(tx pgx.Tx) (err error) {
		out, err = scanTx(tx.QueryRow(ctx, `select `+txColumns+` from offline_transaction where tenant_id = $1 and idempotency_key = $2`, tenantID(ctx), key))
		return err
	})
	return out, err
}

// DeviceSkew is the server clock minus the device clock at the last heartbeat.
func (s *Store) DeviceSkew(ctx context.Context, deviceID string) (time.Duration, error) {
	var ms int64
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select skew_ms from mobile_device where tenant_id = $1 and device_id = $2`, tenantID(ctx), deviceID).Scan(&ms)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return time.Duration(ms) * time.Millisecond, err
}

// ListConflicts is the supervisor queue: CONFLICT transactions nobody has
// resolved, oldest capture first, after the cursor key.
func (s *Store) ListConflicts(ctx context.Context, after time.Time, afterKey string, limit int) ([]OfflineTx, error) {
	var out []OfflineTx
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+txColumns+` from offline_transaction
			where tenant_id = $1 and replay_status = 'CONFLICT' and resolution = '' and (captured_at, idempotency_key) > ($2, $3)
			order by captured_at, idempotency_key limit $4`, tenantID(ctx), after, afterKey, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTx(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// Resolve records a supervisor's decision on a CONFLICT (BR-32.4). ifMatch is
// the conflict's version, so two supervisors cannot both resolve it. REJECT
// ends the transaction as REJECTED; REAPPLY keeps it CONFLICT with the
// resolution set and tells consumers through dev.passage.mobile.offline.resolved.v1
// to move the inventory as scanned.
func (s *Store) Resolve(ctx context.Context, idem Idem, key, action string, ifMatch int) (OfflineTx, bool, error) {
	var out OfflineTx
	var replayed bool
	err := s.inTx(ctx, func(tx pgx.Tx) (err error) {
		out, replayed, err = once(ctx, tx, idem, 200, func() (OfflineTx, error) {
			status := TxConflict
			if action == ResolveReject {
				status = TxRejected
			}
			t, err := scanTx(tx.QueryRow(ctx, `update offline_transaction set replay_status = $3, resolution = $4, resolved_by = $5, resolved_at = now(), version = version + 1
				where tenant_id = $1 and idempotency_key = $2 and replay_status = 'CONFLICT' and resolution = '' and ($6 = 0 or version = $6) returning `+txColumns,
				tenantID(ctx), key, status, action, userID(ctx), ifMatch))
			if errors.Is(err, pgx.ErrNoRows) {
				var exists bool
				if qerr := tx.QueryRow(ctx, `select true from offline_transaction where tenant_id = $1 and idempotency_key = $2`, tenantID(ctx), key).Scan(&exists); qerr != nil {
					return OfflineTx{}, ErrNotFound
				}
				return OfflineTx{}, ErrVersionConflict
			}
			if err != nil {
				return OfflineTx{}, err
			}
			return t, enqueue(ctx, tx, "offline-transaction", key, evts.OfflineResolved,
				evts.OfflineResolvedData{IdempotencyKey: key, Command: t.Command, Action: action, ResolvedBy: userID(ctx)})
		})
		return err
	})
	return out, replayed, err
}
