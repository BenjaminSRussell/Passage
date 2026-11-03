package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
	evts "github.com/BenjaminSRussell/passage/services/warehouse/internal/events"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/inventory"
)

// PlanInput is a CreateWorkFromOrderLines command (§6.31.5).
type PlanInput struct {
	WarehouseID string
	WorkType    string
	Priority    int
	Lines       []domain.NewLine
}

// Planned is a new work and, per line, the directive result with its trace.
type Planned struct {
	Work   domain.Work     `json:"work"`
	Traces []domain.Result `json:"traces"`
}

// PlanWork creates a work and chooses each line's location with the
// warehouse's directives (BR-31.1). Directives, locations and every open
// line's reservation (BR-31.2) are read in the same transaction as the
// insert. Planning takes no location locks, so two concurrent plans can pick
// the same room; confirmation checks capacity again under a row lock (FR-31.1).
func (s *Store) PlanWork(ctx context.Context, idem Idem, in PlanInput) (Planned, bool, error) {
	var (
		out      Planned
		replayed bool
	)
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = once(ctx, tx, idem, http.StatusCreated, func() (Planned, error) {
			return planWork(ctx, tx, in)
		})
		return err
	})
	return out, replayed, err
}

// planWork places and inserts a work within tx and enqueues work.created.
// PlanWork and receiving share it.
func planWork(ctx context.Context, tx pgx.Tx, in PlanInput) (Planned, error) {
	dirs, err := loadDirectives(ctx, tx, in.WarehouseID, in.WorkType, "")
	if err != nil {
		return Planned{}, err
	}
	locs, err := loadLocations(ctx, tx, in.WarehouseID)
	if err != nil {
		return Planned{}, err
	}
	open, err := openLines(ctx, tx, in.WarehouseID)
	if err != nil {
		return Planned{}, err
	}
	domain.Reserve(locs, open)
	w, res, err := domain.Plan(ids.New(), in.WarehouseID, in.WorkType, in.Priority, in.Lines, dirs, locs, domain.Compliance{})
	if err != nil {
		return Planned{}, err
	}
	if err := insertWork(ctx, tx, &w); err != nil {
		return Planned{}, err
	}
	if err := enqueue(ctx, tx, w.ID, evts.WorkCreated, createdData(w)); err != nil {
		return Planned{}, err
	}
	return Planned{Work: w, Traces: res}, nil
}

