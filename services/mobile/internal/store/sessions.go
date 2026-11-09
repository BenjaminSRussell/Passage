package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	evts "github.com/BenjaminSRussell/passage/services/mobile/internal/events"
	"github.com/BenjaminSRussell/passage/services/mobile/internal/session"
)

var (
	ErrSessionActive = errors.New("store: the device has an active session of another user")
	ErrSessionEnded  = errors.New("store: the session has ended")
	ErrNotOwner      = errors.New("store: the session belongs to another user")
)

type Device struct {
	DeviceID       string     `json:"deviceId"`
	Model          string     `json:"model"`
	ScannerProfile string     `json:"scannerProfile"`
	AppVersion     string     `json:"appVersion"`
	WarehouseID    string     `json:"warehouseId,omitempty"`
	MenuKey        string     `json:"menuKey"`
	SkewMs         int64      `json:"skewMs"`
	LastSeenAt     *time.Time `json:"lastSeenAt,omitempty"`
}

const deviceColumns = `device_id, model, scanner_profile, app_version, coalesce(warehouse_id::text, ''), menu_key, skew_ms, last_seen_at`

func scanDevice(row scanner) (Device, error) {
	var d Device
	err := row.Scan(&d.DeviceID, &d.Model, &d.ScannerProfile, &d.AppVersion, &d.WarehouseID, &d.MenuKey, &d.SkewMs, &d.LastSeenAt)
	return d, err
}

// RegisterDevice creates or updates a device's profile and bindings.
func (s *Store) RegisterDevice(ctx context.Context, d Device) (Device, error) {
	var out Device
	err := s.inTx(ctx, func(tx pgx.Tx) (err error) {
		var wh any
		if d.WarehouseID != "" {
			wh = d.WarehouseID
		}
		out, err = scanDevice(tx.QueryRow(ctx, `insert into mobile_device (tenant_id, device_id, model, scanner_profile, app_version, warehouse_id, menu_key)
			values ($1, $2, $3, $4, $5, $6::uuid, $7)
			on conflict (tenant_id, device_id) do update set model = excluded.model, scanner_profile = excluded.scanner_profile,
			  app_version = excluded.app_version, warehouse_id = excluded.warehouse_id, menu_key = excluded.menu_key
			returning `+deviceColumns, tenantID(ctx), d.DeviceID, d.Model, d.ScannerProfile, d.AppVersion, wh, d.MenuKey))
		return err
	})
	return out, err
}

func (s *Store) GetDevice(ctx context.Context, deviceID string) (Device, error) {
	var d Device
	err := s.inTx(ctx, func(tx pgx.Tx) (err error) {
		d, err = scanDevice(tx.QueryRow(ctx, `select `+deviceColumns+` from mobile_device where tenant_id = $1 and device_id = $2`, tenantID(ctx), deviceID))
		return err
	})
	return d, err
}

