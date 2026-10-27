package store

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
)

// CreateDoor stores a dock door. A code already used in the warehouse is
// ErrConflict.
func (s *Store) CreateDoor(ctx context.Context, idem Idem, d domain.DockDoor) (domain.DockDoor, bool, error) {
	var (
		out      domain.DockDoor
		replayed bool
	)
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = once(ctx, tx, idem, http.StatusCreated, func() (domain.DockDoor, error) {
			err := tx.QueryRow(ctx, `insert into dock_door (tenant_id, id, warehouse_id, code) values ($1, $2, $3, $4)
				returning created_at`, tenantID(ctx), d.ID, d.WarehouseID, d.Code).Scan(&d.CreatedAt)
			return d, err
		})
		return err
	})
	return out, replayed, err
}

// ListDoors returns the doors of a warehouse ordered by code.
func (s *Store) ListDoors(ctx context.Context, warehouseID string) ([]domain.DockDoor, error) {
	var out []domain.DockDoor
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, warehouse_id::text, code, created_at from dock_door
			where warehouse_id = $1 order by code limit $2`, warehouseID, MaxPageSize)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d domain.DockDoor
			if err := rows.Scan(&d.ID, &d.WarehouseID, &d.Code, &d.CreatedAt); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// CreateAppointment books a door for a window (FR-31.8). The door row is
// locked first, so concurrent bookings of one door are serialized and the
// overlap check sees every committed appointment (AC-31.8). A door that is not
// in the appointment's warehouse is ErrNotFound; an overlap is WMS-0108.
func (s *Store) CreateAppointment(ctx context.Context, idem Idem, a domain.Appointment) (domain.Appointment, bool, error) {
	var (
		out      domain.Appointment
		replayed bool
	)
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = once(ctx, tx, idem, http.StatusCreated, func() (domain.Appointment, error) {
			if err := tx.QueryRow(ctx, `select code from dock_door where id = $1 and warehouse_id = $2 for update`,
				a.DoorID, a.WarehouseID).Scan(&a.DoorCode); err != nil {
				return a, err
			}
			existing, err := queryAppointments(ctx, tx, `where a.door_id = $1 and a.start_at < $3 and a.end_at > $2
				order by a.start_at`, a.DoorID, a.Start, a.End)
			if err != nil {
				return a, err
			}
			if err := domain.CheckDoorSlot(existing, a); err != nil {
				return a, err
			}
			err = tx.QueryRow(ctx, `insert into dock_appointment (tenant_id, id, warehouse_id, door_id, direction, reference, start_at, end_at)
				values ($1, $2, $3, $4, $5, $6, $7, $8) returning created_at`,
				tenantID(ctx), a.ID, a.WarehouseID, a.DoorID, string(a.Direction), a.Reference, a.Start, a.End).Scan(&a.CreatedAt)
			return a, err
		})
		return err
	})
	return out, replayed, err
}

// AppointmentFilter narrows ListAppointments to a warehouse, optionally one
// door, and the appointments overlapping [From, To); a zero bound is open.
type AppointmentFilter struct {
	WarehouseID string
	DoorID      string
	From, To    time.Time
	Limit       int
}

// ListAppointments returns a warehouse's appointments in the window, earliest
// first, with each door's code.
func (s *Store) ListAppointments(ctx context.Context, f AppointmentFilter) ([]domain.Appointment, error) {
	var out []domain.Appointment
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var from, to *time.Time
		if !f.From.IsZero() {
			from = &f.From
		}
		if !f.To.IsZero() {
			to = &f.To
		}
		var err error
		out, err = queryAppointments(ctx, tx, `where a.warehouse_id = $1 and ($2::text = '' or a.door_id = $2::uuid)
			and ($3::timestamptz is null or a.end_at > $3) and ($4::timestamptz is null or a.start_at < $4)
			order by a.start_at, d.code limit $5`, f.WarehouseID, f.DoorID, from, to, f.Limit)
		return err
	})
	return out, err
}

func queryAppointments(ctx context.Context, tx pgx.Tx, tail string, args ...any) ([]domain.Appointment, error) {
	rows, err := tx.Query(ctx, `select a.id::text, a.warehouse_id::text, a.door_id::text, d.code, a.direction, a.reference,
		a.start_at, a.end_at, a.created_at
		from dock_appointment a join dock_door d on d.tenant_id = a.tenant_id and d.id = a.door_id `+tail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Appointment
	for rows.Next() {
		var a domain.Appointment
		var dir string
		if err := rows.Scan(&a.ID, &a.WarehouseID, &a.DoorID, &a.DoorCode, &dir, &a.Reference, &a.Start, &a.End, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.Direction = domain.AppointmentDirection(dir)
		a.Start, a.End, a.CreatedAt = a.Start.UTC(), a.End.UTC(), a.CreatedAt.UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}