// openLines returns the OPEN lines of the warehouse's live work: the
// reservations BR-31.2 subtracts from remaining capacity and stock.
func openLines(ctx context.Context, tx pgx.Tx, warehouseID string) ([]domain.WorkLine, error) {
	rows, err := tx.Query(ctx, `select l.item_id, l.direction, l.qty::text, l.weight::text, l.volume::text, l.lps,
		coalesce(l.from_location_id::text, ''), coalesce(l.to_location_id::text, ''), w.id::text, w.work_type = 'REPLENISHMENT'
		from work_line l join work w on w.tenant_id = l.tenant_id and w.id = l.work_id
		where w.warehouse_id = $1 and l.status = 'OPEN' and w.status in ('OPEN', 'WAITING', 'ASSIGNED', 'IN_PROGRESS')`, warehouseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.WorkLine
	for rows.Next() {
		l := domain.WorkLine{Status: domain.LineOpen}
		var dir, qty, weight, volume string
		if err := rows.Scan(&l.ItemID, &dir, &qty, &weight, &volume, &l.LPs, &l.FromLocationID, &l.ToLocationID, &l.WorkID, &l.Refill); err != nil {
			return nil, err
		}
		l.Direction = domain.Direction(dir)
		if l.Qty, err = dec(qty); err != nil {
			return nil, err
		}
		if l.Weight, err = dec(weight); err != nil {
			return nil, err
		}
		if l.Volume, err = dec(volume); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// WorkKey is the keyset cursor of the work list: priority, creation time, id.
type WorkKey struct {
	Priority  int
	CreatedAt time.Time
	ID        string
}

// WorkFilter narrows ListWork. After is nil for the first page.
type WorkFilter struct {
	WarehouseID string
	Status      string
	After       *WorkKey
	Limit       int
}

// ListWork returns a page of a warehouse's work headers (no lines), highest
// priority (1) first, then oldest first.
func (s *Store) ListWork(ctx context.Context, f WorkFilter) ([]domain.Work, error) {
	var out []domain.Work
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		after := WorkKey{Priority: 0, CreatedAt: time.Time{}, ID: "00000000-0000-0000-0000-000000000000"}
		if f.After != nil {
			after = *f.After
		}
		rows, err := tx.Query(ctx, `select `+workColumns+` from work
			where warehouse_id = $1 and ($2::text = '' or status = $2::text) and (priority, created_at, id) > ($3, $4, $5::uuid)
			order by priority, created_at, id limit $6`, f.WarehouseID, f.Status, after.Priority, after.CreatedAt, after.ID, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w domain.Work
			if err := scanWork(rows, &w); err != nil {
				return err
			}
			w.Lines = []domain.WorkLine{}
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

// GetWork returns one work with its lines.
func (s *Store) GetWork(ctx context.Context, id string) (domain.Work, error) {
	var out domain.Work
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = readWork(ctx, tx, id, false)
		return err
	})
	return out, err
}

// Assigned is the answer of AssignNext: the work given to the user, or nil
// when the warehouse has no assignable work.
type Assigned struct {
	Work *domain.Work `json:"work"`
}

// AssignNext gives the highest-priority OPEN work of a warehouse to user
// (FR-31.7). ASSIGNED work idle past timeout returns to OPEN first (BR-31.3).
// Both steps take rows with FOR UPDATE SKIP LOCKED (BR-31.8), so concurrent
// callers never receive the same work and never wait on each other.
func (s *Store) AssignNext(ctx context.Context, idem Idem, warehouseID, user string, timeout time.Duration, now time.Time) (Assigned, bool, error) {
	var (
		out      Assigned
		replayed bool
	)
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = once(ctx, tx, idem, http.StatusOK, func() (Assigned, error) {
			if _, err := tx.Exec(ctx, `update work set status = 'OPEN', assigned_user = '', assigned_at = null,
				version = version + 1, updated_at = now()
				where (tenant_id, id) in (select tenant_id, id from work
					where warehouse_id = $1 and status = 'ASSIGNED' and assigned_at < $2 for update skip locked)`,
				warehouseID, now.Add(-timeout)); err != nil {
				return Assigned{}, err
			}
			var id string
			err := tx.QueryRow(ctx, `select id::text from work where warehouse_id = $1 and status = 'OPEN'
				order by priority, created_at, id for update skip locked limit 1`, warehouseID).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				return Assigned{}, nil
			}
			if err != nil {
				return Assigned{}, err
			}
			w, err := readWork(ctx, tx, id, false)
			if err != nil {
				return Assigned{}, err
			}
			if err := w.Assign(user, now); err != nil {
				return Assigned{}, err
			}
			if err := updateWork(ctx, tx, &w); err != nil {
				return Assigned{}, err
			}
			return Assigned{Work: &w}, nil
		})
		return err
	})
	return out, replayed, err
}

// ConfirmInput is a ConfirmWorkLine command. IfMatch is the work version the
// caller last saw, or 0 to skip the check.
type ConfirmInput struct {
	WorkID  string
	LineNo  int
	Qty     decimal.Decimal
	User    string
	IfMatch int
	Now     time.Time
}