func (s *Store) ListDevices(ctx context.Context, limit int) ([]Device, error) {
	var out []Device
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+deviceColumns+` from mobile_device where tenant_id = $1 order by device_id limit $2`, tenantID(ctx), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDevice(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

type Session struct {
	SessionID     string          `json:"sessionId"`
	DeviceID      string          `json:"deviceId"`
	UserID        string          `json:"userId"`
	WarehouseID   string          `json:"warehouseId"`
	MenuKey       string          `json:"menuKey"`
	CurrentWorkID string          `json:"currentWorkId,omitempty"`
	State         json.RawMessage `json:"state"`
	Status        string          `json:"status"`
	EndReason     string          `json:"endReason,omitempty"`
	Version       int             `json:"version"`
	StartedAt     time.Time       `json:"startedAt"`
	LastSeenAt    time.Time       `json:"lastSeenAt"`
	EndedAt       *time.Time      `json:"endedAt,omitempty"`
}

func (s Session) View() session.View {
	return session.View{ID: s.SessionID, DeviceID: s.DeviceID, UserID: s.UserID, CurrentWorkID: s.CurrentWorkID, LastSeenAt: s.LastSeenAt, Status: session.State(s.Status)}
}

const sessionColumns = `session_id::text, device_id, user_id, warehouse_id::text, menu_key, current_work_id, state, status, end_reason, version, started_at, last_seen_at, ended_at`

func scanSession(row scanner) (Session, error) {
	var x Session
	err := row.Scan(&x.SessionID, &x.DeviceID, &x.UserID, &x.WarehouseID, &x.MenuKey, &x.CurrentWorkID, &x.State, &x.Status, &x.EndReason, &x.Version, &x.StartedAt, &x.LastSeenAt, &x.EndedAt)
	return x, err
}

type StartInput struct {
	DeviceID    string
	WarehouseID string
	MenuKey     string
	Now         time.Time
	Idle        time.Duration
}

// StartSession binds a device to the caller, a warehouse and a menu. The
// caller's own open session on the device is returned as it is (resumed); an
// open session of another user stops the login unless it has been silent past
// the idle timeout, in which case it is ended as a takeover and its work lock
// is released.
func (s *Store) StartSession(ctx context.Context, in StartInput) (Session, bool, error) {
	var out Session
	var resumed bool
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		me := userID(ctx)
		if _, err := tx.Exec(ctx, `insert into mobile_device (tenant_id, device_id) values ($1, $2) on conflict do nothing`, tenantID(ctx), in.DeviceID); err != nil {
			return err
		}
		cur, err := scanSession(tx.QueryRow(ctx, `select `+sessionColumns+` from device_session
			where tenant_id = $1 and device_id = $2 and status in ('ACTIVE', 'STALE') for update`, tenantID(ctx), in.DeviceID))
		switch {
		case err == nil && cur.UserID == me:
			out, resumed = cur, true
			return nil
		case err == nil:
			if session.StateAt(cur.View(), in.Now, in.Idle) == session.Active {
				return ErrSessionActive
			}
			if err := endSession(ctx, tx, cur, "TAKEOVER", in.Now); err != nil {
				return err
			}
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		out, err = scanSession(tx.QueryRow(ctx, `insert into device_session (tenant_id, session_id, device_id, user_id, warehouse_id, menu_key, status, started_at, last_seen_at)
			values ($1, $2, $3, $4, $5, $6, 'ACTIVE', $7, $7) returning `+sessionColumns,
			tenantID(ctx), ids.New(), in.DeviceID, me, in.WarehouseID, in.MenuKey, in.Now))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update mobile_device set last_seen_at = $3 where tenant_id = $1 and device_id = $2`, tenantID(ctx), in.DeviceID, in.Now); err != nil {
			return err
		}
		return enqueue(ctx, tx, "session", out.SessionID, evts.SessionStarted,
			evts.SessionData{SessionID: out.SessionID, DeviceID: out.DeviceID, UserID: out.UserID, Warehouse: out.WarehouseID})
	})
	return out, resumed, err
}

func endSession(ctx context.Context, tx pgx.Tx, x Session, reason string, now time.Time) error {
	if _, err := tx.Exec(ctx, `update device_session set status = 'ENDED', end_reason = $3, ended_at = $4, current_work_id = '', version = version + 1
		where tenant_id = $1 and session_id = $2`, tenantID(ctx), x.SessionID, reason, now); err != nil {
		return err
	}
	return enqueue(ctx, tx, "session", x.SessionID, evts.SessionEnded,
		evts.SessionData{SessionID: x.SessionID, DeviceID: x.DeviceID, UserID: x.UserID, Warehouse: x.WarehouseID, Reason: reason})
}

func (s *Store) GetSession(ctx context.Context, sessionID string) (Session, error) {
	var x Session
	err := s.inTx(ctx, func(tx pgx.Tx) (err error) {
		x, err = scanSession(tx.QueryRow(ctx, `select `+sessionColumns+` from device_session where tenant_id = $1 and session_id = $2`, tenantID(ctx), sessionID))
		return err
	})
	return x, err
}

// Heartbeat records the device's liveness and clock. A STALE session becomes
// ACTIVE again (wasStale reports it so the caller can decide whether its work
// lock may be re-taken); an ended session cannot be resumed. Only the
// session's user may beat it.
func (s *Store) Heartbeat(ctx context.Context, sessionID string, now time.Time, deviceTime *time.Time) (x Session, wasStale bool, err error) {
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := scanSession(tx.QueryRow(ctx, `select `+sessionColumns+` from device_session where tenant_id = $1 and session_id = $2 for update`, tenantID(ctx), sessionID))
		if err != nil {
			return err
		}
		if cur.UserID != userID(ctx) {
			return ErrNotOwner
		}
		if cur.Status == string(session.Ended) {
			return ErrSessionEnded
		}
		wasStale = cur.Status == string(session.Stale)
		x, err = scanSession(tx.QueryRow(ctx, `update device_session set status = 'ACTIVE', last_seen_at = $3, version = version + 1
			where tenant_id = $1 and session_id = $2 returning `+sessionColumns, tenantID(ctx), sessionID, now))
		if err != nil {
			return err
		}
		skew := int64(0)
		if deviceTime != nil {
			skew = now.Sub(*deviceTime).Milliseconds()
			_, err = tx.Exec(ctx, `update mobile_device set last_seen_at = $3, skew_ms = $4 where tenant_id = $1 and device_id = $2`, tenantID(ctx), x.DeviceID, now, skew)
		} else {
			_, err = tx.Exec(ctx, `update mobile_device set last_seen_at = $3 where tenant_id = $1 and device_id = $2`, tenantID(ctx), x.DeviceID, now)
		}
		return err
	})
	return x, wasStale, err
}

// EndSession signs a session out. The session's user may end it; force ends
// another user's (WMS_WORK_SUPERVISE) and records why.
func (s *Store) EndSession(ctx context.Context, sessionID, reason string, force bool, now time.Time) (Session, error) {
	var out Session
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		cur, err := scanSession(tx.QueryRow(ctx, `select `+sessionColumns+` from device_session where tenant_id = $1 and session_id = $2 for update`, tenantID(ctx), sessionID))
		if err != nil {
			return err
		}
		if cur.UserID != userID(ctx) && !force {
			return ErrNotOwner
		}
		if cur.Status == string(session.Ended) {
			out = cur
			return nil
		}
		if err := endSession(ctx, tx, cur, reason, now); err != nil {
			return err
		}
		out, err = scanSession(tx.QueryRow(ctx, `select `+sessionColumns+` from device_session where tenant_id = $1 and session_id = $2`, tenantID(ctx), sessionID))
		return err
	})
	return out, err
}

// SaveRun stores the run state and the work the session holds, if version
// still matches (a concurrent submit on the same session is a conflict).
func (s *Store) SaveRun(ctx context.Context, sessionID string, version int, state json.RawMessage, workID string, now time.Time) (Session, error) {
	var out Session
	err := s.inTx(ctx, func(tx pgx.Tx) (err error) {
		out, err = scanSession(tx.QueryRow(ctx, `update device_session set state = $4, current_work_id = $5, last_seen_at = $6, status = 'ACTIVE', version = version + 1
			where tenant_id = $1 and session_id = $2 and version = $3 and status in ('ACTIVE', 'STALE') and user_id = $7 returning `+sessionColumns,
			tenantID(ctx), sessionID, version, state, workID, now, userID(ctx)))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVersionConflict
		}
		return err
	})
	return out, err
}

func (s *Store) ListSessions(ctx context.Context, status, warehouseID string, limit int) ([]Session, error) {
	var out []Session
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+sessionColumns+` from device_session where tenant_id = $1
			and ($2 = '' or status = $2) and ($3 = '' or warehouse_id::text = $3) order by last_seen_at desc limit $4`, tenantID(ctx), status, warehouseID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			x, err := scanSession(rows)
			if err != nil {
				return err
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

// SessionsForWork lists the open sessions that name a work.
func (s *Store) SessionsForWork(ctx context.Context, workID string) ([]Session, error) {
	var out []Session
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+sessionColumns+` from device_session where tenant_id = $1 and current_work_id = $2 and status in ('ACTIVE', 'STALE')`, tenantID(ctx), workID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			x, err := scanSession(rows)
			if err != nil {
				return err
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}

// SweepStale marks the tenant's ACTIVE sessions silent past idle as STALE,
// which releases their work locks (BR-32.6), and returns how many it marked.
func (s *Store) SweepStale(ctx context.Context, now time.Time, idle time.Duration) (int, error) {
	var n int
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update device_session set status = 'STALE', version = version + 1
			where tenant_id = $1 and status = 'ACTIVE' and last_seen_at < $2`, tenantID(ctx), now.Add(-idle))
		n = int(tag.RowsAffected())
		return err
	})
	return n, err
}