// ConfirmLine confirms one line at its open quantity and moves the stock at
// the line's location (PUT receives, PICK issues) under row locks on the work
// and the location. A refused move (capacity, mixing, stock) rolls the whole
// confirmation back. The last open line completes the work and emits
// work.completed in the same transaction.
func (s *Store) ConfirmLine(ctx context.Context, idem Idem, in ConfirmInput) (domain.Work, bool, error) {
	var (
		out      domain.Work
		replayed bool
	)
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = once(ctx, tx, idem, http.StatusOK, func() (domain.Work, error) {
			w, err := lockWork(ctx, tx, in.WorkID, in.IfMatch)
			if err != nil {
				return w, err
			}
			again, err := w.ConfirmLine(in.LineNo, in.Qty, idem.Key, in.User)
			if err != nil || again {
				return w, err
			}
			line, err := w.Line(in.LineNo)
			if err != nil {
				return w, err
			}
			if err := s.moveStock(ctx, tx, w.WarehouseID, line); err != nil {
				return w, err
			}
			if err := updateWork(ctx, tx, &w); err != nil {
				return w, err
			}
			if w.Completed() {
				if err := enqueue(ctx, tx, w.ID, evts.WorkCompleted, completedData(w, in.Now)); err != nil {
					return w, err
				}
				if err := releaseDependents(ctx, tx, w); err != nil {
					return w, err
				}
			}
			return w, nil
		})
		return err
	})
	return out, replayed, err
}

// Exception actions of POST /work/{id}:exception.
const (
	ActionShortPick = "SHORT_PICK"
	ActionCancel    = "CANCEL"
)

// ExceptionInput is a work exception. Supervise is true when the caller holds
// the supervise duty, which allows cancelling work assigned to someone else.
type ExceptionInput struct {
	WorkID    string
	Action    string
	LineNo    int
	Qty       decimal.Decimal
	Reason    string
	User      string
	Supervise bool
	IfMatch   int
	Now       time.Time
}

// Excepted is the work after an exception and, for a short pick, the new
// OPEN remainder work.
type Excepted struct {
	Work      domain.Work  `json:"work"`
	Remainder *domain.Work `json:"remainder"`
}

// ExceptWork applies SHORT_PICK (confirm a PICK line below its quantity and
// re-queue the rest as a child work, AC-31.5) or CANCEL (no line confirmed,
// BR-31.3). Stock moves only for the quantity actually picked.
func (s *Store) ExceptWork(ctx context.Context, idem Idem, in ExceptionInput) (Excepted, bool, error) {
	var (
		out      Excepted
		replayed bool
	)
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = once(ctx, tx, idem, http.StatusOK, func() (Excepted, error) {
			w, err := lockWork(ctx, tx, in.WorkID, in.IfMatch)
			if err != nil {
				return Excepted{}, err
			}
			switch in.Action {
			case ActionShortPick:
				return s.shortPick(ctx, tx, w, in)
			case ActionCancel:
				if err := w.Cancel(in.Reason, in.User, in.Supervise); err != nil {
					return Excepted{}, err
				}
				if err := updateWork(ctx, tx, &w); err != nil {
					return Excepted{}, err
				}
				if err := releaseDependents(ctx, tx, w); err != nil {
					return Excepted{}, err
				}
				return Excepted{Work: w}, nil
			}
			return Excepted{}, fmt.Errorf("store: unknown exception %q", in.Action)
		})
		return err
	})
	return out, replayed, err
}

func (s *Store) shortPick(ctx context.Context, tx pgx.Tx, w domain.Work, in ExceptionInput) (Excepted, error) {
	remainder, err := w.ShortPick(in.LineNo, in.Qty, in.Reason, ids.New(), in.User)
	if err != nil {
		return Excepted{}, err
	}
	line, err := w.Line(in.LineNo)
	if err != nil {
		return Excepted{}, err
	}
	if err := s.moveStock(ctx, tx, w.WarehouseID, line); err != nil {
		return Excepted{}, err
	}
	if err := updateWork(ctx, tx, &w); err != nil {
		return Excepted{}, err
	}
	if err := insertWork(ctx, tx, remainder); err != nil {
		return Excepted{}, err
	}
	if w.Completed() {
		if err := enqueue(ctx, tx, w.ID, evts.WorkCompleted, completedData(w, in.Now)); err != nil {
			return Excepted{}, err
		}
		if err := releaseDependents(ctx, tx, w); err != nil {
			return Excepted{}, err
		}
	}
	if err := enqueue(ctx, tx, remainder.ID, evts.WorkCreated, createdData(*remainder)); err != nil {
		return Excepted{}, err
	}
	return Excepted{Work: w, Remainder: remainder}, nil
}

// lockWork reads a work under a row lock and checks the caller's If-Match.
func lockWork(ctx context.Context, tx pgx.Tx, id string, ifMatch int) (domain.Work, error) {
	w, err := readWork(ctx, tx, id, true)
	if err != nil {
		return w, err
	}
	if ifMatch != 0 && ifMatch != w.Version {
		return w, fmt.Errorf("%w: current version is %d", ErrVersionConflict, w.Version)
	}
	return w, nil
}

// moveStock applies a confirmed line to its location under a row lock.
func (s *Store) moveStock(ctx context.Context, tx pgx.Tx, warehouseID string, line domain.WorkLine) error {
	if line.ConfirmedQty.IsZero() {
		return nil
	}
	locID := line.LocationOf()
	if locID == "" {
		return &domain.Error{Code: domain.CodeNoLocation, Detail: fmt.Sprintf("line %d has no location", line.LineNo)}
	}
	loc, err := readLocation(ctx, tx, locID)
	if err != nil {
		return err
	}
	if err := s.checkHold(ctx, warehouseID, loc, line); err != nil {
		return err
	}
	if refusal, ok := loc.Move(line.Direction, line.ItemID, line.ConfirmedQty, line.ConfirmedUsage()); !ok {
		return domain.MoveError(refusal)
	}
	return writeLocation(ctx, tx, loc, line.ItemID)
}

func (s *Store) checkHold(ctx context.Context, warehouseID string, loc domain.Location, line domain.WorkLine) error {
	if s.holds == nil || line.Direction != domain.DirectionPick {
		return nil
	}
	p, _ := tenant.FromContext(ctx)
	batch := ""
	if line.Attributes != nil {
		batch = line.Attributes["batch"]
	}
	lot, err := s.holds.Lot(ctx, p.LegalEntityID, line.ItemID, warehouseID, batch)
	if err != nil {
		return &domain.Error{Code: domain.CodeHoldBlocked, Detail: fmt.Sprintf("inventory hold check failed: %v", err)}
	}
	physical := loc.Stock[line.ItemID]
	if inventory.Blocks(physical, lot.Held, line.ConfirmedQty) {
		return &domain.Error{Code: domain.CodeHoldBlocked,
			Detail: fmt.Sprintf("item %s is on quality hold; pick not allowed", line.ItemID)}
	}
	return nil
}

const workColumns = `id::text, warehouse_id::text, work_type, status, priority, coalesce(parent_work_id::text, ''),
	assigned_user, assigned_at, version, created_at, updated_at, coalesce(depends_on::text[], '{}')`

func scanWork(row scanner, w *domain.Work) error {
	var status string
	if err := row.Scan(&w.ID, &w.WarehouseID, &w.WorkType, &status, &w.Priority, &w.ParentID,
		&w.AssignedUser, &w.AssignedAt, &w.Version, &w.CreatedAt, &w.UpdatedAt, &w.DependsOn); err != nil {
		return err
	}
	w.Status = domain.WorkStatus(status)
	return nil
}

// readWork loads a work header and its lines; lock takes a row lock on the header.
func readWork(ctx context.Context, tx pgx.Tx, id string, lock bool) (domain.Work, error) {
	var w domain.Work
	q := `select ` + workColumns + ` from work where id = $1`
	if lock {
		q += ` for update`
	}
	if err := scanWork(tx.QueryRow(ctx, q, id), &w); err != nil {
		return w, err
	}
	rows, err := tx.Query(ctx, `select line_no, item_id, direction, qty::text, confirmed_qty::text, weight::text, volume::text,
		lps, coalesce(from_location_id::text, ''), coalesce(to_location_id::text, ''), status, reason,
		coalesce(confirm_key, ''), attributes
		from work_line where work_id = $1 order by line_no`, w.ID)
	if err != nil {
		return w, err
	}
	defer rows.Close()
	for rows.Next() {
		var l domain.WorkLine
		var dir, status, qty, conf, weight, volume string
		var attrs []byte
		if err := rows.Scan(&l.LineNo, &l.ItemID, &dir, &qty, &conf, &weight, &volume, &l.LPs,
			&l.FromLocationID, &l.ToLocationID, &status, &l.Reason, &l.ConfirmKey, &attrs); err != nil {
			return w, err
		}
		l.Direction = domain.Direction(dir)
		l.Status = domain.LineStatus(status)
		for _, f := range []struct {
			raw string
			dst *decimal.Decimal
		}{{qty, &l.Qty}, {conf, &l.ConfirmedQty}, {weight, &l.Weight}, {volume, &l.Volume}} {
			if *f.dst, err = dec(f.raw); err != nil {
				return w, err
			}
		}
		if err := json.Unmarshal(attrs, &l.Attributes); err != nil {
			return w, err
		}
		if len(l.Attributes) == 0 {
			l.Attributes = nil
		}
		w.Lines = append(w.Lines, l)
	}
	return w, rows.Err()
}

// insertWork writes a new work header and its lines, and sets the stored
// version and timestamps on w.
func insertWork(ctx context.Context, tx pgx.Tx, w *domain.Work) error {
	if err := tx.QueryRow(ctx, `insert into work (tenant_id, id, warehouse_id, work_type, status, priority, parent_work_id,
		assigned_user, assigned_at, depends_on) values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::text[]::uuid[]) returning version, created_at, updated_at`,
		tenantID(ctx), w.ID, w.WarehouseID, w.WorkType, string(w.Status), w.Priority, nullable(w.ParentID),
		w.AssignedUser, w.AssignedAt, append([]string{}, w.DependsOn...)).Scan(&w.Version, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return err
	}
	for _, l := range w.Lines {
		attrs, err := json.Marshal(l.Attributes)
		if err != nil {
			return err
		}
		if l.Attributes == nil {
			attrs = []byte("{}")
		}
		if _, err := tx.Exec(ctx, `insert into work_line (tenant_id, work_id, line_no, item_id, direction, qty, confirmed_qty,
			weight, volume, lps, from_location_id, to_location_id, status, reason, confirm_key, attributes)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
			tenantID(ctx), w.ID, l.LineNo, l.ItemID, string(l.Direction), l.Qty, l.ConfirmedQty, l.Weight, l.Volume, l.LPs,
			nullable(l.FromLocationID), nullable(l.ToLocationID), string(l.Status), l.Reason, nullable(l.ConfirmKey), attrs); err != nil {
			return err
		}
	}
	return nil
}

// updateWork writes the mutable state of a work header and its lines, and
// sets the new version and timestamp on w.
func updateWork(ctx context.Context, tx pgx.Tx, w *domain.Work) error {
	if err := tx.QueryRow(ctx, `update work set status = $2, priority = $3, assigned_user = $4, assigned_at = $5,
		version = version + 1, updated_at = now() where id = $1 returning version, updated_at`,
		w.ID, string(w.Status), w.Priority, w.AssignedUser, w.AssignedAt).Scan(&w.Version, &w.UpdatedAt); err != nil {
		return err
	}
	for _, l := range w.Lines {
		if _, err := tx.Exec(ctx, `update work_line set confirmed_qty = $3, status = $4, reason = $5, confirm_key = $6
			where work_id = $1 and line_no = $2`,
			w.ID, l.LineNo, l.ConfirmedQty, string(l.Status), l.Reason, nullable(l.ConfirmKey)); err != nil {
			return err
		}
	}
	return nil
}

func createdData(w domain.Work) evts.WorkCreatedData {
	return evts.WorkCreatedData{WorkID: w.ID, WorkType: w.WorkType, WarehouseID: w.WarehouseID, Priority: w.Priority,
		Status: string(w.Status), ParentWorkID: w.ParentID, Lines: eventLines(w.Lines)}
}

func completedData(w domain.Work, now time.Time) evts.WorkCompletedData {
	var secs int64
	if w.AssignedAt != nil {
		secs = int64(now.Sub(*w.AssignedAt).Seconds())
	}
	return evts.WorkCompletedData{WorkID: w.ID, WorkType: w.WorkType, WarehouseID: w.WarehouseID, Status: string(w.Status),
		User: w.AssignedUser, DurationS: secs, Lines: eventLines(w.Lines)}
}

func eventLines(lines []domain.WorkLine) []evts.Line {
	out := make([]evts.Line, 0, len(lines))
	for _, l := range lines {
		out = append(out, evts.Line{LineNo: l.LineNo, ItemID: l.ItemID, Direction: string(l.Direction),
			Qty: l.Qty.String(), ConfirmedQty: l.ConfirmedQty.String(), Status: string(l.Status),
			FromLocationID: l.FromLocationID, ToLocationID: l.ToLocationID})
	}
	return out
}

// releaseDependents settles the work waiting on a refill that has just ended.
// Dependents are locked first, in id order, and only then updated. A COMPLETED
// or CANCELLED refill is removed from their DependsOn; a WAITING work with
// nothing left to wait for becomes OPEN and work.released is emitted. A
// PARTIALLY_COMPLETED refill (short picked) hands the wait to its remainder
// works: they wait for those instead, and release when those end.
func releaseDependents(ctx context.Context, tx pgx.Tx, w domain.Work) error {
	if w.WorkType != "REPLENISHMENT" || !(w.Status == domain.WorkCompleted || w.Status == domain.WorkCancelled || w.Status == domain.WorkPartiallyComplete) {
		return nil
	}
	rows, err := tx.Query(ctx, `select id::text, status from work where warehouse_id = $1 and depends_on @> array[$2::uuid]
		order by id for update`, w.WarehouseID, w.ID)
	if err != nil {
		return err
	}
	before := map[string]string{}
	var locked []string
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			rows.Close()
			return err
		}
		before[id] = status
		locked = append(locked, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(locked) == 0 {
		return err
	}
	if w.Status == domain.WorkPartiallyComplete {
		var children []string
		crows, err := tx.Query(ctx, `select id::text from work where parent_work_id = $1 and work_type = 'REPLENISHMENT' order by id`, w.ID)
		if err != nil {
			return err
		}
		for crows.Next() {
			var id string
			if err := crows.Scan(&id); err != nil {
				crows.Close()
				return err
			}
			children = append(children, id)
		}
		crows.Close()
		if err := crows.Err(); err != nil {
			return err
		}
		if len(children) > 0 {
			_, err := tx.Exec(ctx, `update work set depends_on = array_remove(depends_on, $1::uuid) || $2::text[]::uuid[],
				version = version + 1, updated_at = now() where id = any($3::text[]::uuid[])`, w.ID, children, locked)
			return err
		}
	}
	urows, err := tx.Query(ctx, `update work set depends_on = array_remove(depends_on, $1::uuid),
		status = case when status = 'WAITING' and cardinality(array_remove(depends_on, $1::uuid)) = 0 then 'OPEN' else status end,
		version = version + 1, updated_at = now() where id = any($2::text[]::uuid[]) returning id::text, status`, w.ID, locked)
	if err != nil {
		return err
	}
	var released []string
	for urows.Next() {
		var id, status string
		if err := urows.Scan(&id, &status); err != nil {
			urows.Close()
			return err
		}
		if before[id] == string(domain.WorkWaiting) && status == string(domain.WorkOpen) {
			released = append(released, id)
		}
	}
	urows.Close()
	if err := urows.Err(); err != nil {
		return err
	}
	for _, id := range released {
		if err := enqueue(ctx, tx, id, evts.WorkReleased, evts.WorkReleasedData{WorkID: id, WarehouseID: w.WarehouseID, AfterWorkID: w.ID, AfterStatus: string(w.Status)}); err != nil {
			return err
		}
	}
	return nil
}
